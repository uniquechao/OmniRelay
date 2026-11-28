package group

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/agent/pmtools"
	"github.com/uniquechao/OmniRelay/internal/routing"
)

// Apply the user's accepted period to the effective arguments. These exact
// arguments are also stored as the inputs to subsequent authority/digest checks.
func (w work) reportToolArgs(name string, args pmtools.Args) (pmtools.Args, error) {
	period := w.Plan.EvidencePeriod
	if period == nil {
		return args, nil
	}
	switch name {
	case "briefing_get":
		day := args.String("business_date")
		if period.From <= day && day <= period.To {
			return args, nil
		}
		for _, a := range w.Plan.Actions {
			if a.Tool == "briefing_save" && a.Fields["business_date"] == day {
				out := maps.Clone(args)
				out["view"] = json.RawMessage(`"version"`)
				return out, nil
			}
		}
		return nil, access.ErrForbidden
	case "chat_search_messages", "daily_report_query", "briefing_list", "file_search", "file_get":
		if name == "file_get" && explicitFileReference(w.Address.Text, args.String("file_ref")) {
			return args, nil
		}
	case "task_candidate_chat_sources":
		return reportChatCandidateArgs(args, period)
	default:
		return args, nil
	}
	from, to := args.String("date_from"), args.String("date_to")
	for _, day := range []string{from, to} {
		if day != "" {
			if _, err := time.Parse(time.DateOnly, day); err != nil {
				return nil, pmtools.ErrArguments
			}
		}
	}
	from = max(from, period.From)
	if to == "" || to > period.To {
		to = period.To
	}
	if from > to {
		return nil, pmtools.ErrArguments
	}
	out := maps.Clone(args)
	out["date_from"], _ = json.Marshal(from)
	out["date_to"], _ = json.Marshal(to)
	switch name {
	case "chat_search_messages", "file_search", "file_get":
		out["business_timezone"], _ = json.Marshal(period.Timezone)
	case "briefing_list":
		out["view"] = json.RawMessage(`"period_with_directory"`)
	}
	return out, nil
}

// File IDs may contain periods and colons. A prefix of a different reference
// in the user's message must not authorize an out-of-period document.
func explicitFileReference(text, ref string) bool {
	if ref == "" {
		return false
	}
	tokens := strings.FieldsFunc(text, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.:-", r))
	})
	return slices.Contains(tokens, ref)
}

func reportChatCandidateArgs(args pmtools.Args, p *routing.EvidencePeriod) (pmtools.Args, error) {
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		return nil, err
	}
	start, err := time.ParseInLocation(time.DateOnly, p.From, loc)
	if err != nil {
		return nil, err
	}
	last, err := time.ParseInLocation(time.DateOnly, p.To, loc)
	if err != nil {
		return nil, err
	}
	end := last.AddDate(0, 0, 1)
	from, err := time.Parse(time.RFC3339, args.String("from_at"))
	if err != nil {
		return nil, pmtools.ErrArguments
	}
	to, err := time.Parse(time.RFC3339, args.String("to_at"))
	if err != nil {
		return nil, pmtools.ErrArguments
	}
	if from.Before(start) {
		from = start
	}
	if to.After(end) {
		to = end
	}
	if !from.Before(to) {
		return nil, pmtools.ErrArguments
	}
	out := maps.Clone(args)
	out["from_at"], _ = json.Marshal(from)
	out["to_at"], _ = json.Marshal(to)
	return out, nil
}

// These list APIs return raw linked evidence without a source-date filter.
// A period-bound request uses file_search/file_get or the dated chat readers.
func unboundedReportSource(name string) bool {
	switch name {
	case "task_candidate_sources", "task_candidate_list", "task_evidence_list":
		return true
	}
	return false
}
