package routing

import "strings"

// Match only the identity query implemented by the project PM. Other intents
// keep their existing inbox behavior. Mention eligibility is checked by the SDK adapter.
func GroupIdentityQuery(text string) bool {
	words := strings.Fields(text)
	for len(words) > 0 && strings.HasPrefix(words[0], "@_user_") {
		words = words[1:]
	}
	switch strings.ToLower(strings.Trim(strings.Join(words, " "), " ？?!！。")) {
	case "我是谁", "我在哪个项目", "whoami", "context.whoami", "context.whoami()":
		return true
	}
	return false
}

// RequestIntent supplies the default when a trusted entry point has no explicit
// role selection. Authorization of that role is always done separately.
func RequestIntent(text string, private bool) string {
	if GroupIdentityQuery(text) && private {
		return "knowledge"
	}
	for _, s := range []string{"跨项目", "哪些项目", "各项目", "所有项目", "全局管理", "委派给项目"} {
		if strings.Contains(text, s) {
			return "admin"
		}
	}
	if RequestsWrite(text) {
		return "project"
	}
	for _, s := range []string{"为什么", "原因", "纪要", "依据", "知识问答", "出处", "查文档", "查资料", "引用"} {
		if strings.Contains(text, s) {
			return "knowledge"
		}
	}
	if private {
		return "knowledge"
	}
	return "project"
}

func RequestsWrite(text string) bool {
	for tool := range commands {
		if len(AuthorizedClauses(text, tool)) != 0 {
			return true
		}
	}
	return false
}
