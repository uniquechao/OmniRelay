package group

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

func TestOpenGroupTasksAudience(t *testing.T) {
	g := newGate(t)
	g.sql(`UPDATE accounts SET open_id='ou_'||id;
INSERT INTO feishu_bot_configs(tenant_key,app_id,credentials_generation,enabled) VALUES('tenant','app',1,true);
INSERT INTO feishu_chat_conversations(tenant_key,app_id,chat_id,kind) VALUES
 ('tenant','app','main','group'),('tenant','app','side','group');
INSERT INTO project_relay_settings(tenant_key,app_id,project_id,timezone,local_time,version) VALUES('tenant','app','north','UTC','18:00',1);
INSERT INTO project_chat_links(tenant_key,app_id,project_id,chat_id) VALUES('tenant','app','north','main');
INSERT INTO feishu_drive_group_memberships(tenant_key,app_id,chat_id,generation,members,verified_at)
 VALUES('tenant','app','main',1,'{ou_alice,ou_bob}',now()),('tenant','app','side',1,'{ou_alice,ou_bob}',now());
INSERT INTO project_task_boards(tenant_key,app_id,project_id) VALUES('tenant','app','north'),('tenant','app','south');
SELECT set_config('omnirelay.task_actor','alice',true);
INSERT INTO project_tasks(tenant_key,app_id,project_id,id,title,description,status,position,owner_member_id) VALUES
 ('tenant','app','north','one','修复电源','隐藏说明','in_progress',1,'a'),
 ('tenant','app','north','two','检查网线','隐藏说明','blocked',2,'b'),
 ('tenant','app','north','three','已结束','隐藏说明','done',3,'b'),
 ('tenant','app','north','four','已取消','隐藏说明','cancelled',4,'a'),
 ('tenant','app','north','five','已删除','隐藏说明','todo',5,'a'),
 ('tenant','app','south','foreign','南区秘密','隐藏说明','todo',6,NULL);
UPDATE project_tasks SET deleted_at=now() WHERE id='five';`)
	w := work{Scope: Who{ProjectID: "north"}, Actor: identity.Principal{TenantKey: "tenant", AppID: "app", AccountID: "alice", OpenID: "ou_alice"},
		Address: Envelope{Tenant: "tenant", App: "app", Project: "north", Channel: "feishu", Account: "alice", Mode: GroupReply}}
	read := func(chat string, notify bool) (GroupReplyBody, error) {
		w.Address.Chat = chat
		var result GroupReplyBody
		err := pgx.BeginFunc(g.ctx, g.DB, func(tx pgx.Tx) error {
			var err error
			result, err = g.openGroupTasks(g.ctx, tx, w, notify)
			return err
		})
		return result, err
	}
	check := func(chat string, notify bool, want int) GroupReplyBody {
		t.Helper()
		body, err := read(chat, notify)
		if err != nil || len(body.Lines) != want+1 {
			t.Fatalf("read %s, expected %d items: %+v %v", chat, want, body, err)
		}
		for _, forbidden := range []string{"隐藏说明", "南区秘密", "已结束", "已取消", "已删除"} {
			if strings.Contains(body.PlainText(), forbidden) {
				t.Fatalf("leaked %s", forbidden)
			}
		}
		return body
	}
	body := check("main", true, 2)
	if body.Lines[1].MentionOpenID != "ou_bob" || body.Lines[2].MentionOpenID != "ou_alice" || !strings.Contains(body.PlainText(), "检查网线") {
		t.Fatalf("wrong owners or rows: %+v", body)
	}
	if got := check("main", false, 2); got.Lines[1].MentionOpenID != "" || !strings.Contains(got.PlainText(), "小李") {
		t.Fatalf("unexpected mention: %+v", got)
	}
	if _, err := read("side", true); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("unbound chat: %v", err)
	}
	g.sql(`UPDATE feishu_drive_group_memberships SET verified_at=now()-interval '27 hours' WHERE chat_id='main'`)
	if body = check("main", true, 2); body.Lines[1].MentionOpenID != "" || strings.Contains(body.PlainText(), "小李") {
		t.Fatalf("stale identity: %+v", body)
	}
	g.sql(`UPDATE feishu_drive_group_memberships SET verified_at=now() WHERE chat_id='main';
UPDATE project_members SET role='revoked' WHERE account_id='bob'`)
	if body = check("main", true, 2); body.Lines[1].MentionOpenID != "" {
		t.Fatalf("revoked owner: %+v", body)
	}
	g.sql(`UPDATE project_members SET role='reader' WHERE account_id='bob';
INSERT INTO project_chat_links(tenant_key,app_id,project_id,chat_id) VALUES('tenant','app','north','side');
INSERT INTO project_chat_primary(tenant_key,app_id,project_id,chat_id,version) VALUES('tenant','app','north','main',1);
UPDATE project_relay_settings SET target_chat_id='side';
INSERT INTO project_sources(tenant_key,app_id,project_id,kind,id,chat_id,title,body) VALUES('tenant','app','north','message','m','side','','');
INSERT INTO project_task_sources VALUES('tenant','app','north','two','message','m');
INSERT INTO project_source_readers VALUES('tenant','app','north','message','m','alice');
INSERT INTO feishu_chat_messages(tenant_key,app_id,message_id,chat_id,sent_at,updated_at,body,search_text,mention_names,visible_in_chat)
 VALUES('tenant','app','m','side',now(),now(),'@_user_1 请检查网线，谢谢','@小李 请检查网线，谢谢','{"ou_bob":"小李","@_user_1":"小李"}',true)`)
	check("main", true, 2)
	if body = check("side", true, 1); body.Lines[1].MentionOpenID != "ou_bob" || strings.Contains(body.PlainText(), "修复电源") || strings.Contains(body.PlainText(), "受阻") || strings.Contains(body.PlainText(), "-T") {
		t.Fatalf("secondary scope: %+v", body)
	}
	g.sql(`UPDATE project_tasks SET owner_member_id='a' WHERE id='two'`)
	if body = check("side", true, 1); body.Lines[1].MentionOpenID != "" || strings.Contains(body.PlainText(), "小林") {
		t.Fatalf("private reassignment: %+v", body)
	}
	g.sql(`UPDATE project_tasks SET title='机密修改' WHERE id='two'`)
	check("side", true, 0)
	g.sql(`UPDATE project_tasks SET title='检查网线' WHERE id='two'; UPDATE feishu_chat_messages SET deleted=true,body='',search_text='' WHERE message_id='m'`)
	check("side", true, 0)
}

func TestOpenGroupTasksLongPost(t *testing.T) {
	var tasks []groupTaskRow
	for range 150 {
		tasks = append(tasks, groupTaskRow{board: true, total: 175, code: "PRJ-T001", title: strings.Repeat("<", 200), status: "todo"})
	}
	body, err := renderGroupTasks(tasks, true)
	if err != nil || len(body.Lines) < 3 || len(body.Lines) >= len(tasks) || !strings.Contains(body.Lines[len(body.Lines)-1].Text, "未展示") || !strings.Contains(body.Lines[0].Text, "175") {
		t.Fatalf("long post = %d lines (%v)", len(body.Lines), err)
	}
}
