package group

import (
	"context"
	"strings"

	"github.com/uniquechao/OmniRelay/internal/routing"
)

// These are deterministic planner responses for existing gateway integration
// fixtures. The image boundary tests replace this function with the mTLS client.
func fixturePlan(_ context.Context, r routing.PlanRequest) (routing.Plan, error) {
	intent := r.Intent
	if intent == "" {
		intent = "project"
	}
	p := routing.Plan{Intent: intent, Mode: "read"}
	if r.Group {
		p.GroupReply = &routing.GroupResponse{Kind: "answer"}
	}
	add := func(tool string, fields map[string]string, gen ...string) {
		p.Mode = "execute"
		p.Actions = append(p.Actions, routing.PlannedAction{Tool: tool, Evidence: r.Text, Fields: fields, Generate: gen})
	}
	type fields = map[string]string
	task := func(title, owner string) {
		f := fields{"title": title}
		if owner != "" {
			f["owner_member_id"] = owner
		}
		add("task_create", f)
	}
	switch r.Text {
	case "创建任务 校验交付":
		task("校验交付", "")
	case "创建任务 接口联调", "创建任务 接口联调，备注提到小王", "创建任务 接口联调；交给小王":
		task("接口联调", "")
	case "把接口联调建成任务，交给小李", "创建任务 接口联调，交给小李，备注小王负责协助", "创建任务 接口联调，交给小李":
		task("接口联调", "小李")
	case "创建任务 接口联调，交给成员 b；创建任务 文档发布，交给成员 w":
		task("接口联调", "b")
		task("文档发布", "w")
	case "创建任务 接口联调，交给小李；创建任务 文档发布，交给小王", "创建任务 接口联调，交给小李，创建任务 文档发布，交给小王":
		task("接口联调", "小李")
		task("文档发布", "小王")
	case "委派给北辰项目，创建任务 联调":
		if intent == "admin" {
			add("delegate_project", fields{"project_id": "北辰项目"})
		} else {
			task("联调", "")
		}
	case "创建任务 第一项，创建任务 第二项":
		task("第一项", "")
		task("第二项", "")
	case "创建我的任务 recover 并追加任务 recover 进度":
		task("recover", "")
		add("task_add_progress", fields{"task_id": "recover"}, "body")
	case "设置项目主群 oc_a", "设置项目主群 oc_b":
		add("chat_set_primary", fields{"chat_id": strings.TrimPrefix(r.Text, "设置项目主群 ")})
	case "把 bob 的角色名称改为验收负责人，职责为质量验收；创建重复任务样本，截止 2026-12-01，验收条件为接口验收；批量把重复任务样本的优先级改为 P1；取消后删除这项任务；提交候选并忽略本次唯一的候选":
		add("member_update", fields{"account_id": "bob", "role_title": "验收负责人", "responsibility": "质量验收"})
		add("task_create", fields{"title": "重复任务样本", "due_at": "2026-12-01", "acceptance": "接口验收"})
		add("task_bulk_update", fields{"items": `[{"id":"重复任务样本","priority":"P1"}]`})
		add("task_transition", fields{"task_id": "重复任务样本", "status": "cancelled"}, "change_reason")
		add("task_delete", fields{"task_id": "重复任务样本"}, "change_reason")
		add("task_candidate_submit", nil, "session_ref", "candidates")
		add("task_candidate_review", fields{"candidate_id": "@submitted", "action": "ignore"}, "reason")
	}
	return p, nil
}
