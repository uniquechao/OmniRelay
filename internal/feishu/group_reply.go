package feishu

import (
	"context"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/orchestrator/group"
)

// ReplyTo addresses the inbound message. Start no new thread in an ordinary group.
func (s *ChatSource) ReplyTo(ctx context.Context, chatID, bot, messageID, threadID, text, uuid string, body *group.GroupReplyBody) chat.DeliveryResult {
	if !validIMID(chatID, "oc_") || !validIMID(messageID, "om_") || (threadID != "" && !validIMID(threadID, "omt_")) || uuid == "" {
		return chat.DeliveryResult{State: "failed", Reason: "invalid_target"}
	}
	var content string
	var err error
	if body == nil {
		content, err = groupTextPost(text)
	} else {
		members := s.groupMentionMembers(ctx, chatID, bot, body.Lines)
		content, _, err = chat.RichPost(groupReplyParagraphs(body.Lines, members))
	}
	if err != nil {
		return chat.DeliveryResult{State: "failed", Reason: "invalid_group_reply"}
	}
	return s.replyPayload(ctx, chatID, bot, messageID, uuid, "post", content, threadID != "")
}

const groupReplyRemainder = "其余内容可在个人项目工作台查看。"

func groupTextPost(text string) (string, error) {
	encode := func(value string) (string, error) {
		lines := strings.Split(value, "\n")
		paragraphs := make([][]chat.DeliverySpan, len(lines))
		for i, line := range lines {
			paragraphs[i] = []chat.DeliverySpan{{Text: line}}
		}
		content, _, err := chat.RichPost(paragraphs)
		return content, err
	}
	content, err := encode(text)
	if err == nil || !utf8.ValidString(text) || strings.ContainsRune(text, 0) || strings.TrimSpace(text) == "" {
		return content, err
	}
	// The cap applies to the encoded post, including escaped text and per-line
	// nodes. Preserve a whole-rune prefix that fits together with the notice.
	runes := []rune(text)
	const tail = "\n" + groupReplyRemainder
	tooLong := sort.Search(len(runes)+1, func(n int) bool {
		_, err := encode(string(runes[:n]) + tail)
		return err != nil
	})
	return encode(string(runes[:tooLong-1]) + tail)
}

// Resolve against this chat immediately before delivery. If Feishu can only
// confirm part of the membership, the remaining owners are displayed by name.
func (s *ChatSource) groupMentionMembers(ctx context.Context, chatID, bot string, lines []group.ReplyLine) map[string]bool {
	wanted := make(map[string]bool)
	for _, line := range lines {
		if validIMID(line.MentionOpenID, "ou_") && line.MentionOpenID != bot && line.MentionName != "" {
			wanted[line.MentionOpenID] = true
		}
	}
	members := make(map[string]bool)
	seen := make(map[string]bool)
	for cursor := ""; len(members) < len(wanted); {
		page, err := s.Members(ctx, chatID, cursor)
		for _, member := range page.Items {
			if wanted[member.OpenID] {
				members[member.OpenID] = true
			}
		}
		if err != nil || page.Next == "" || seen[page.Next] {
			break
		}
		seen[page.Next] = true
		cursor = page.Next
	}
	return members
}

func groupReplyParagraphs(lines []group.ReplyLine, members map[string]bool) [][]chat.DeliverySpan {
	paragraphs := make([][]chat.DeliverySpan, 0, len(lines))
	pinged := make(map[string]bool)
	for _, line := range lines {
		var paragraph []chat.DeliverySpan
		label := line.MentionName
		id := line.MentionOpenID
		if id != "" && !pinged[id] {
			if members[id] && len(pinged) < chat.MaxPostMentions && label != "" {
				paragraph = append(paragraph, chat.DeliverySpan{Mention: id})
				pinged[id] = true
				label = ""
			} else {
				label += "（未提醒）"
			}
		}
		if text := label + line.Text; text != "" || len(paragraph) == 0 {
			paragraph = append(paragraph, chat.DeliverySpan{Text: text})
		}
		paragraphs = append(paragraphs, paragraph)
	}
	return paragraphs
}
