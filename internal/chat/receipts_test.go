package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/uniquechao/OmniRelay/internal/identity"
	"github.com/uniquechao/OmniRelay/internal/orchestrator"
)

type deliveryLookup func(context.Context, ReceiptScope, []ReceiptMessage) ([]DeliveryEvidence, error)

func (f deliveryLookup) LookupDeliveries(ctx context.Context, scope ReceiptScope, messages []ReceiptMessage) ([]DeliveryEvidence, error) {
	return f(ctx, scope, messages)
}

type businessLookup func(context.Context, identity.Principal, []BusinessReceiptKey) ([]BusinessReceiptKey, error)

func (f businessLookup) LookupAuthorizedReceipts(ctx context.Context, p identity.Principal, keys []BusinessReceiptKey) ([]BusinessReceiptKey, error) {
	return f(ctx, p, keys)
}

func TestChatReceiptRejectsUnverifiedAssociations(t *testing.T) {
	scope := ReceiptScope{TenantKey: "tenant", AppID: "cli_app"}
	actor := identity.Principal{AccountID: "actor", TenantKey: scope.TenantKey, AppID: scope.AppID}
	key := BusinessReceiptKey{OperationID: "operation", Target: ReceiptTarget{
		ReceiptID: "receipt", ProjectID: "project", TargetKind: "task", TargetID: "task",
	}}
	a := DeliveryEvidence{Scope: scope, Message: ReceiptMessage{ChatID: "oc_a", MessageID: "om_a"}, DeliveryID: "delivery", State: DeliveryConfirmed, Receipt: key}
	wrongChat, otherMessage, wrongTenant, pending, conflicting := a, a, a, a, a
	wrongChat.Message.ChatID = "oc_unrelated"
	otherMessage.Message.MessageID = "om_b"
	wrongTenant.Scope.TenantKey = "other"
	pending.State = "unknown"
	conflicting.DeliveryID = "different_delivery"
	wrongTarget := key
	wrongTarget.Target.TargetID = "different_task"

	for _, tc := range []struct {
		name   string
		claims []DeliveryEvidence
		proofs []BusinessReceiptKey
		want   *ReceiptTarget
	}{
		{name: "exact replay", claims: []DeliveryEvidence{a, a}, proofs: []BusinessReceiptKey{key, key}, want: &key.Target},
		{name: "no evidence", proofs: []BusinessReceiptKey{key}},
		{name: "pending", claims: []DeliveryEvidence{pending}, proofs: []BusinessReceiptKey{key}},
		{name: "wrong tenant", claims: []DeliveryEvidence{wrongTenant}, proofs: []BusinessReceiptKey{key}},
		{name: "wrong chat", claims: []DeliveryEvidence{wrongChat}, proofs: []BusinessReceiptKey{key}},
		{name: "same message in two chats", claims: []DeliveryEvidence{a, wrongChat}, proofs: []BusinessReceiptKey{key}},
		{name: "same delivery across messages", claims: []DeliveryEvidence{a, otherMessage}, proofs: []BusinessReceiptKey{key}},
		{name: "two deliveries for a message", claims: []DeliveryEvidence{a, conflicting}, proofs: []BusinessReceiptKey{key}},
		{name: "unverified target", claims: []DeliveryEvidence{a}, proofs: []BusinessReceiptKey{wrongTarget}},
		{name: "revoked", claims: []DeliveryEvidence{a}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := []messageItem{
				{ID: "om_a", ChatID: "oc_a", State: "available"},
				{ID: "om_b", ChatID: "oc_a", State: "available"},
				{ID: "om_c", ChatID: "oc_a", State: "deleted"},
			}
			sources := ReceiptSources{
				Deliveries: deliveryLookup(func(_ context.Context, got ReceiptScope, ids []ReceiptMessage) ([]DeliveryEvidence, error) {
					if got != scope || len(ids) != 2 {
						t.Fatalf("wrong delivery scope or input: %#v %#v", got, ids)
					}
					return tc.claims, nil
				}),
				Business: businessLookup(func(_ context.Context, p identity.Principal, keys []BusinessReceiptKey) ([]BusinessReceiptKey, error) {
					if p != actor || len(keys) != 1 || keys[0] != key {
						t.Fatalf("wrong reader or business inputs: %#v %#v", p, keys)
					}
					return tc.proofs, nil
				}),
			}
			sources.project(t.Context(), actor, items)
			if got := items[0].BusinessReceipt; (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
				t.Fatalf("association got %+v want %+v", got, tc.want)
			}
			if items[1].BusinessReceipt != nil || items[2].BusinessReceipt != nil {
				t.Fatalf("unexpected second association: %+v", items)
			}
		})
	}
}

func TestChatReceiptAuthorizationOnHistorySearchAndContext(t *testing.T) {
	src := &fakeSource{members: map[string][]Member{"oc_group": {{OpenID: "ou_alice", Name: "林晓"}}}}
	s, ctx := testChat(t, src)
	for i, id := range []string{"om_task", "om_briefing", "om_waiting", "om_deleted"} {
		message := exampleMessage("oc_group", id, "项目更新", i+1)
		message.Deleted = id == "om_deleted"
		applyEvent(t, ctx, s, orchestrator.ArchiveEvent{ChatID: message.ChatID, Kind: orchestrator.ChatGroup, Message: &message})
	}
	task := BusinessReceiptKey{OperationID: "op_task", Target: ReceiptTarget{ReceiptID: "rcp_task", ProjectID: "project", TargetKind: "task", TargetID: "task_1"}}
	briefing := BusinessReceiptKey{OperationID: "op_briefing", Target: ReceiptTarget{ReceiptID: "rcp_briefing", ProjectID: "project", TargetKind: "briefing", TargetID: "brief_1"}}
	allowed := true
	businessAvailable := true
	looks := 0
	sources := ReceiptSources{
		Deliveries: deliveryLookup(func(_ context.Context, scope ReceiptScope, messages []ReceiptMessage) ([]DeliveryEvidence, error) {
			looks++
			var evidence []DeliveryEvidence
			for _, m := range messages {
				key, state := task, DeliveryConfirmed
				switch m.MessageID {
				case "om_task":
				case "om_briefing":
					key = briefing
				case "om_waiting":
					state = "unknown"
				default:
					t.Fatalf("sent inaccessible or deleted message to E1: %#v", m)
				}
				entry := DeliveryEvidence{Scope: scope, Message: m, DeliveryID: "delivery_" + m.MessageID, State: state, Receipt: key}
				evidence = append(evidence, entry, entry)
			}
			return evidence, nil
		}),
		Business: businessLookup(func(_ context.Context, actor identity.Principal, keys []BusinessReceiptKey) ([]BusinessReceiptKey, error) {
			if actor.TenantKey != "tenant_chat" || actor.AppID != "cli_chat" {
				t.Fatalf("unexpected authority subject: %#v", actor)
			}
			if !businessAvailable {
				return nil, errors.New("business authority unavailable")
			}
			if !allowed || actor.AccountID != "alice" {
				return nil, nil
			}
			return append([]BusinessReceiptKey(nil), keys...), nil
		}),
	}
	s.receipts = sources
	mux := http.NewServeMux()
	s.Register(mux, func(_ http.ResponseWriter, r *http.Request, _ identity.SessionPolicy) (identity.Principal, bool) {
		user := r.Header.Get("test-user")
		return identity.Principal{AccountID: user, OpenID: "ou_" + user, TenantKey: "tenant_chat", AppID: "cli_chat"}, true
	})
	read := func(user, path string, status int, want ...ReceiptTarget) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, route+path, nil)
		req.Header.Set("test-user", user)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("%s %s: got %d, want %d: %s", user, path, w.Code, status, w.Body)
		}
		var got struct {
			Items []messageItem `json:"items"`
		}
		// Check exact structured targets and that an unauthorized association
		// leaves no receipt field anywhere in the response.
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		var targets []ReceiptTarget
		for _, item := range got.Items {
			if item.BusinessReceipt != nil {
				targets = append(targets, *item.BusinessReceipt)
			}
		}
		if !slices.Equal(targets, want) || len(want) == 0 && strings.Contains(w.Body.String(), `"business_receipt"`) {
			t.Fatalf("%s %s: receipt projection %+v, want %+v", user, path, targets, want)
		}
	}
	read("alice", "/oc_group/messages", 200, briefing.Target, task.Target)
	read("alice", "/search?q=项目", 200, briefing.Target, task.Target)
	read("alice", "/oc_group/messages/om_task/context", 200, task.Target, briefing.Target)
	read("admin", "/oc_group/messages", 200)
	read("alice", "/oc_group/messages/om_deleted/context", 410)
	allowed = false
	read("alice", "/oc_group/messages", 200)
	allowed, businessAvailable = true, false
	read("alice", "/oc_group/messages", 200)
	businessAvailable = true
	s.receipts = ReceiptSources{Deliveries: sources.Deliveries}
	before := looks
	read("alice", "/oc_group/messages", 200)
	s.receipts = sources
	src.members["oc_group"] = nil
	read("alice", "/oc_group/messages", 404)
	if looks != before {
		t.Fatal("unconfigured receipt or revoked chat queried delivery evidence")
	}
}
