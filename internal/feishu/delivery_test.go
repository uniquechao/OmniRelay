package feishu

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
)

func TestDeliveryFeishuCreateAndConfirm(t *testing.T) {
	const (
		sender   = `"sender":{"id":"cli_delivery","id_type":"app_id","sender_type":"app","tenant_key":"tenant_delivery"}`
		target   = "oc_group"
		bot      = "ou_bot"
		uuid     = "op_part"
		body     = `{"text":"第一行\n第二行 😀"}`
		snapshot = `"create_time":"1795242600000","body":{"content":"{\"text\":\"实际归档内容\"}"}`
	)
	for _, tc := range []struct {
		name, reply string
		status      int
		want        chat.DeliveryResult
	}{
		{"confirmed", `{"code":0,"data":{"message_id":"om_sent","chat_id":"oc_group","msg_type":"text",` + sender + "," + snapshot + `}}`, 200,
			chat.DeliveryResult{State: "success", MessageID: "om_sent"}},
		{"confirmed with readback", `{"code":0,"data":{"message_id":"om_sent","chat_id":"oc_group","msg_type":"text",` + sender + `}}`, 200,
			chat.DeliveryResult{State: "success", MessageID: "om_sent"}},
		{"missing evidence", `{"code":0,"data":{"message_id":"om_sent"}}`, 200,
			chat.DeliveryResult{State: "unknown", Reason: "receipt_unknown", MessageID: "om_sent"}},
		{"wrong target", `{"code":0,"data":{"message_id":"om_sent","chat_id":"oc_other","msg_type":"text",` + sender + `}}`, 200,
			chat.DeliveryResult{State: "failed", Reason: "wrong_target_receipt"}},
		{"access lost", `{"code":230002}`, 400,
			chat.DeliveryResult{State: "failed", Reason: "access_lost"}},
		{"still sending", `{"code":230049}`, 400,
			chat.DeliveryResult{State: "unknown", Reason: "receipt_unknown"}},
		{"server failure", `{"code":230001}`, 502,
			chat.DeliveryResult{State: "unknown", Reason: "receipt_unknown"}},
		{"response dropped", "", 0,
			chat.DeliveryResult{State: "unknown", Reason: "receipt_unknown"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deliveries, verifies := 0, 0
			source := NewChatSource(NewBotCheck("cli_delivery", "test_secret", chatTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() == tokenURL {
					return deliveryResponse(200, `{"code":0,"access_token":"bot_token"}`), nil
				}
				if r.Header.Get("Authorization") != "Bearer bot_token" {
					t.Fatal("missing Bot token")
				}
				switch r.URL.Path {
				case "/open-apis/im/v1/messages":
					deliveries++
					var got map[string]string
					if r.Method != http.MethodPost || r.URL.Query().Get("receive_id_type") != "chat_id" ||
						json.NewDecoder(r.Body).Decode(&got) != nil || got["receive_id"] != target ||
						got["content"] != body || got["uuid"] != uuid || got["msg_type"] != "text" {
						t.Fatalf("unexpected create request: %v %v", r.URL, got)
					}
					if tc.status == 0 {
						return nil, io.ErrUnexpectedEOF
					}
					return deliveryResponse(tc.status, tc.reply), nil
				case "/open-apis/im/v1/messages/om_sent":
					verifies++
					if r.Method != http.MethodGet {
						t.Fatalf("reconciliation issued %s", r.Method)
					}
					return deliveryResponse(200, `{"code":0,"data":{"items":[{"message_id":"om_sent","chat_id":"oc_group","msg_type":"text",`+sender+","+snapshot+`}]}}`), nil
				default:
					t.Fatalf("unexpected request: %s", r.URL)
					return nil, errors.New("unexpected request")
				}
			})), "tenant_delivery")
			got := source.Deliver(t.Context(), target, bot, "text", body, uuid)
			message := got.Message
			got.Message = orchestrator.ChatMessage{}
			if got != tc.want {
				t.Fatalf("create response: %#v, want %#v", got, tc.want)
			}
			wantVerifies := 3
			if tc.name == "confirmed with readback" {
				wantVerifies++
			}
			if tc.want.State == "success" && (message.ID != got.MessageID || message.ChatID != target || message.SentAt.IsZero() ||
				message.Body != `{"text":"实际归档内容"}` || !message.VisibleInChat) {
				t.Fatalf("missing confirmed message snapshot: %#v", message)
			}
			readback, confirmed := source.ConfirmDelivery(t.Context(), target, "text", bot, "om_sent")
			_, wrongChat := source.ConfirmDelivery(t.Context(), "oc_other", "text", bot, "om_sent")
			_, wrongKind := source.ConfirmDelivery(t.Context(), target, "file", bot, "om_sent")
			if !confirmed || readback.ID != "om_sent" || wrongChat || wrongKind || deliveries != 1 || verifies != wantVerifies {
				t.Fatalf("incorrect confirmation, %d create requests and %d verification reads", deliveries, verifies)
			}
		})
	}
}

func TestGroupAgentReplyContract(t *testing.T) {
	calls := 0
	source := NewChatSource(NewBotCheck("cli_delivery", "fixture", chatTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == tokenURL {
			return deliveryResponse(200, `{"code":0,"access_token":"fixture-token"}`), nil
		}
		calls++
		var body struct {
			Thread  bool `json:"reply_in_thread"`
			UUID    string
			Content string
			Kind    string `json:"msg_type"`
		}
		if r.Method != "POST" || r.URL.Path != "/open-apis/im/v1/messages/om_original/reply" || json.NewDecoder(r.Body).Decode(&body) != nil ||
			!body.Thread || body.Kind != "text" || body.UUID != "stable-id" || body.Content != `{"text":"项目 PM · 北辰项目"}` {
			t.Errorf("incorrect thread reply")
		}
		return deliveryResponse(200, `{"code":0,"data":{"message_id":"om_reply","chat_id":"oc_north","msg_type":"text","create_time":"1795242600000","body":{"content":"{}"},"sender":{"id":"cli_delivery","id_type":"app_id","sender_type":"app","tenant_key":"tenant"}}}`), nil
	})), "tenant")
	result := source.ReplyTo(t.Context(), "oc_north", "ou_bot", "om_original", "项目 PM · 北辰项目", "stable-id")
	if calls != 1 || result.State != "success" || result.MessageID != "om_reply" {
		t.Fatal(result, calls)
	}
}

func TestDeliveryFeishuUploadAndMembership(t *testing.T) {
	source := NewChatSource(NewBotCheck("cli_delivery", "test_secret", chatTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case tokenURL:
			return deliveryResponse(200, `{"code":0,"access_token":"bot_token"}`), nil
		}
		switch r.URL.Path {
		case "/open-apis/im/v1/chats/oc_group/members/is_in_chat":
			return deliveryResponse(200, `{"code":0,"data":{"is_in_chat":false}}`), nil
		case "/open-apis/im/v1/files", "/open-apis/im/v1/images":
			if r.Method != http.MethodPost || r.ContentLength <= 0 || r.Header.Get("Authorization") != "Bearer bot_token" ||
				r.ParseMultipartForm(2048) != nil {
				t.Fatalf("invalid upload: %#v", r)
			}
			kind, key := "file", "file_attachment"
			if strings.HasSuffix(r.URL.Path, "images") {
				kind, key = "image", "img_picture"
				if r.FormValue("image_type") != "message" {
					t.Fatal("invalid image type")
				}
			} else if r.FormValue("file_type") != "stream" || r.FormValue("file_name") != "附件.txt" {
				t.Fatal("invalid file metadata")
			}
			file, header, err := r.FormFile(kind)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			data, err := io.ReadAll(file)
			if err != nil || string(data) != "测试内容" || header.Filename != "附件.txt" {
				t.Fatalf("invalid upload data: %q %q %v", data, header.Filename, err)
			}
			return deliveryResponse(200, `{"code":0,"data":{"`+kind+`_key":"`+key+`"}}`), nil
		default:
			t.Fatalf("unexpected request: %s", r.URL)
			return nil, errors.New("unexpected request")
		}
	})), "tenant_delivery")
	if err := source.BotInGroup(t.Context(), "oc_group"); !errors.Is(err, chat.ErrAccessLost) {
		t.Fatalf("Bot removal: %v", err)
	}
	for kind, want := range map[string]string{"file": "file_attachment", "image": "img_picture"} {
		got, err := source.Upload(t.Context(), kind, "附件.txt", []byte("测试内容"))
		if got != want || err != nil {
			t.Fatalf("%s upload: %q %v", kind, got, err)
		}
	}
}

func deliveryResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
