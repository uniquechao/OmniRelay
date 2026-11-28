package group

import (
	"context"
	"github.com/uniquechao/OmniRelay/internal/routing"
	"strings"
	"testing"
	"time"
)

func TestEvidenceWeeksUseLocalCalendarAndAdmissionDate(t *testing.T) {
	anchor := time.Date(2026, 11, 2, 1, 10, 0, 0, time.UTC)
	for _, tc := range []struct{ zone, kind, from, to string }{
		{"Asia/Shanghai", "last_week", "2026-10-26", "2026-11-01"},
		{"America/New_York", "last_week", "2026-10-19", "2026-10-25"},
		{"America/New_York", "this_week", "2026-10-26", "2026-11-01"},
		{"Asia/Shanghai", "this_week", "2026-11-02", "2026-11-02"},
	} {
		p := routing.EvidencePeriod{Kind: tc.kind, Timezone: tc.zone}
		if err := resolveEvidenceDates(&p, anchor); err != nil || p.From != tc.from || p.To != tc.to {
			t.Fatalf("%s %s %+v %v", tc.zone, tc.kind, p, err)
		}
	}
	for _, tc := range []struct {
		from, to, evidence string
		ok                 bool
	}{
		{"2026-11-01", "2026-11-02", "本月头两天", true},
		{"2026-11-02", "2026-11-01", "本月头两天", false},
		{"2026-11-01", "2026-11-02", "不存在于当前请求", false},
	} {
		p := routing.Plan{Mode: "read", Intent: "project", EvidencePeriod: &routing.EvidencePeriod{Kind: "range", From: tc.from, To: tc.to, Evidence: tc.evidence}}
		if (p.Validate(routing.PlanRequest{Text: "请总结本月头两天的群内结论。"}) == nil) != tc.ok {
			t.Error(tc)
		}
	}
}
func TestSourcePeriodSurvivesRetryIndependentOfFilingDate(t *testing.T) {
	g := newGate(t)
	const question = "把此前一个自然周的所有会议结论存进本周简报，业务日期 2026-11-28。"
	g.PlanIntent = func(context.Context, routing.PlanRequest) (routing.Plan, error) {
		return routing.Plan{Intent: "project", Mode: "execute", GroupReply: &routing.GroupResponse{Kind: "answer"}, EvidencePeriod: &routing.EvidencePeriod{Kind: "last_week", Evidence: "此前一个自然周"}, Actions: []routing.PlannedAction{{Tool: "briefing_save", Evidence: question, Fields: map[string]string{"business_date": "2026-11-28"}, Generate: []string{"title", "body"}}}}, nil
	}
	id := g.projectMention("freeze-period", question)
	w, err := g.claim(g.ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := w.Plan.EvidencePeriod
	if p == nil || p.From == "" || p.To == "" || p.Timezone == "" || !strings.Contains(w.Prompt, p.From+" 到 "+p.To) || w.Plan.Actions[0].Fields["business_date"] != "2026-11-28" {
		t.Fatalf("unbound evidence window: %+v", w.Plan)
	}
	g.sql("UPDATE project_task_boards SET business_timezone='Pacific/Honolulu' WHERE project_id='north'")
	g.sql("UPDATE agent_group_work SET lease_until=NULL WHERE id=$1", id)
	w, err = g.claim(g.ctx)
	if err != nil || w.Plan.EvidencePeriod == nil || *w.Plan.EvidencePeriod != *p {
		t.Fatalf("retry changed evidence period: %+v %v", w.Plan, err)
	}
}
