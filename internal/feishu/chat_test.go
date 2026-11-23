package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	im "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/uniquechao/OmniRelay/internal/chat"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
)

type chatTransport func(*http.Request) (*http.Response, error)

func TestArchiveMentionNamesFromHistoryAndLiveEvent(t *testing.T) {
	var item im.Message
	if err := json.Unmarshal([]byte(`{"message_id":"om_test","chat_id":"oc_fixture","create_time":"1700000000000","msg_type":"text","body":{"content":"{\"text\":\"@_user_1\"}"},"mentions":[null,{"key":"@_user_1","id":"ou_one","name":"林一"}]}`), &item); err != nil {
		t.Fatal(err)
	}
	history, err := historyMessage(chat.HistoryQuery{ChatID: "oc_fixture"}, &item)
	if err != nil {
		t.Fatal(err)
	}
	var incoming im.P2MessageReceiveV1
	if err := json.Unmarshal([]byte(fixtureEvent), &incoming); err != nil {
		t.Fatal(err)
	}
	m := incoming.Event.Message
	m.Content = item.Body.Content
	if err := json.Unmarshal([]byte(`[null,{"key":"@_user_1","id":{"open_id":"ou_one"},"name":"林一"}]`), &m.Mentions); err != nil {
		t.Fatal(err)
	}
	live := mapArchive(fixtureConfig, &incoming, time.Now())
	if live == nil || live.Message == nil {
		t.Fatal("missing archived event")
	}
	want := map[string]string{"@_user_1": "林一", "ou_one": "林一"}
	for _, msg := range []orchestrator.ChatMessage{history, *live.Message} {
		if msg.Body != `{"text":"@_user_1"}` || !maps.Equal(msg.MentionNames, want) {
			t.Fatalf("archive lost raw body or mention names: %+v", msg)
		}
	}
}

func (f chatTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGroupMembersChangedEvent(t *testing.T) {
	const kind = "im.chat.member.user.withdrawn_v1"
	cfg := workerConfig{AppID: "cli_group", TenantKey: "tenant_group"}
	raw := `{"schema":"2.0","header":{"event_id":"member_left","event_type":"im.chat.member.user.withdrawn_v1",
		"app_id":"cli_group","tenant_key":"tenant_group"},"event":{"chat_id":"oc_group"}}`
	got := mapGroupMembersChanged(cfg, []byte(raw), kind)
	if got == nil || !got.GroupMembersChanged || got.ChatID != "oc_group" || got.EventID != "member_left" || got.Message != nil {
		t.Fatalf("mapped membership event: %#v", got)
	}
	for _, mutation := range [][2]string{{"tenant_group", "another_tenant"}, {"cli_group", "another_app"}, {"oc_group", "not_a_chat"}, {kind, "im.message.receive_v1"}} {
		if got := mapGroupMembersChanged(cfg, []byte(strings.Replace(raw, mutation[0], mutation[1], 1)), kind); got != nil {
			t.Fatalf("unexpected membership event for %v", mutation)
		}
	}
}

func TestChatSourceHistoryAndGroupScope(t *testing.T) {
	now := time.Unix(1700000010, 0)
	groupCalls := 0
	historyCalls := 0
	source := NewChatSource(NewBotCheck("cli_chat_test", "fixture_secret", chatTransport(func(r *http.Request) (*http.Response, error) {
		body := ""
		switch r.URL.String() {
		case tokenURL:
			body = `{"code":0,"access_token":"fixture_token"}`
		default:
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer fixture_token" {
				t.Fatalf("unexpected Feishu request %s %s", r.Method, r.URL.Path)
			}
			q := r.URL.Query()
			switch r.URL.Path {
			case "/open-apis/im/v1/chats":
				groupCalls++
				if groupCalls == 1 {
					body = `{"code":0,"data":{"has_more":true,"page_token":"group-page","items":[
						{"chat_id":"oc_group","tenant_key":"tenant_chat","name":"项目群"},
						{"chat_id":"oc_other_tenant","tenant_key":"outside","name":"外部群"}]}}`
				} else if q.Get("page_token") == "group-page" {
					body = `{"code":0,"data":{"has_more":false,"items":[]}}`
				}
			case "/open-apis/im/v1/messages":
				historyCalls++
				if q.Get("sort_type") != "ByCreateTimeAsc" || q.Get("page_size") != "50" {
					t.Fatalf("unexpected history query %v", q)
				}
				switch historyCalls {
				case 1:
					if q.Get("container_id_type") != "chat" || q.Get("container_id") != "oc_group" ||
						q.Get("end_time") != "1700000010" || q.Has("start_time") || q.Has("page_token") {
						t.Fatalf("unexpected first history query %v", q)
					}
					body = `{"code":0,"data":{"has_more":true,"page_token":"second","items":[{
						"message_id":"om_root","chat_id":"oc_group","thread_id":"omt_topic","msg_type":"text",
						"create_time":"1700000001000","body":{"content":"{\"text\":\"消息\"}"}}]}}`
				case 2:
					if q.Get("page_token") != "second" {
						t.Fatalf("missing cursor: %v", q)
					}
					body = `{"code":230002}`
				case 3:
					if q.Get("container_id_type") != "thread" || q.Get("container_id") != "omt_topic" ||
						q.Has("start_time") || q.Has("end_time") {
						t.Fatalf("unexpected topic query %v", q)
					}
					body = `{"code":230073}`
				}
			}
		}
		if body == "" {
			t.Fatalf("unexpected request %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})), "tenant_chat")

	groups, err := source.Groups(t.Context(), "")
	if err != nil || groups.Next != "group-page" || len(groups.Items) != 1 || groups.Items[0].ID != "oc_group" {
		t.Fatalf("bad group scope: %#v %v", groups, err)
	}
	if page, err := source.Groups(t.Context(), groups.Next); err != nil || page.Next != "" {
		t.Fatalf("group cursor: %#v %v", page, err)
	}
	q := chat.HistoryQuery{ChatID: "oc_group", End: &now, Limit: 50}
	page, err := source.History(t.Context(), q)
	if err != nil || page.Next != "second" || len(page.Items) != 1 ||
		page.Items[0].ThreadID != "omt_topic" || page.Items[0].Body != `{"text":"消息"}` {
		t.Fatalf("bad history response: %#v %v", page, err)
	}
	q.Cursor = page.Next
	if _, err := source.History(t.Context(), q); !errors.Is(err, chat.ErrAccessLost) {
		t.Fatalf("bot removal got %v", err)
	}
	q.Cursor, q.ThreadID = "", "omt_topic"
	if _, err := source.History(t.Context(), q); !errors.Is(err, chat.ErrPartial) {
		t.Fatalf("invisible topic got %v", err)
	}
}

func TestChatArchiveAndCommandAdmissionAreIndependent(t *testing.T) {
	var incoming im.P2MessageReceiveV1
	noMention := strings.Replace(fixtureEvent, `[{"id":{"open_id":"ou_bot"},"mentioned_type":"bot","tenant_key":"tenant_fixture"}]`, "[]", 1)
	if err := json.Unmarshal([]byte(noMention), &incoming); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, eligible := mapInbound(fixtureConfig, &incoming, now); eligible {
		t.Fatal("a non-mention became a command")
	}
	archive := mapArchive(fixtureConfig, &incoming, now)
	if archive == nil || archive.Message == nil || archive.Message.ID != "om_fixture" {
		t.Fatalf("non-mention archive: %#v", archive)
	}

	var archived, commands int
	c := &Connection{inbox: testInbox(func(_ context.Context, event orchestrator.InboundEvent) (orchestrator.AcceptResult, error) {
		commands++
		if event.Archive != nil || event.MessageID != "om_fixture" {
			t.Fatalf("unexpected command event %#v", event)
		}
		return orchestrator.AcceptResult{}, orchestrator.ErrInboxFull
	})}
	c.SetArchive(func(context.Context, orchestrator.InboundEvent) error {
		archived++
		return nil
	})
	if err := c.accept(t.Context(), archiveInbound(fixtureConfig, archive)); err != nil || commands != 0 || archived != 1 {
		t.Fatalf("archive-only delivery: %v, command calls %d, archive calls %d", err, commands, archived)
	}

	if err := json.Unmarshal([]byte(fixtureEvent), &incoming); err != nil {
		t.Fatal(err)
	}
	command, eligible := mapInbound(fixtureConfig, &incoming, now)
	if !eligible {
		t.Fatal("a mention failed command admission")
	}
	command.Archive = mapArchive(fixtureConfig, &incoming, now)
	if err := c.accept(t.Context(), command); !errors.Is(err, orchestrator.ErrInboxFull) || commands != 1 || archived != 2 {
		t.Fatalf("command plus archive: %v, command calls %d, archive calls %d", err, commands, archived)
	}

	var recalled im.P2MessageRecalledV1
	if err := json.Unmarshal([]byte(`{"schema":"2.0","header":{"event_id":"evt_recall","event_type":"im.message.recalled_v1",
		"app_id":"cli_fixture","tenant_key":"tenant_fixture"},"event":{"chat_id":"oc_fixture","message_id":"om_fixture","recall_time":"1700000001000"}}`), &recalled); err != nil {
		t.Fatal(err)
	}
	recall := mapRecall(fixtureConfig, &recalled, now)
	if recall == nil || !recall.Message.Deleted || recall.Message.ID != archive.Message.ID {
		t.Fatalf("recall mapping: %#v", recall)
	}
}
