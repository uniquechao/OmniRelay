package group

import "strings"

// GroupReplyBody contains only content authorized for the bound group.
// Mention fields come from account bindings, never from model output or task titles.
type GroupReplyBody struct {
	Lines []ReplyLine `json:"lines"`
}

// ReplyLine displays the owner, when available, followed by Text.
type ReplyLine struct {
	Text          string `json:"text"`
	MentionOpenID string `json:"mention_open_id,omitempty"`
	MentionName   string `json:"mention_name,omitempty"`
}

func (body GroupReplyBody) PlainText() string {
	lines := make([]string, len(body.Lines))
	for i, line := range body.Lines {
		lines[i] = line.MentionName + line.Text
	}
	return strings.Join(lines, "\n")
}
