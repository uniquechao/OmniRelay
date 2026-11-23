package drive

import "testing"

func TestMeetingTitleClassification(t *testing.T) {
	for _, tc := range []struct{ category, format, title, want string }{
		{"minutes", "minutes", "客户联调", "minutes"},
		{"docs", "docs", "研发周会纪要", "minutes"},
		{"files", "word", "Q4 复盘会议记录.docx", "minutes"},
		{"files", "slides", "11月23日研发例会.pptx", "minutes"},
		{"files", "pdf", "Product_Meeting_Notes.pdf", "minutes"},
		{"docs", "docs", "Minutes of the meeting", "minutes"},
		{"files", "pdf", "meeting-notesheet.pdf", "files"},
		{"images", "image", "会议纪要截图.png", "images"},
		{"docs", "docs", "周会邀请", "docs"},
	} {
		if got := ProjectFileCategory(tc.category, tc.format, tc.title); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.title, got, tc.want)
		}
	}
}
