package chat

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

func TestAgentThreadReadsOnlyCurrentPublicTopic(t *testing.T) {
	source := &fakeSource{members: map[string][]Member{"group": {{OpenID: "ou_alice"}}}}
	s, ctx := testChat(t, source)
	_, err := s.pool.Exec(ctx, `INSERT INTO feishu_chat_conversations(tenant_key,app_id,chat_id,kind)
	VALUES('tenant_chat','cli_chat','group','group');
	INSERT INTO project_chat_links(tenant_key,app_id,project_id,chat_id) VALUES('tenant_chat','cli_chat','project','group');
	INSERT INTO feishu_chat_messages(tenant_key,app_id,chat_id,thread_id,message_id,sender_kind,search_text,sent_at,updated_at)
	SELECT 'tenant_chat','cli_chat','group',topic,id,kind,id,now(),now() FROM (VALUES
	('topic','correction','user'),('topic','unconfirmed-bot','app'),('another','other-topic','user')) v(topic,id,kind)`)
	if err != nil {
		t.Fatal(err)
	}
	actor := identity.Principal{TenantKey: s.tenant, AppID: s.app, AccountID: "alice", OpenID: "ou_alice"}
	read := func() (string, error) {
		var data []byte
		err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			var err error
			data, err = s.AgentThread(ctx, tx, actor, "project", "group", "topic", "question", time.Now())
			return err
		})
		return string(data), err
	}
	data, err := read()
	if err != nil || !strings.Contains(data, "correction") || strings.Contains(data, "unconfirmed-bot") || strings.Contains(data, "other-topic") {
		t.Fatalf("topic: %s %v", data, err)
	}
	_, err = s.pool.Exec(ctx, "UPDATE feishu_chat_messages SET deleted=true,body='',search_text='' WHERE message_id='correction'")
	if err != nil {
		t.Fatal(err)
	}
	if data, err := read(); err != nil || data != "[]" {
		t.Fatalf("recall: %s %v", data, err)
	}
	source.members["group"] = nil
	if _, err := read(); !errors.Is(err, ErrAccessLost) {
		t.Fatalf("revoked: %v", err)
	}
}
