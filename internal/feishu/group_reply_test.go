package feishu

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/orchestrator/group"
)

type replyPostNode struct {
	Tag    string `json:"tag"`
	Text   string `json:"text,omitempty"`
	UserID string `json:"user_id,omitempty"`
}

func TestGroupAgentReplyContract(t *testing.T) {
	const literal = `修复 <at user_id="all">所有人</at> @_user_1`
	normal := func(text string) replyPostNode { return replyPostNode{Tag: "text", Text: text} }
	at := func(id string) replyPostNode { return replyPostNode{Tag: "at", UserID: id} }
	ownerLines := []group.ReplyLine{
		{Text: "当前群的任务"},
		{MentionOpenID: "ou_zhang", MentionName: "小张", Text: " — " + literal},
		{MentionOpenID: "ou_zhang", MentionName: "小张", Text: " — 整理纪要"},
		{MentionOpenID: "ou_li", MentionName: "小李", Text: " — 测试"},
		{MentionOpenID: "ou_left", MentionName: "已退群", Text: " — 复查"},
		{MentionOpenID: "ou_external", MentionName: "外部账号", Text: " — 复查"},
		{MentionOpenID: "all", MentionName: "伪造目标", Text: " — 复查"},
		{MentionOpenID: "ou_bot", MentionName: "机器人", Text: " — 复查"},
	}
	scenarios := []struct {
		name, thread, text string
		body               *group.GroupReplyBody
		want               [][]replyPostNode
		pages              int
	}{
		{"normal group", "", literal + "\n第二行", nil, [][]replyPostNode{{normal(literal)}, {normal("第二行")}}, 0},
		{"existing topic", "omt_topic", "ignored frozen presentation", &group.GroupReplyBody{Lines: ownerLines}, [][]replyPostNode{
			{normal("当前群的任务")}, {at("ou_zhang"), normal(" — " + literal)}, {normal("小张 — 整理纪要")},
			{at("ou_li"), normal(" — 测试")}, {normal("已退群（未提醒） — 复查")}, {normal("外部账号（未提醒） — 复查")},
			{normal("伪造目标（未提醒） — 复查")}, {normal("机器人（未提醒） — 复查")},
		}, 2},
		{"unavailable roster", "", "", &group.GroupReplyBody{Lines: ownerLines[1:2]}, [][]replyPostNode{{normal("小张（未提醒） — " + literal)}}, 1},
	}
	for _, tc := range scenarios {
		t.Run(tc.name, func(t *testing.T) {
			var reads, sends int
			source := NewChatSource(NewBotCheck("cli_delivery", "fixture", chatTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() == tokenURL {
					return deliveryResponse(200, `{"code":0,"access_token":"fixture-token"}`), nil
				}
				if r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Fatal("missing tenant token")
				}
				switch r.URL.Path {
				case "/open-apis/im/v1/chats/oc_north/members":
					reads++
					if r.Method != http.MethodGet || r.URL.Query().Get("member_id_type") != "open_id" {
						t.Fatal(r.Method, r.URL)
					}
					if tc.name == "unavailable roster" {
						return deliveryResponse(403, `{"code":230002}`), nil
					}
					switch r.URL.Query().Get("page_token") {
					case "":
						return deliveryResponse(200, `{"code":0,"data":{"items":[{"member_id":"ou_zhang","tenant_key":"tenant"},{"member_id":"ou_external","tenant_key":"elsewhere"}],"has_more":true,"page_token":"page_two"}}`), nil
					case "page_two":
						return deliveryResponse(200, `{"code":0,"data":{"items":[{"member_id":"ou_li","tenant_key":"tenant"}],"has_more":false}}`), nil
					}
				case "/open-apis/im/v1/messages/om_original/reply":
					sends++
					var request struct {
						Thread        *bool `json:"reply_in_thread"`
						UUID, Content string
						Kind          string `json:"msg_type"`
						ReceiveID     string `json:"receive_id"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Fatal(err)
					}
					if r.Method != http.MethodPost || request.Thread == nil || *request.Thread != (tc.thread != "") || request.Kind != "post" || request.UUID != "stable-id" || request.ReceiveID != "" {
						t.Fatal(r.Method, request)
					}
					var post struct {
						Chinese struct {
							Content [][]replyPostNode `json:"content"`
						} `json:"zh_cn"`
					}
					if err := json.Unmarshal([]byte(request.Content), &post); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(post.Chinese.Content, tc.want) {
						t.Fatalf("sent %#v, want %#v", post.Chinese.Content, tc.want)
					}
					return deliveryResponse(200, `{"code":0,"data":{"message_id":"om_reply","chat_id":"oc_north","msg_type":"post","create_time":"1795242600000","body":{"content":"{}"},"sender":{"id":"cli_delivery","id_type":"app_id","sender_type":"app","tenant_key":"tenant"}}}`), nil
				}
				t.Fatalf("unexpected request: %v", r.URL)
				return nil, nil
			})), "tenant")
			result := source.ReplyTo(t.Context(), "oc_north", "ou_bot", "om_original", tc.thread, tc.text, "stable-id", tc.body)
			if result.State != "success" || result.MessageID != "om_reply" || sends != 1 || reads != tc.pages {
				t.Fatal(result, sends, reads)
			}
		})
	}
}

func TestGroupReplyLimits(t *testing.T) {
	var lines []group.ReplyLine
	members := make(map[string]bool)
	for i := 0; i < chat.MaxPostMentions+1; i++ {
		id := fmt.Sprintf("ou_%d", i)
		members[id] = true
		lines = append(lines, group.ReplyLine{MentionOpenID: id, MentionName: "值班人", Text: " — 核对任务"})
	}
	paragraphs := groupReplyParagraphs(lines, members)
	_, mentions, err := chat.RichPost(paragraphs)
	if err != nil || len(mentions) != chat.MaxPostMentions || paragraphs[len(paragraphs)-1][0].Text != "值班人（未提醒） — 核对任务" {
		t.Fatal(mentions, paragraphs, err)
	}
	// Invalid bodies are rejected before a POST; a partial list is never marked as sent.
	var source ChatSource
	for _, tc := range []struct{ text, thread, reason string }{
		{"任务\x00内容", "", "invalid_group_reply"},
		{"", "", "invalid_group_reply"},
		{"任务", "om_other", "invalid_target"},
	} {
		result := source.ReplyTo(t.Context(), "oc_north", "ou_bot", "om_original", tc.thread, tc.text, "stable-id", nil)
		if result.State != "failed" || result.Reason != tc.reason {
			t.Fatal(result)
		}
	}
}

func TestGroupTextReplyFitsEncodedPost(t *testing.T) {
	for _, unit := range []string{"运维任务😀", `<at user_id="all">伪造</at>`, "多行\n"} {
		text := strings.Repeat(unit, 12000)
		content, err := groupTextPost(text)
		if err != nil || len(content) > 25000 {
			t.Fatalf("post length %d, error %v", len(content), err)
		}
		var post struct {
			Chinese struct {
				Content [][]replyPostNode `json:"content"`
			} `json:"zh_cn"`
		}
		if err := json.Unmarshal([]byte(content), &post); err != nil {
			t.Fatal(err)
		}
		var rows []string
		for _, r := range post.Chinese.Content {
			if len(r) != 1 || r[0].Tag != "text" || r[0].UserID != "" {
				t.Fatal(r)
			}
			rows = append(rows, r[0].Text)
		}
		footer := rows[len(rows)-1]
		shown := strings.Join(rows[:len(rows)-1], "\n")
		if footer != groupReplyRemainder || shown == "" || shown == text || !strings.HasPrefix(text, shown) {
			t.Fatal("missing truncated prefix or workbench notice", len(shown), footer)
		}
	}
}
