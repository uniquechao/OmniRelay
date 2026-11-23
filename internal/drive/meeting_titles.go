package drive

import (
	"regexp"
	"strings"
)

var englishMeetingTitle = regexp.MustCompile(`(?i)\b(meeting[ -]+(minutes|notes|summary)|minutes[ -]+of[ -]+(the[ -]+)?meeting)\b`)

// ProjectFileCategory uses the visible title only. Preview and download still use the original format.
func ProjectFileCategory(category, format, title string) string {
	switch format {
	case "docs", "word", "pdf", "slides", "excel", "base", "generic":
		for _, keyword := range []string{"纪要", "会议记录", "例会记录", "周会记录", "会议总结", "会议小结", "会议实录", "妙记"} {
			if strings.Contains(title, keyword) {
				return "minutes"
			}
		}
		if englishMeetingTitle.MatchString(strings.ReplaceAll(title, "_", " ")) {
			return "minutes"
		}
		for _, keyword := range []string{"邀请", "通知", "议程", "安排", "模板"} {
			if strings.Contains(title, keyword) {
				return category
			}
		}
		for _, keyword := range []string{"周会", "例会", "站会", "同步会", "复盘会"} {
			if strings.Contains(title, keyword) {
				return "minutes"
			}
		}
	}
	return category
}
