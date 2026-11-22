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
	{"member_list", "按姓名或角色查当前项目的有效成员及会籍版本", "project.read", false, false, "q role page"},
	{"member_add", "向本项目添加指定账号，已有成员请用 member_set_role", "project.settings", true, false, "!account_id !role role_title responsibility"},
	{"member_set_role", "按会籍版本变更项目成员角色", "project.settings", true, false, "!account_id !generation !role"},
	{"member_update", "按版本修改项目角色名称、职责及访问角色", "project.settings", true, false, "!account_id !expected_version role role_title responsibility"},
	{"member_candidates", "按姓名或账号搜索可添加的启用账号", "project.settings", false, false, "!q page"},
	{"member_remove", "按会籍版本移除项目成员", "project.settings", true, false, "!account_id !generation"},
	{"chat_list", "列出本人可见的绑定群和主群版本", "project.read", false, true, "candidate_chat_id"},
	{"chat_search_messages", "在本项目已绑定且本人可见的群搜索消息", "project.read", false, true, "!chat_id !q"},
	{"chat_bind", "绑定已获来源授权且尚未归属其他项目的群", "project.settings", true, false, "!chat_id !expected_version"},
	{"chat_unbind", "按绑定版本解绑本项目群", "project.settings", true, false, "!chat_id !expected_version"},
	{"chat_set_primary", "按绑定版本和主群版本指定项目主群", "project.settings", true, false, "!chat_id !expected_version !primary_version"},
	{"file_search", "搜索本人有来源读权的项目文件，返回引用和元数据", "project.read", false, true, "q page"},
	{"file_get", "用搜索结果的 file_ref 查询单个文件元数据", "project.read", false, true, "!file_ref"},
	{"task_search", "按关键词、状态、负责人搜索当前项目的任务", "project.read", false, false, "q status owner_member_id page"},
	{"task_get", "取得项目任务及当前版本", "project.read", false, false, "!task_id"},
	{"task_create", "创建任务，默认由本人负责", "project.comment", true, false, "!title description owner_member_id priority start_at due_at milestone_id acceptance participant_ids tag_ids"},
	{"task_update", "修改任务指定字段，改派和优先级需要任务分派权", "project.comment", true, false, "!task_id !expected_version title description owner_member_id priority start_at due_at milestone_id acceptance participant_ids tag_ids"},
	{"task_delete", "按任务版本删除任务并保留审计，必须提供删除原因", "project.assign", true, false, "!task_id !expected_version !change_reason"},
	{"task_transition", "按版本变更状态，受阻需 blocked_reason，取消和重开需 change_reason", "project.comment", true, false, "!task_id !expected_version !expected_board_version !status blocked_reason change_reason completion_note"},
	{"task_add_progress", "在可评论任务下追加进度，保留作者和时间", "project.comment", true, false, "!task_id !expected_version !body"},
	{"daily_report_query", "按日期范围查授权日报，source 可选 reports 或 published。published 只查已发布且本人仍能查看的简报", "project.read", false, true, "source date_from date_to q page"},
}
var ErrArguments = errors.New("invalid tool arguments")

type Args map[string]json.RawMessage

func (a Args) String(k string) string { var s string; _ = json.Unmarshal(a[k], &s); return s }
func (a Args) Int(k string) int64     { var n int64; _ = json.Unmarshal(a[k], &n); return n }
func numeric(k string) bool {
	return k == "expected_board_version" || k == "expected_version" || k == "primary_version" || k == "page"
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
		if k == "participant_ids" || k == "tag_ids" {
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
func Schemas() []map[string]any {
	out := []map[string]any{}
	for _, t := range catalog {
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
			if k != f {
				req = append(req, k)
			}
		}
		out = append(out, map[string]any{"name": t.Name, "description": t.Description, "write": t.Write, "parameters": map[string]any{"type": "object", "properties": props, "required": req, "additionalProperties": false}})
	}
	return out
}
