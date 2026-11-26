package routing

import (
	"regexp"
	"strings"
)

// Only a positive imperative in the user's own request grants that action.
// Every model tool selection is checked against the matching clause again.
var commands = map[string]struct{ verbs, objects string }{
	"project_update":        {"更新|修改|设置|变更", "项目"},
	"member_add":            {"添加|新增|邀请", "成员|人员"},
	"member_remove":         {"移除|删除", "成员|人员"},
	"member_update":         {"维护|修改|更新|设置", "成员|职责"},
	"member_set_role":       {"修改|设置|授予|撤销", "角色|权限"},
	"chat_bind":             {"绑定", "群"},
	"chat_unbind":           {"解绑", "群"},
	"chat_set_primary":      {"设置|设为|设成|指定", "主群"},
	"meeting_mark":          {"标记", "纪要"},
	"task_create":           {"创建|新建|新增|建立|建一|建个|建任务|建成", "任务|待办"},
	"task_update":           {"修改|更新|改派|分派|设置", "任务|待办"},
	"task_delete":           {"删除|清理", "任务|待办"},
	"task_transition":       {"取消|完成|重开|标记|变更|改为", "任务|待办"},
	"task_add_progress":     {"追加|添加|更新", "进度"},
	"task_move":             {"移动|调整", "任务"},
	"task_bulk_update":      {"批量修改|批量更新", "任务"},
	"task_board_save":       {"修改|保存|更新", "看板"},
	"task_tag_create":       {"创建|添加|新增", "标签"},
	"task_tag_update":       {"修改|更新", "标签"},
	"task_tag_delete":       {"删除|移除", "标签"},
	"task_feedback_submit":  {"提交|添加", "反馈"},
	"task_feedback_review":  {"审核|处理", "反馈"},
	"task_candidate_submit": {"提交|生成", "候选|建议"},
	"task_candidate_review": {"确认|合并|忽略", "候选|建议"},
	"task_milestone_create": {"创建|新增", "里程碑"},
	"task_evidence_link":    {"关联|附上", "依据|来源|证据"},
	"task_board_timezone":   {"设置|修改", "时区"},
	"briefing_save":         {"保存|更新|撰写", "简报"},
	"delegate_project":      {"交办|委派|交给|安排|让|请", "项目|跟进"},
}
var separator = regexp.MustCompile(`[，,。；;\n]|并且|然后|并`)

var quotedObject = regexp.MustCompile(`「[^」]*」|“[^”]*”|"[^"]*"`)

var assigneePrefix = regexp.MustCompile(`^(?:交给|指派给|分派给|改派给|负责人(?:为|是)?[：:]?)[[:space:]]*`)

// TaskAssignment isolates the object expression from the single assignment
// belonging to it. Markers inside a quoted title are ordinary title text.
func TaskAssignment(clause string) (subject, assignee string) {
	mask := quotedObject.ReplaceAllStringFunc(clause, func(q string) string { return strings.Repeat(" ", len(q)) })
	if n := strings.Index(mask, "备注"); n >= 0 {
		mask = mask[:n]
		clause = clause[:n]
	}
	marker := taskOwnerMarker.FindStringIndex(mask)
	if marker == nil {
		return clause, ""
	}
	return strings.TrimSpace(clause[:marker[0]]), strings.TrimSpace(strings.TrimRight(clause[marker[1]:], "）)"))
}

var taskOwnerMarker = regexp.MustCompile(`交给|指派给|分派给|改派给|负责人(?:为|是)?[：:]?`)

func containsWord(text, alternatives string) bool {
	for _, word := range strings.Split(alternatives, "|") {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}

func AuthorizedClauses(text, tool string) []string {
	cmd, ok := commands[tool]
	if !ok {
		return nil
	}
	t := strings.TrimSpace(text)
	for _, prefix := range []string{"请问", "查询", "查找", "查一下", "查一查", "搜索", "看看", "列出", "复述", "说明", "解释", "我创建的", "已删除", "已创建", "总结"} {
		if strings.HasPrefix(t, prefix) {
			return nil
		}
	}
	mask := quotedObject.ReplaceAllStringFunc(t, func(phrase string) string { return strings.Repeat(" ", len(phrase)) })
	if containsWord(mask, "不要|不用|无需|别|不必|不执行|能否|如何|怎么|可以吗|是否|假设|如果|模拟|例如|举例|只查|仅查|？|?|「|」|“|”|`") {
		return nil
	}
	var matches []string
	begin := 0
	parts := append(separator.FindAllStringIndex(mask, -1), []int{len(mask), len(mask)})
	for i, bounds := range parts {
		start := begin
		clause := t[start:bounds[0]]
		part := strings.TrimSpace(mask[begin:bounds[0]])
		begin = bounds[1]
		if strings.HasPrefix(part, "@_user_") {
			_, part, _ = strings.Cut(part, " ")
			part = strings.TrimSpace(part)
		}
		for _, prefix := range []string{"麻烦", "请", "帮我", "帮忙", "给我"} {
			part = strings.TrimSpace(strings.TrimPrefix(part, prefix))
		}
		for _, verb := range strings.Split(cmd.verbs, "|") {
			idx := strings.Index(part, verb)
			if idx < 0 {
				continue
			}
			if idx > 0 && !(strings.HasPrefix(part, "把") || strings.HasPrefix(part, "将") || strings.HasPrefix(part, "给") || strings.HasPrefix(part, "在") || strings.HasPrefix(part, "向")) {
				continue
			}
			after := part[idx+len(verb):]
			if strings.HasPrefix(after, "的") || strings.HasPrefix(after, "过") || strings.HasPrefix(after, "了") {
				continue
			}
			if containsWord(part, cmd.objects) {
				// Only an immediately adjacent comma complement belongs to this
				// command. A semicolon, another command or a note starts a new scope.
				delim := t[bounds[0]:bounds[1]]
				if (tool == "task_create" || tool == "task_update") && (delim == "，" || delim == ",") && i+1 < len(parts) {
					next := strings.TrimSpace(mask[bounds[1]:parts[i+1][0]])
					if assigneePrefix.MatchString(next) {
						clause = t[start:parts[i+1][0]]
					}
				}
				matches = append(matches, clause)
				break
			}
		}
	}
	return matches
}
