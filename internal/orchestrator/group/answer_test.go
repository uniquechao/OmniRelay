package group

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/uniquechao/OmniRelay/internal/agent/pmtools"
)

func TestReportEvidenceCitations(t *testing.T) {
	for _, c := range []struct {
		tool, input, result string
		want                []string
	}{
		{"briefing_list", "{}", `{"items":[{"business_date":"2026-11-23","version":2}]}`, []string{"已保存简报目录 2026-11-23 · 版本 2（摘要）"}},
		{"briefing_get", `{"business_date":"2026-11-23"}`, `{"business_date":"2026-11-23","body":"草案","version":2}`, []string{"项目简报 2026-11-23 · 版本 2"}},
		{"briefing_get", `{"business_date":"2026-11-23"}`, `{"business_date":"2026-11-23","version":0}`, nil},
		{"briefing_get", `{"business_date":"2026-11-23"}`, `{"business_date":"2026-11-20","body":"草案","version":2}`, nil},
		{"daily_report_query", `{"source":"published"}`, `{"items":[{"business_date":"2026-11-23","chat_id":"oc1","message_id":"om2","visible_submissions":3}]}`, []string{"已发布日报统计 2026-11-23 · 群 oc1 · 消息 om2"}},
		{"daily_report_query", "{}", `{"data":{"items":[{"work_date":"2026-11-23","account_id":"alice","note":"自述","version":3}]}}`, []string{"成员日报 2026-11-23 · alice · 版本 3"}},
		{"chat_search_messages", `{"chat_id":"oc1"}`, `{"chat_id":"oc1","items":[{"id":"om2","text":"计划上线","sent_at":"2026-11-23T20:00:00+08:00"},{"id":"om3","text":""}]}`, []string{"群 oc1 · 消息 om2 · 2026-11-23T20:00:00+08:00"}},
		{"chat_search_messages", `{"chat_id":"oc1"}`, `{"chat_id":"other","items":[{"id":"om2","text":"计划上线","sent_at":"2026-11-23T20:00:00+08:00"}]}`, nil},
		{"task_get", `{"task_id":"id1"}`, `{"id":"id1","task_code":"BC-001","version":4}`, []string{"任务 BC-001（id1）· 版本 4"}},
		{"task_activity_list", `{"task_id":"id1"}`, `{"items":[{"id":2,"kind":"change","created_at":"2026-11-24T01:00:00Z"}]}`, []string{"任务 id1 · 活动 2（change）· 2026-11-24T01:00:00Z"}},
	} {
		t.Run(c.tool, func(t *testing.T) {
			var args pmtools.Args
			if err := json.Unmarshal([]byte(c.input), &args); err != nil {
				t.Fatal(err)
			}
			if got := evidenceCitations(c.tool, args, json.RawMessage(c.result)); !slices.Equal(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}
