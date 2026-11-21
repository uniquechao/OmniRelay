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
