package routing

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/uniquechao/OmniRelay/internal/agent/pmtools"
)

// An input to the isolated planner has no transcript, sources, tools, persona,
// project IDs or ambient credentials. Intent is an explicit UI role selector.
type PlanRequest struct {
	Group             bool   `json:"group,omitempty"`
	DisplayedProposal bool   `json:"displayed_proposal,omitempty"`
	ProjectSelected   bool   `json:"project_selected,omitempty"`
	Text              string `json:"text"`
	Intent            string `json:"intent,omitempty"`
	Private           bool   `json:"private"`
}
type PlannedAction struct {
	ChildActions []PlannedAction   `json:"child_actions,omitempty"`
	Targets      map[string]string `json:"targets,omitempty"`
	// Expected is populated by Gateway when a draft is published, never by the planner.
	Expected map[string]string `json:"expected,omitempty"`
	Tool     string            `json:"tool"`
	Evidence string            `json:"evidence"`
	// Each field is a literal requested value (or a human-readable resource
	// selector), independently validated at invocation. Only content fields may
	// be synthesized. Versions are always supplied by the domain's optimistic lock.
	Fields   map[string]string `json:"fields"`
	Generate []string          `json:"generate,omitempty"`
}

// EvidencePeriod describes when facts may be drawn from sources. It is
// independent of the business date of the record a user chooses to save.
type EvidencePeriod struct {
	Kind     string `json:"kind"`
	Evidence string `json:"evidence"`
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	Timezone string `json:"timezone,omitempty"`
}

// GroupResponse is classified from the authenticated group message alone.
type GroupResponse struct {
	// Set only by Gateway when a tool returns data outside the answering group.
	PrivateSources  bool   `json:"private_sources,omitempty"`
	Kind            string `json:"kind"`
	MentionEvidence string `json:"mention_evidence,omitempty"`
}
type Plan struct {
	GroupReply     *GroupResponse  `json:"group_reply,omitempty"`
	PriorResult    string          `json:"prior_result,omitempty"`
	EvidencePeriod *EvidencePeriod `json:"evidence_period,omitempty"`
	Project        string          `json:"project,omitempty"`
	Intent         string          `json:"intent"`
	Mode           string          `json:"mode"`
	Actions        []PlannedAction `json:"actions"`
	Selection      int             `json:"selection,omitempty"`
	Clarification  string          `json:"clarification,omitempty"`
}

func (p Plan) Allows(tool string) bool {
	return (p.Mode == "execute" || p.Mode == "confirm") && slices.ContainsFunc(p.Actions, func(a PlannedAction) bool { return a.Tool == tool })
}
func role(intent string) string {
	switch intent {
	case "admin":
		return "management-pm"
	case "project":
		return "project-pm"
	case "knowledge":
		return "query"
	}
	return ""
}
func GeneratedField(tool, field string) bool {
	switch field {
	case "title", "body", "description", "acceptance", "change_reason", "reason", "completion_note", "blocked_reason", "note", "responsibility", "role_title":
		return true
	case "candidates", "session_ref":
		return tool == "task_candidate_submit"
	}
	return false
}

// These are optimistic concurrency inputs owned by the domain and Gateway.
func ServerFields() []string {
	return []string{"expected_version", "expected_task_version", "expected_board_version", "task_version", "target_version", "primary_version", "generation", "expected_project_role", "source_revision"}
}
func VersionField(field string) bool { return slices.Contains(ServerFields(), field) }

type ActionContract struct {
	Name     string   `json:"name"`
	Literal  []string `json:"literal,omitempty"`
	Selector []string `json:"selector,omitempty"`
	Content  []string `json:"content,omitempty"`
	Required []string `json:"required,omitempty"`
}

func PlanningCatalog() []ActionContract {
	var catalog []ActionContract
	for _, role := range []string{"project-pm", "management-pm"} {
		for _, t := range pmtools.RoleTools(role) {
			if !t.Write {
				continue
			}
			entry := ActionContract{Name: t.Name}
			for _, k := range t.FieldNames() {
				if VersionField(k) {
					continue
				}
				switch {
				case Selector(k):
					entry.Selector = append(entry.Selector, k)
				case GeneratedField(t.Name, k):
					entry.Content = append(entry.Content, k)
				default:
					entry.Literal = append(entry.Literal, k)
				}
				if slices.Contains(t.RequiredFields(), k) {
					entry.Required = append(entry.Required, k)
				}
			}
			catalog = append(catalog, entry)
		}
	}
	return catalog
}
func (p Plan) Validate(r PlanRequest) error {

	if r.Group && p.GroupReply == nil {
		return errors.New("invalid semantic plan: missing group response")
	}
	if g := p.GroupReply; g != nil {
		if !r.Group || g.PrivateSources || (g.Kind != "answer" && g.Kind != "open_tasks") || g.Kind == "open_tasks" && (p.Mode != "read" || p.Intent == "admin" || p.EvidencePeriod != nil) {
			return errors.New("invalid semantic plan: group response")
		}
		if g.MentionEvidence != "" && (g.Kind != "open_tasks" || strings.TrimSpace(g.MentionEvidence) == "" || len([]rune(g.MentionEvidence)) > 256 || !strings.Contains(r.Text, g.MentionEvidence)) {
			return errors.New("invalid semantic plan: group mention evidence")
		}
	}
	if p.PriorResult != "" && (p.Mode != "execute" || len([]rune(p.PriorResult)) > 512 || strings.TrimSpace(p.PriorResult) == "" || !strings.Contains(r.Text, p.PriorResult)) {
		return errors.New("invalid semantic plan: prior_result.evidence")
	}
	if scope := p.EvidencePeriod; scope != nil {
		if scope.Timezone != "" || strings.TrimSpace(scope.Evidence) == "" || !strings.Contains(r.Text, scope.Evidence) {
			return errors.New("invalid semantic plan: evidence_period.evidence_or_timezone")
		}
		switch scope.Kind {
		case "last_week", "this_week":
			if scope.From != "" || scope.To != "" {
				return errors.New("invalid semantic plan: evidence_period.relative_bounds")
			}
		case "range":
			from, e1 := time.Parse(time.DateOnly, scope.From)
			to, e2 := time.Parse(time.DateOnly, scope.To)
			if e1 != nil || e2 != nil || from.After(to) {
				return errors.New("invalid semantic plan: evidence_period.range")
			}
		default:
			return errors.New("invalid semantic plan: evidence_period.kind")
		}
	}
	if p.Project != "" && (len(p.Project) > 256 || !EvidenceContains(r.Text, p.Project)) {
		return errors.New("invalid semantic plan: project.evidence")
	}
	if role(p.Intent) == "" || r.Intent != "" && r.Intent != p.Intent || len(p.Actions) > 32 || len(p.Clarification) > 1200 {
		return errors.New("invalid semantic plan: intent_or_plan_bounds")
	}
	switch p.Mode {
	case "confirm", "revise":
		if len(p.Actions) != 0 || p.Selection < 0 || p.Selection > 32 || p.Intent != "project" {
			return errors.New("invalid semantic plan: mode.confirm_selection")
		}
	case "read", "clarify":
		if len(p.Actions) != 0 || p.Selection != 0 {
			return errors.New("invalid semantic plan: mode.read_actions")
		}
	case "execute", "draft":
		if len(p.Actions) == 0 || p.Selection != 0 || p.Intent == "knowledge" {
			return errors.New("invalid semantic plan: mode.write_actions")
		}
	default:
		return errors.New("invalid semantic plan: mode.unknown")
	}
	for _, a := range p.Actions {
		if len(a.Expected) > 0 || len(a.Targets) > 0 || strings.TrimSpace(a.Evidence) == "" || !strings.Contains(r.Text, a.Evidence) || len(a.Fields)+len(a.Generate) > 32 {
			return errors.New("invalid semantic plan: action.evidence_or_reserved_fields")
		}
		var tool *pmtools.Tool
		for _, candidate := range pmtools.RoleTools(role(p.Intent)) {
			if candidate.Name == a.Tool && candidate.Write {
				tool = &candidate
				break
			}
		}
		if len(a.ChildActions) > 0 {
			if a.Tool != "delegate_project" || len(a.ChildActions) > 16 {
				return errors.New("invalid semantic plan: action.child_actions")
			}
			child := Plan{Intent: "project", Mode: "execute", Actions: a.ChildActions}
			if err := child.Validate(PlanRequest{Text: a.Evidence, Intent: "project"}); err != nil {
				return errors.Join(errors.New("invalid semantic plan: action.child_contract"), err)
			}
		}
		if tool == nil {
			return errors.New("invalid semantic plan: action.role_tool")
		}
		for k, v := range a.Fields {
			if !tool.HasField(k) || VersionField(k) || v == "" || len(v) > 4096 {
				return errors.New("invalid semantic plan: action.fields.shape")
			}
			// IDs, assignees and other selectors must originate in the user's own clause.
			if k == "business_date" && !strings.HasPrefix(v, "@") && !strings.Contains(a.Evidence, v) {
				return errors.New("invalid semantic plan: action.fields.business_date.evidence")
			}
			if k == "title" && !strings.Contains(a.Evidence, v) {
				return errors.New("invalid semantic plan: action.fields.title.evidence")
			}
			if strings.HasSuffix(k, "_ids") {
				var refs []string
				if json.Unmarshal([]byte(v), &refs) != nil || len(refs) > 50 {
					return errors.New("invalid semantic plan: action.fields.selectors.shape")
				}
				for _, ref := range refs {
					if ref == "" || !EvidenceContains(a.Evidence, ref) {
						return errors.New("invalid semantic plan: action.fields.selectors.evidence")
					}
				}
			} else if Selector(k) && !EvidenceContains(a.Evidence, v) && !(k == "candidate_id" && v == "@submitted") {
				return errors.New("invalid semantic plan: action.fields.selector.evidence")
			}
		}
		for _, k := range a.Generate {
			if !tool.HasField(k) || !GeneratedField(a.Tool, k) || a.Fields[k] != "" {
				return errors.New("invalid semantic plan: action.generate.field")
			}
		}
		for _, k := range tool.RequiredFields() {
			if !VersionField(k) && a.Fields[k] == "" && !slices.Contains(a.Generate, k) {
				return errors.New("invalid semantic plan: action.required_field." + k)
			}
		}
	}
	return nil
}

// ASCII identifiers must match a complete token. Chinese names and titles
// are verified for uniqueness against the scoped authoritative directory.
func EvidenceContains(text, value string) bool {
	if value == "" {
		return false
	}
	if strings.IndexFunc(value, func(r rune) bool { return r > unicode.MaxASCII }) >= 0 {
		return strings.Contains(text, value)
	}
	isID := func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
	}
	for rest := text; ; {
		i := strings.Index(rest, value)
		if i < 0 {
			return false
		}
		end := i + len(value)
		if (i == 0 || !isID(rune(rest[i-1])) || !isID(rune(value[0]))) && (end == len(rest) || !isID(rune(rest[end])) || !isID(rune(value[len(value)-1]))) {
			return true
		}
		rest = rest[end:]
	}
}
func Selector(k string) bool {
	return strings.HasSuffix(k, "_id") || strings.HasSuffix(k, "_ids") || k == "file_ref"
}
func (p Plan) JSON() []byte { b, _ := json.Marshal(p); return b }
