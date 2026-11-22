package feishu

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestAgentAudienceRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, target, path, field string
	}{
		{"private-reply", "oc_dm", "/open-apis/im/v1/messages/om_question/reply", ""},
		{"group-notice", "oc_group", "/open-apis/im/v1/messages", "chat_id"},
		{"person-notice", "ou_member", "/open-apis/im/v1/messages", "open_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			source := NewChatSource(NewBotCheck("cli_delivery", "fixture", chatTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() == tokenURL {
					return deliveryResponse(200, `{"code":0,"access_token":"fixture-token"}`), nil
				}
				if r.Method != "POST" || r.URL.Path != tc.path || r.URL.Query().Get("receive_id_type") != tc.field {
					t.Fatalf("wrong route %s", r.URL)
				}
				var body struct {
					ID      string `json:"receive_id"`
					Thread  *bool  `json:"reply_in_thread"`
					UUID    string
					Content string
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil || body.UUID != "stable" || body.Content != `{"text":"fixture"}` {
					t.Fatal("malformed text")
				}
				if tc.field == "" {
					if body.ID != "" || body.Thread == nil || *body.Thread {
						t.Fatal("wrong DM reply")
					}
				} else if body.ID != tc.target || body.Thread != nil {
					t.Fatal("wrong directed recipient")
				}
				chatID := "oc_dm"
				if tc.field == "chat_id" {
					chatID = tc.target
				}
				calls++
				return deliveryResponse(200, `{"code":0,"data":{"message_id":"om_result","chat_id":"`+chatID+`","msg_type":"text","create_time":"1795242600000","body":{"content":"{}"},"sender":{"id":"cli_delivery","id_type":"app_id","sender_type":"app","tenant_key":"tenant"}}}`), nil
			})), "tenant")
			if tc.field == "" {
				r := source.ReplyPrivate(t.Context(), tc.target, "ou_bot", "om_question", "fixture", "stable")
				if r.State != "success" {
					t.Fatal(r)
				}
			} else {
				r := source.Notify(t.Context(), tc.field == "open_id", tc.target, "ou_bot", "fixture", "stable")
				if r.State != "success" {
					t.Fatal(r)
				}
				r = source.Notify(t.Context(), tc.field != "open_id", tc.target, "ou_bot", "fixture", "stable")
				if r.State != "failed" {
					t.Fatal("target type confusion")
				}
			}
			if calls != 1 {
				t.Fatal("unexpected platform calls", calls)
			}
		})
	}
}
