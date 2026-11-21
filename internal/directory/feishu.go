package directory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const contacts = "/open-apis/contact/v3"

type scope struct {
	Departments []string `json:"departments"`
	People      []string `json:"people"`
}

type scopeSummary struct {
	DepartmentRoots int  `json:"department_roots"`
	ExplicitPeople  int  `json:"explicit_people"`
	EntireOrg       bool `json:"entire_org"`
}

func (s scope) summary() scopeSummary {
	return scopeSummary{DepartmentRoots: len(s.Departments), ExplicitPeople: len(s.People), EntireOrg: slices.Contains(s.Departments, "0")}
}

func (s scope) hash() string {
	data, _ := json.Marshal(s)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

type department struct {
	ID          string   `json:"id"`
	ParentID    string   `json:"parent_id"`
	Name        string   `json:"name"`
	Path        []string `json:"path"`
	HasChildren bool     `json:"has_children"`
}

type contactPerson struct {
	ID          string   `json:"open_id"`
	Name        string   `json:"name"`
	Departments []string `json:"department_ids"`
	Avatar      struct {
		URL string `json:"avatar_72"`
	} `json:"avatar"`
	Status struct {
		Resigned  bool  `json:"is_resigned"`
		Exited    bool  `json:"is_exited"`
		Activated *bool `json:"is_activated"`
		Frozen    bool  `json:"is_frozen"`
		Inactive  bool  `json:"is_unjoin"`
	} `json:"status"`
}

func (p contactPerson) status() (state string, unactivated bool) {
	switch {
	case p.Status.Resigned:
		return "resigned", false
	case p.Status.Exited || p.Status.Frozen || p.Status.Inactive:
		return "inactive", false
	case p.Status.Activated != nil && !*p.Status.Activated:
		return "inactive", true
	case p.Status.Activated != nil:
		return "active", false
	default:
		return "unknown", false
	}
}

func (p contactPerson) avatarURL() string {
	address, err := url.Parse(p.Avatar.URL)
	if err != nil || address.Scheme != "https" || address.Host == "" || address.User != nil {
		return ""
	}
	return address.String()
}

type contactDepartment struct {
	ID     string  `json:"open_department_id"`
	Parent *string `json:"parent_department_id"`
	Name   string  `json:"name"`
}

type snapshot struct {
	ChatSenderChecks []chatSenderCheck
	Scope            scope
	Departments      map[string]department
	People           map[string]contactPerson
	Members          map[string]map[string]bool
	Departures       []string
	MissingExplicit  map[string]bool
}

type upstreamError struct {
	kind       string
	status     int
	code       int
	retryAfter time.Duration
}

func (e upstreamError) Error() string {
	return fmt.Sprintf("%s (HTTP %d, Feishu code %d)", e.kind, e.status, e.code)
}

func (e upstreamError) denied() bool {
	switch e.code {
	case 20009, 20068, 20069, 40004, 41050, 99991672, 99991673, 99991676, 99991679:
		return true
	case 40003, 99991400, 99991401, 99991403:
		return false
	default:
		return e.status == http.StatusForbidden
	}
}

func (e upstreamError) transient() bool {
	return e.status == 0 || e.status == http.StatusTooManyRequests || e.status >= 500 ||
		e.code == 99991400
}

func (s *Service) request(ctx context.Context, method, address, token string, form url.Values, out any) error {
	for attempt := range 3 {
		err := s.requestOnce(ctx, method, address, token, form, out)
		var upstream upstreamError
		if !errors.As(err, &upstream) || upstream.denied() || !upstream.transient() || attempt == 2 {
			return err
		}
		delay := max(time.Duration(1<<attempt)*500*time.Millisecond, upstream.retryAfter)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return errors.New("Feishu request retries exhausted")
}

func (s *Service) requestOnce(ctx context.Context, method, address, token string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, address, strings.NewReader(form.Encode()))
	if err != nil {
		return errors.New("invalid Feishu request URL")
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return upstreamError{kind: "Feishu transport unavailable"}
	}
	defer resp.Body.Close()
	var envelope struct {
		Code  *int            `json:"code"`
		Error json.RawMessage `json:"error"`
		Data  json.RawMessage `json:"data"`
	}
	const maxResponse = 4 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	valid := err == nil && len(data) <= maxResponse && json.Unmarshal(data, &envelope) == nil
	upstream := upstreamError{kind: "Feishu request rejected", status: resp.StatusCode}
	if valid && envelope.Code != nil {
		upstream.code = *envelope.Code
	}
	if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
		upstream.retryAfter = time.Duration(min(seconds, 30)) * time.Second
	}
	hasError := len(envelope.Error) > 0 && string(envelope.Error) != "null" && string(envelope.Error) != `""`
	if upstream.status < 200 || upstream.status >= 300 || upstream.code != 0 || hasError {
		return upstream
	}
	if !valid || envelope.Code == nil {
		return errors.New("invalid Feishu response")
	}
	if token != "" {
		data = envelope.Data
	}
	if len(data) == 0 || string(data) == "null" || json.Unmarshal(data, out) != nil {
		return errors.New("missing or invalid Feishu response data")
	}
	return nil
}

type collector struct {
	service *Service
	token   string
	left    int
}

func (f *collector) get(ctx context.Context, endpoint string, params url.Values, out any) error {
	if f.left <= 0 {
		return errors.New("Feishu directory request budget exhausted")
	}
	f.left--
	base := "https://open.feishu.cn"
	if f.service.cfg.BaseURL != "" {
		base = f.service.cfg.BaseURL
	}
	return f.service.request(ctx, http.MethodGet, base+contacts+endpoint+"?"+params.Encode(), f.token, nil, out)
}

type paging struct {
	More *bool  `json:"has_more"`
	Next string `json:"page_token"`
}

func paginate[T any](ctx context.Context, f *collector, path string, query url.Values, read func(T) (paging, error)) error {
	query.Set("page_size", "50")
	seen := map[string]bool{}
	for {
		var page T
		if err := f.get(ctx, path, query, &page); err != nil {
			return err
		}
		pager, err := read(page)
		if err != nil {
			return err
		}
		if pager.More == nil {
			return errors.New("missing Feishu pagination state")
		}
		if !*pager.More {
			return nil
		}
		if pager.Next == "" || seen[pager.Next] {
			return errors.New("incomplete Feishu pagination")
		}
		seen[pager.Next] = true
		query.Set("page_token", pager.Next)
	}
}

func validID(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	for _, char := range id {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_') {
			return false
		}
	}
	return true
}

func validOpenID(id string) bool {
	return validID(id) && strings.HasPrefix(id, "ou_") && len(id) > len("ou_")
}

func emptyIntermediatePage(pager paging, count int) bool {
	return count == 0 && pager.More != nil && *pager.More
}

func (f *collector) readScope(ctx context.Context) (scope, error) {
	type scopePage struct {
		paging
		Departments []string `json:"department_ids"`
		People      []string `json:"user_ids"`
		Groups      []string `json:"group_ids"`
	}
	result := scope{Departments: []string{}, People: []string{}}
	groups := map[string]bool{}
	err := paginate(ctx, f, "/scopes", url.Values{"department_id_type": {"open_department_id"}, "user_id_type": {"open_id"}}, func(page scopePage) (paging, error) {
		for _, id := range page.Groups {
			if !validID(id) {
				return page.paging, errors.New("invalid user group identifier in Feishu permission scope")
			}
			groups[id] = true
		}
		for _, entry := range page.Departments {
			if !validID(entry) {
				return page.paging, errors.New("invalid department identifier in Feishu permission scope")
			}
		}
		for _, entry := range page.People {
			if !validOpenID(entry) {
				return page.paging, errors.New("invalid person identifier in Feishu permission scope")
			}
		}
		if emptyIntermediatePage(page.paging, len(page.Departments)+len(page.People)+len(page.Groups)) {
			return page.paging, errors.New("empty intermediate Feishu scope page")
		}
		result.Departments = append(result.Departments, page.Departments...)
		result.People = append(result.People, page.People...)
		return page.paging, nil
	})
	if err != nil {
		return scope{}, err
	}
	for id := range groups {
		if err := f.readGroupMembers(ctx, id, &result); err != nil {
			return scope{}, err
		}
	}
	slices.Sort(result.Departments)
	slices.Sort(result.People)
	result.Departments = slices.Compact(result.Departments)
	result.People = slices.Compact(result.People)
	return result, err
}

func (f *collector) readGroupMembers(ctx context.Context, group string, result *scope) error {
	type member struct {
		ID   string `json:"member_id"`
		Type string `json:"member_type"`
	}
	type page struct {
		paging
		Members []member `json:"memberlist"`
	}
	for _, kind := range []string{"department", "user"} {
		params := url.Values{"member_type": {kind}, "member_id_type": {"open_id"}}
		err := paginate(ctx, f, "/group/"+group+"/member/simplelist", params, func(reply page) (paging, error) {
			if reply.Members == nil || emptyIntermediatePage(reply.paging, len(reply.Members)) {
				return reply.paging, errors.New("incomplete Feishu user group member page")
			}
			for _, entry := range reply.Members {
				if entry.Type != kind || !validID(entry.ID) {
					return reply.paging, errors.New("invalid Feishu user group member")
				}
				if kind == "department" {
					result.Departments = append(result.Departments, entry.ID)
				} else {
					if !validOpenID(entry.ID) {
						return reply.paging, errors.New("invalid Feishu user group person identifier")
					}
					result.People = append(result.People, entry.ID)
				}
			}
			return reply.paging, nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func departmentIDs() url.Values { return url.Values{"department_id_type": {"open_department_id"}} }

func personIDs() url.Values {
	ids := departmentIDs()
	ids.Set("user_id_type", "open_id")
	return ids
}

func (s *Service) collect(ctx context.Context, progress func(scope, int, int) error,
	verifyDepartures func(context.Context, *collector, *snapshot) error,
) (snapshot, error) {
	if s.cfg.AppSecret == "" {
		return snapshot{}, errors.New("Feishu credentials are missing")
	}
	base := "https://accounts.feishu.cn"
	if s.cfg.BaseURL != "" {
		base = s.cfg.BaseURL
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	err := s.request(ctx, http.MethodPost, base+"/oauth/v3/token", "", url.Values{
		"grant_type": {"client_credentials"}, "client_id": {s.cfg.AppID}, "client_secret": {s.cfg.AppSecret},
	}, &token)
	if err != nil {
		return snapshot{}, err
	}
	if token.AccessToken == "" {
		return snapshot{}, errors.New("missing Feishu tenant token")
	}
	f := collector{service: s, token: token.AccessToken, left: s.cfg.PageBudget}
	authorization, err := f.readScope(ctx)
	if err != nil {
		return snapshot{}, err
	}
	if err := progress(authorization, 0, 0); err != nil {
		return snapshot{}, err
	}
	result := snapshot{Scope: authorization, Departments: map[string]department{}, People: map[string]contactPerson{}, Members: map[string]map[string]bool{}, MissingExplicit: map[string]bool{}}
	if err := f.readDepartments(ctx, &result); err != nil {
		return snapshot{}, err
	}
	if err := f.readPeople(ctx, &result, progress); err != nil {
		return snapshot{}, err
	}
	if verifyDepartures != nil {
		if err := verifyDepartures(ctx, &f, &result); err != nil {
			return snapshot{}, err
		}
	}
	verified, err := f.readScope(ctx)
	if err != nil {
		return snapshot{}, err
	}
	if verified.hash() != authorization.hash() {
		if err := progress(verified, len(result.Departments), len(result.People)); err != nil {
			return snapshot{}, err
		}
		return snapshot{}, errors.New("Feishu authorization scope changed during sync")
	}
	if verifyDepartures != nil && s.db != nil {
		if err := s.checkChatSenders(ctx, &f, &result); err != nil {
			return snapshot{}, err
		}
	}
	return result, nil
}

func (f *collector) readDepartments(ctx context.Context, result *snapshot) error {
	type page struct {
		paging
		Items []contactDepartment `json:"items"`
	}
	raw := map[string]contactDepartment{}
	listed := map[string]bool{}
	queue := slices.Clone(result.Scope.Departments)
	visited := map[string]bool{}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		if visited[parent] {
			continue
		}
		visited[parent] = true
		if _, found := raw[parent]; !found && parent != "0" {
			var reply struct {
				Department contactDepartment `json:"department"`
			}
			if err := f.get(ctx, "/departments/"+parent, departmentIDs(), &reply); err != nil {
				return err
			}
			if reply.Department.ID != parent {
				return errors.New("unexpected Feishu department")
			}
			raw[parent] = reply.Department
		}
		args := departmentIDs()
		args.Set("fetch_child", "false")
		err := paginate(ctx, f, "/departments/"+parent+"/children", args, func(reply page) (paging, error) {
			if reply.Items == nil || emptyIntermediatePage(reply.paging, len(reply.Items)) {
				return reply.paging, errors.New("incomplete Feishu department page")
			}
			for _, entry := range reply.Items {
				if !validID(entry.ID) || entry.ID == "0" || listed[entry.ID] || entry.Parent == nil || *entry.Parent != parent {
					return reply.paging, errors.New("invalid Feishu child department")
				}
				if before, ok := raw[entry.ID]; ok && (before.Parent == nil || *before.Parent != parent || before.Name != entry.Name) {
					return reply.paging, errors.New("inconsistent Feishu department hierarchy")
				}
				raw[entry.ID] = entry
				listed[entry.ID] = true
				queue = append(queue, entry.ID)
			}
			return reply.paging, nil
		})
		if err != nil {
			return err
		}
	}
	for id, entry := range raw {
		if entry.Parent == nil || !validID(*entry.Parent) || strings.TrimSpace(entry.Name) == "" {
			return errors.New("incomplete Feishu department")
		}
		parent := *entry.Parent
		if _, visible := raw[parent]; visible {
			if !listed[id] {
				return errors.New("Feishu child missing from parent pages")
			}
		} else {
			if !slices.Contains(result.Scope.Departments, id) && parent != "0" {
				return errors.New("orphaned Feishu department")
			}
			parent = ""
		}
		result.Departments[id] = department{ID: id, Name: entry.Name, ParentID: parent}
	}
	return addDepartmentPaths(result.Departments)
}

func addDepartmentPaths(departments map[string]department) error {
	for id, entry := range departments {
		seen := map[string]bool{id: true}
		path := []string{entry.Name}
		for parent := entry.ParentID; parent != ""; {
			node, ok := departments[parent]
			if !ok || seen[parent] {
				return errors.New("invalid Feishu department ancestry")
			}
			seen[parent] = true
			node.HasChildren = true
			departments[parent] = node
			path = append(path, node.Name)
			parent = node.ParentID
		}
		slices.Reverse(path)
		entry.Path = path
		departments[id] = entry
	}
	return nil
}

func (f *collector) readPeople(ctx context.Context, result *snapshot, progress func(scope, int, int) error) error {
	type page struct {
		paging
		Items []contactPerson `json:"items"`
	}
	merge := func(entry contactPerson, parent string) error {
		if !validOpenID(entry.ID) || strings.TrimSpace(entry.Name) == "" {
			return errors.New("incomplete Feishu person")
		}
		if parent != "" && entry.Departments != nil && !slices.Contains(entry.Departments, parent) {
			return errors.New("Feishu person does not belong to the listed department")
		}
		for _, id := range entry.Departments {
			if !validID(id) {
				return errors.New("invalid Feishu person department identifier")
			}
		}
		if before, exists := result.People[entry.ID]; exists {
			oldStatus, oldUnactivated := before.status()
			newStatus, newUnactivated := entry.status()
			if before.Name != entry.Name || (oldStatus != "unknown" && newStatus != "unknown" &&
				(oldStatus != newStatus || oldUnactivated != newUnactivated)) {
				return errors.New("inconsistent Feishu person")
			}
			if newStatus == "unknown" {
				entry.Status = before.Status
			}
			for _, id := range before.Departments {
				if !slices.Contains(entry.Departments, id) {
					entry.Departments = append(entry.Departments, id)
				}
			}
		}
		result.People[entry.ID] = entry
		if result.Members[entry.ID] == nil {
			result.Members[entry.ID] = map[string]bool{}
		}
		if _, visible := result.Departments[parent]; visible {
			result.Members[entry.ID][parent] = true
		}
		return nil
	}
	parents := make([]string, 0, len(result.Departments)+1)
	for id := range result.Departments {
		parents = append(parents, id)
	}
	if slices.Contains(result.Scope.Departments, "0") {
		parents = append(parents, "0")
	}
	slices.Sort(parents)
	departmentsRead := 0
	for _, parent := range parents {
		seen := map[string]bool{}
		ids := personIDs()
		ids.Set("department_id", parent)
		err := paginate(ctx, f, "/users/find_by_department", ids, func(reply page) (paging, error) {
			if reply.Items == nil || emptyIntermediatePage(reply.paging, len(reply.Items)) {
				return reply.paging, errors.New("incomplete Feishu department member page")
			}
			for _, entry := range reply.Items {
				if seen[entry.ID] {
					return reply.paging, errors.New("duplicate Feishu person in department pages")
				}
				seen[entry.ID] = true
				if err := merge(entry, parent); err != nil {
					return reply.paging, err
				}
			}
			return reply.paging, nil
		})
		if err != nil {
			return err
		}
		if parent != "0" {
			departmentsRead++
		}
		if err := progress(result.Scope, departmentsRead, len(result.People)); err != nil {
			return err
		}
	}
	for _, id := range result.Scope.People {
		if _, found := result.People[id]; found {
			continue
		}
		person, err := f.readPerson(ctx, id)
		if userDoesNotExist(err) {
			result.MissingExplicit[id] = true
			continue
		}
		if err != nil {
			return err
		}
		if err := merge(person, ""); err != nil {
			return err
		}
		if err := progress(result.Scope, departmentsRead, len(result.People)); err != nil {
			return err
		}
	}
	for id, person := range result.People {
		for _, dept := range person.Departments {
			if _, visible := result.Departments[dept]; visible && !result.Members[id][dept] {
				return errors.New("Feishu person missing from their department pages")
			}
		}
	}
	return nil
}

func (f *collector) readPerson(ctx context.Context, id string) (contactPerson, error) {
	var reply struct {
		User contactPerson `json:"user"`
	}
	if err := f.get(ctx, "/users/"+id, personIDs(), &reply); err != nil {
		return contactPerson{}, err
	}
	if reply.User.ID != id {
		return contactPerson{}, errors.New("unexpected Feishu person")
	}
	return reply.User, nil
}

func userDoesNotExist(err error) bool {
	var upstream upstreamError
	return errors.As(err, &upstream) && upstream.code == 40007 &&
		(upstream.status == http.StatusBadRequest || upstream.status == http.StatusOK)
}
