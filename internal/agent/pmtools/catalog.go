// Package pmtools defines the model-visible project tool contracts and validates callback arguments.
package pmtools

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

type Tool struct {
	Name, Description, Action string
	Write, PrivateSource      bool
	fields                    string
}

var catalog = []Tool{
	{"project_get", "读取当前项目的档案、状态和版本", "project.read", false, false, ""},
	{"project_update", "修改项目档案中指定的字段", "project.settings", true, false, "!expected_version name status project_type background planned_start planned_end"},
	{"member_list", "查询项目成员；分派任务时传 purpose=assignment 可检索关联群成员", "project.read", false, false, "q role page purpose"},
	{"member_add", "向本项目添加系统账号；role 优先用 member_list 的 project_roles，已有成员请用 member_update", "project.settings", true, false, "!account_id !role role_title responsibility project_role expected_project_role"},
	{"member_set_role", "按会籍版本变更项目成员角色；使用项目级角色时必须传 expected_project_role", "project.settings", true, false, "!account_id !generation !role expected_project_role"},
	{"member_update", "按版本修改项目角色名称、职责及访问角色", "project.settings", true, false, "!account_id !expected_version role role_title responsibility project_role expected_project_role"},
	{"member_candidates", "按姓名或账号搜索可添加的启用账号", "project.settings", false, false, "q page"},
	{"member_remove", "按会籍版本移除项目成员", "project.settings", true, false, "!account_id !generation"},
	{"chat_list", "列出本人可见的绑定群和主群版本", "project.read", false, true, "candidate_chat_id"},
	{"chat_search_messages", "检索本人可见的已绑定项目群。传关键词 q 或完整 date_from、date_to（YYYY-MM-DD，含首尾业务日），两者可组合。按项目时区过滤已索引消息，返回群、消息 ID、时间、发言人和命中片段。has_more 为真时继续翻页", "project.read", false, true, "!chat_id q date_from date_to business_timezone page"},
	{"chat_bind", "绑定已获来源授权且尚未归属其他项目的群", "project.settings", true, false, "!chat_id !expected_version"},
	{"chat_unbind", "按绑定版本解绑本项目群", "project.settings", true, false, "!chat_id !expected_version"},
	{"chat_set_primary", "按绑定版本和主群版本指定项目主群", "project.settings", true, false, "!chat_id !expected_version !primary_version"},
	{"file_search", "按关键词、category=minutes 或业务日期搜索可读项目资料。本地纪要按来源登记时间，飞书文件按分享时间。返回 file_ref 和元数据", "project.read", false, true, "q page category date_from date_to business_timezone"},
	{"file_get", "用 file_ref 读取可见文件引用、已同步的正文片段和修订，核对引用依据", "project.read", false, true, "!file_ref page date_from date_to business_timezone"},
	{"meeting_mark", "将 file_search 返回的飞书文档或文件标记为当前项目的会议纪要，重复标记不会创建副本", "project.assign", true, false, "!file_ref"},
	{"task_search", "按关键词、状态、负责人搜索当前项目的任务", "project.read", false, false, "q status owner_member_id page"},
	{"task_get", "取得项目任务及当前版本", "project.read", false, false, "!task_id"},
	{"task_create", "创建任务，默认由本人负责", "project.comment", true, false, "!title status column_id blocked_reason description owner_member_id priority start_at due_at milestone_id acceptance participant_ids tag_ids"},
	{"task_update", "修改任务指定字段，改派和优先级需要任务分派权", "project.comment", true, false, "!task_id !expected_version title description owner_member_id priority start_at due_at milestone_id acceptance participant_ids tag_ids"},
	{"task_delete", "按任务版本删除任务并保留审计，必须提供删除原因", "project.assign", true, false, "!task_id !expected_version !change_reason"},
	{"task_transition", "按版本变更状态，受阻需 blocked_reason，取消和重开需 change_reason", "project.comment", true, false, "!task_id !expected_version !expected_board_version !status column_id blocked_reason change_reason completion_note"},
	{"task_add_progress", "在可评论任务下追加进度，保留作者和时间", "project.comment", true, false, "!task_id !expected_version !body"},
	{"task_board_get", "读取任务看板、列、标签、数量和版本，可按列续页", "project.read", false, false, "q status sort owner_member_id owner_unassigned milestone_id milestone_unassigned priority priority_unset tag_ids tag_match due_from due_before overdue_only no_due filter_column_id cursor limit column_id"},
	{"task_query", "按编号、成员、状态、标签、日期等筛选并分页读取任务", "project.read", false, false, "q status sort owner_member_id owner_unassigned milestone_id milestone_unassigned priority priority_unset tag_ids tag_match due_from due_before overdue_only no_due filter_column_id cursor limit"},
	{"task_move", "在列间移动任务或在同列重排，按任务和看板版本执行", "project.comment", true, false, "!task_id !expected_task_version !expected_board_version !target_column_id before_id after_id blocked_reason completion_note change_reason"},
	{"task_bulk_update", "原子批量更新任务，每项含 id、expected_version 及要修改的字段", "project.assign", true, false, "!items"},
	{"task_board_save", "保存列配置和默认列，删除有任务的列时提供 migrations 映射", "project.settings", true, false, "!expected_version !columns !default_column_id migrations blocked_reason change_reason tags"},
	{"task_tag_create", "创建任务标签，使用看板版本", "project.settings", true, false, "!expected_version !name !color !type"},
	{"task_tag_update", "修改指定任务标签，使用看板版本", "project.settings", true, false, "!tag_id !expected_version !name !color !type"},
	{"task_tag_delete", "删除任务标签，使用看板版本", "project.settings", true, false, "!tag_id !expected_version"},
	{"task_activity_list", "读取评论与变更历史，kind 可选 all、change、comment", "project.read", false, false, "!task_id kind cursor limit"},
	{"task_evidence_list", "读取本人有权查看的任务依据，撤权后重新校验", "project.read", false, true, "!task_id"},
	{"task_feedback_list", "读取当前账号可见的任务进展建议", "project.read", false, true, "!task_id"},
	{"task_feedback_submit", "本人负责或参与的任务可提交进展建议", "project.comment", true, false, "!task_id !task_version !proposed_status !note"},
	{"task_feedback_review", "审核进展建议，action 为 accept 或 reject", "project.assign", true, false, "!suggestion_id !expected_version !action !reason"},
	{"task_candidate_sources", "读取当前账号有权查看的项目候选任务来源及修订值", "project.assign", false, true, "lookback_days"},
	{"task_candidate_chat_sources", "按时间范围读取本项目已授权群的候选来源", "project.assign", false, true, "!chat_id !from_at !to_at limit cursor"},
	{"task_candidate_submit", "基于已读取来源的修订提交候选任务，仍需人工确认", "project.assign", true, false, "!session_ref !source_revision !candidates skill_ref lookback_days chat_scope"},
	{"task_candidate_list", "列出本人可见的任务建议、版本与依据，默认待审核", "project.read", false, true, "status q sort owner_member_id owner_unassigned no_due due_from due_before tag_ids overdue_only cursor limit"},
	{"task_candidate_review", "确认、合并或忽略候选（confirm、merge、ignore）；确认前须获得用户对共享内容及无截止日期的明确同意", "project.assign", true, false, "!candidate_id !action !expected_version reason target_task_id target_version fields share_ack no_deadline_ack"},
	{"task_actions", "读取本人对任务可执行的编辑、删除、评论及审核动作", "project.read", false, false, "!task_id"},
	{"task_milestone_list", "查询本项目里程碑以设置任务关联", "project.read", false, false, ""},
	{"task_milestone_create", "新增里程碑，可关联本项目任务", "project.assign", true, false, "!title occurs_on task_id"},
	{"task_evidence_link", "将本人可读的项目来源关联到任务", "project.assign", true, false, "!task_id !source_kind !source_id"},
	{"task_source_tasks", "按可读来源列出关联任务，消息来源须提供 chat_id", "project.read", false, true, "!source_kind !source_id chat_id"},
	{"task_business_time", "查询看板业务时区及当前业务日期", "project.read", false, false, ""},
	{"task_board_timezone", "按看板版本设置业务时区", "project.settings", true, false, "!expected_board_version !business_timezone"},
	{"briefing_list", "发现已撰写并保存的项目共享简报，按业务日期倒序列出每天的当前版本，可按日期区间、关键词和页码筛选；使用 briefing_get 读正文。期间查询附全项目近期成稿目录，跨期目录只含日期、标题、版本。发布记录另用 daily_report_query", "project.read", false, false, "date_from date_to q page view"},
	{"briefing_get", "按 YYYY-MM-DD 读取项目共享简报正文和版本。version 为 0 表示当天尚未撰写；期间外归档目标只返回版本，用于核对保存条件", "project.read", false, false, "!business_date view"},
	{"briefing_save", "按 YYYY-MM-DD 保存项目全体成员可见的简报标题和正文。先用 briefing_get 读取版本，首次创建传 expected_version=0，版本冲突时重新读取", "project.settings", true, false, "!business_date !title !body !expected_version"},
	{"daily_report_query", "按日期范围查授权日报，source 可选 reports 或 published。published 只查已发布且本人仍能查看的简报", "project.read", false, true, "source date_from date_to q page"},
}
var ErrArguments = errors.New("invalid tool arguments")

type Args map[string]json.RawMessage

func (a Args) String(k string) string { var s string; _ = json.Unmarshal(a[k], &s); return s }
func (a Args) Int(k string) int64     { var n int64; _ = json.Unmarshal(a[k], &n); return n }
func numeric(k string) bool {
	switch k {
	case "expected_board_version", "expected_task_version", "expected_version", "primary_version", "task_version", "target_version", "lookback_days", "page", "limit":
		return true
	}
	return false
}
func Lookup(name string) (Tool, bool) {
	for _, t := range catalog {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}
func Names() []string {
	names := []string{}
	for _, t := range catalog {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return names
}
func (t Tool) FieldNames() []string {
	fields := strings.Fields(t.fields)
	for i, f := range fields {
		fields[i] = strings.TrimPrefix(f, "!")
	}
	return fields
}
func (t Tool) HasField(k string) bool {
	for _, f := range strings.Fields(t.fields) {
		if strings.TrimPrefix(f, "!") == k {
			return true
		}
	}
	return false
}
func (t Tool) RequiredFields() []string {
	var out []string
	for _, f := range strings.Fields(t.fields) {
		if strings.HasPrefix(f, "!") {
			out = append(out, f[1:])
		}
	}
	return out
}
func (t Tool) Validate(a Args) error {
	if a == nil {
		return ErrArguments
	}
	seen := 0
	for _, f := range strings.Fields(t.fields) {
		k := strings.TrimPrefix(f, "!")
		v, ok := a[k]
		if !ok {
			if k != f {
				return ErrArguments
			}
			continue
		}
		seen++
		if string(v) == "null" {
			return ErrArguments
		}
		if shape := taskArgument(k); shape != nil {
			if !shape.valid(v) {
				return ErrArguments
			}
		} else if k == "participant_ids" || k == "tag_ids" {
			var ids []string
			if json.Unmarshal(v, &ids) != nil || len(ids) > 50 {
				return ErrArguments
			}
		} else if numeric(k) {
			var n int64
			if json.Unmarshal(v, &n) != nil || n < 0 || k == "page" && (n < 1 || n > 100000) {
				return ErrArguments
			}
		} else {
			var s string
			if json.Unmarshal(v, &s) != nil || len(s) > 16000 || strings.ContainsRune(s, 0) || k != f && strings.TrimSpace(s) == "" {
				return ErrArguments
			}
		}
	}
	if seen != len(a) {
		return ErrArguments
	}
	return nil
}

var management = []Tool{
	{"visible_projects", "列出发起人当前可见项目及其受阻任务", "", false, true, ""},
	{"delegation_status", "查询本人委派的子工作及状态；可按父工作编号筛选", "", false, true, "parent_work"},
	{"delegate_project", "将用户明确要求跟进的原始请求交办给指定项目的 PM；返回子工作编号", "", true, false, "!project_id"},
}

func RoleTools(role string) []Tool {
	if role == "management-pm" {
		return management
	}
	tools := []Tool{}
	for _, t := range catalog {
		if role == "project-pm" || role == "query" && !t.Write {
			tools = append(tools, t)
		}
	}
	return tools
}
func RoleNames(role string) []string {
	names := []string{}
	for _, t := range RoleTools(role) {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return names
}
func Schemas() []map[string]any { return ToolSchemas(catalog) }
func ToolSchemas(tools []Tool) []map[string]any {
	out := []map[string]any{}
	for _, t := range tools {
		props := map[string]any{}
		req := []string{}
		for _, f := range strings.Fields(t.fields) {
			k := strings.TrimPrefix(f, "!")
			typ := "string"
			if numeric(k) {
				typ = "integer"
			}
			props[k] = map[string]any{"type": typ}
			if k == "participant_ids" || k == "tag_ids" {
				props[k] = map[string]any{"type": "array", "items": map[string]string{"type": "string"}, "maxItems": 50}
			}
			if shape := taskArgument(k); shape != nil {
				props[k] = shape
			}
			if k != f {
				req = append(req, k)
			}
		}
		out = append(out, map[string]any{"name": t.Name, "description": t.Description, "write": t.Write, "parameters": map[string]any{"type": "object", "properties": props, "required": req, "additionalProperties": false}})
	}
	return out
}
