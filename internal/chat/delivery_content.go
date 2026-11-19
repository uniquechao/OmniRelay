package chat

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	maxAttachments = 5
	maxFileBytes   = 30 * 1024 * 1024
	maxImageBytes  = 10 * 1024 * 1024
)

var errDeliveryInput = errors.New("invalid delivery input")

// DeliveryContent accepts plain text or structured paragraphs. Emoji are Unicode
// text. Only the structured mention field can generate a platform mention.
type DeliveryContent struct {
	Text        string           `json:"text"`
	Paragraphs  [][]DeliverySpan `json:"paragraphs"`
	Attachments []string         `json:"attachments"`
}

type DeliverySpan struct {
	Text    string   `json:"text"`
	Styles  []string `json:"styles"`
	Mention string   `json:"mention_open_id"`
}

type deliveryPart struct {
	position                    int
	kind, content, attachmentID string
	state, reason, messageID    string
}

func validDeliveryText(text string) bool {
	return utf8.ValidString(text) && !strings.ContainsRune(text, 0) && len(text) <= 120_000
}

func (c DeliveryContent) parts() ([]deliveryPart, []string, error) {
	if !validDeliveryText(c.Text) || (c.Text != "" && len(c.Paragraphs) != 0) ||
		len(c.Paragraphs) > 100 || len(c.Attachments) > maxAttachments {
		return nil, nil, errDeliveryInput
	}
	var parts []deliveryPart
	var mentions []string
	if strings.TrimSpace(c.Text) != "" {
		// Feishu's text format interprets inline at-tags. post text nodes keep
		// literal input inert while preserving line breaks in that case.
		if strings.Contains(c.Text, "<") {
			if strings.Count(c.Text, "\n") >= 100 {
				return nil, nil, errDeliveryInput
			}
			c.Paragraphs = make([][]DeliverySpan, 0, strings.Count(c.Text, "\n")+1)
			for _, line := range strings.Split(c.Text, "\n") {
				c.Paragraphs = append(c.Paragraphs, []DeliverySpan{{Text: line}})
			}
		} else {
			body, _ := json.Marshal(map[string]string{"text": c.Text})
			if len(body) > 120_000 {
				return nil, nil, errDeliveryInput
			}
			parts = append(parts, deliveryPart{kind: "text", content: string(body)})
		}
	}
	if len(c.Paragraphs) != 0 {
		body, ids, err := richDelivery(c.Paragraphs)
		if err != nil {
			return nil, nil, err
		}
		mentions = ids
		parts = append(parts, deliveryPart{kind: "post", content: body})
	}
	for _, id := range c.Attachments {
		if !validIdentifier(id) || slices.ContainsFunc(parts, func(part deliveryPart) bool { return part.attachmentID == id }) {
			return nil, nil, errDeliveryInput
		}
		parts = append(parts, deliveryPart{kind: "file", attachmentID: id})
	}
	if len(parts) == 0 {
		return nil, nil, errDeliveryInput
	}
	return parts, mentions, nil
}

type postNode struct {
	Tag    string   `json:"tag"`
	Text   *string  `json:"text,omitempty"`
	Style  []string `json:"style,omitempty"`
	UserID string   `json:"user_id,omitempty"`
}

func richDelivery(paragraphs [][]DeliverySpan) (string, []string, error) {
	content := make([][]postNode, 0, len(paragraphs))
	var mentions []string
	hasContent := false
	textSize := 0
	for _, paragraph := range paragraphs {
		if len(paragraph) > 100 {
			return "", nil, errDeliveryInput
		}
		nodes := make([]postNode, 0, len(paragraph))
		for _, span := range paragraph {
			if span.Mention != "" {
				if !strings.HasPrefix(span.Mention, "ou_") || !validIdentifier(span.Mention) ||
					span.Text != "" || len(span.Styles) != 0 {
					return "", nil, errDeliveryInput
				}
				if !slices.Contains(mentions, span.Mention) {
					mentions = append(mentions, span.Mention)
				}
				nodes = append(nodes, postNode{Tag: "at", UserID: span.Mention})
				hasContent = true
				continue
			}
			if !validDeliveryText(span.Text) || len(span.Styles) > 4 {
				return "", nil, errDeliveryInput
			}
			textSize += len(span.Text)
			if textSize > 25_000 {
				return "", nil, errDeliveryInput
			}
			for _, style := range span.Styles {
				switch style {
				case "bold", "underline", "lineThrough", "italic":
				default:
					return "", nil, errDeliveryInput
				}
			}
			nodes = append(nodes, postNode{Tag: "text", Text: &span.Text, Style: span.Styles})
			hasContent = hasContent || strings.TrimSpace(span.Text) != ""
		}
		content = append(content, nodes)
	}
	body, _ := json.Marshal(map[string]any{"zh_cn": map[string]any{"content": content}})
	if !hasContent || len(mentions) > 20 || len(body) > 25_000 {
		return "", nil, errDeliveryInput
	}
	return string(body), mentions, nil
}
