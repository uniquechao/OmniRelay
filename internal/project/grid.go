// Package project provides the authorized project workbench and Bot integration.
package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/uniquechao/OmniRelay/internal/access"
	"github.com/uniquechao/OmniRelay/internal/identity"
)

var (
	errDenied      = errors.New("project access denied")
	errUnmapped    = errors.New("OmniGrid identity unavailable")
	errUnavailable = errors.New("project source unavailable")
	errConflict    = errors.New("project state changed")
	errInput       = errors.New("invalid project request")
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.:-]{0,199}$`)

type mapping struct {
	Account string `json:"account_id"`
	OpenID  string `json:"open_id"`
	User    string `json:"user_id"`
	Token   string `json:"token"`
}

type Grid struct {
	origin      string
	client      http.Client
	tenant, app string
	mappings    map[string]mapping
}

func NewGrid(origin, file, tenant, app string, client http.Client) (*Grid, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || strings.Contains(origin, "#") {
		return nil, errors.New("OMNIRELAY_OMNIGRID_ORIGIN must be an HTTPS origin")
	}
	if file == "" {
		return nil, errors.New("OMNIRELAY_OMNIGRID_IDENTITIES_FILE is required")
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return (&Grid{strings.TrimRight(origin, "/"), client, tenant, app, nil}).withMappings(file)
}

func (g *Grid) withMappings(file string) (*Grid, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, errors.New("cannot open OmniGrid identity mappings")
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 2<<20))
	decoder.DisallowUnknownFields()
	var entries []mapping
	if decoder.Decode(&entries) != nil || decoder.Decode(new(json.RawMessage)) != io.EOF {
		return nil, errors.New("invalid OmniGrid identity mappings")
	}
	g.mappings = make(map[string]mapping, len(entries))
	users := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if !identifier.MatchString(entry.Account) || !identifier.MatchString(entry.OpenID) ||
			!identifier.MatchString(entry.User) || !strings.HasPrefix(entry.Token, "ogt_") ||
			len(entry.Token) > 256 || strings.ContainsAny(entry.Token, " \r\n\t") ||
			g.mappings[entry.Account].User != "" || users[entry.User] {
			return nil, errors.New("invalid or duplicate OmniGrid identity mapping")
		}
		g.mappings[entry.Account], users[entry.User] = entry, true
	}
	return g, nil
}

func (g *Grid) credential(ctx context.Context, actor identity.Principal) (mapping, error) {
	if g == nil {
		return mapping{}, errUnavailable
	}
	entry, ok := g.mappings[actor.AccountID]
	if actor.TenantKey != g.tenant || actor.AppID != g.app || !ok || entry.OpenID != actor.OpenID {
		return mapping{}, errUnmapped
	}
	var current struct {
		ID   string
		Role string
	}
	err := g.get(ctx, entry, "/auth/me", nil, &current)
	if errors.Is(err, errDenied) || (err == nil && (current.ID != entry.User || current.Role == "" || current.Role == "service")) {
		return mapping{}, errUnmapped
	}
	return entry, err
}

func (g *Grid) get(ctx context.Context, entry mapping, path string, query url.Values, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.origin+"/api/v1"+path, nil)
	if err != nil {
		return errUnavailable
	}
	req.URL.RawQuery = query.Encode()
	req.Header.Set("Authorization", "Bearer "+entry.Token)
	req.Header.Set("Accept", "application/json")
	res, err := g.client.Do(req)
	if err != nil {
		return errUnavailable
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return errDenied
	case http.StatusConflict:
		return errConflict
	case http.StatusUnprocessableEntity:
		return errInput
	case http.StatusOK:
	default:
		return errUnavailable
	}
	const maxResponse = 8 << 20
	body, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
	if err != nil || len(body) > maxResponse || json.Unmarshal(body, dst) != nil {
		return errUnavailable
	}
	return nil
}

type state struct {
	Project      string            `json:"project_id"`
	Member       string            `json:"current_member_id"`
	BusinessDate string            `json:"business_date"`
	Actions      []string          `json:"actions"`
	Versions     map[string]string `json:"versions"`
}

type Project struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Type     string `json:"project_type"`
	Timezone string `json:"business_timezone"`
}

type gridMember struct {
	ID      string `json:"id"`
	Project string `json:"project_id"`
	Roster  string `json:"roster_status"`
	Active  bool   `json:"active"`
}

func projectPath(id string) string { return "/projects/" + url.PathEscape(id) }

func (g *Grid) state(ctx context.Context, entry mapping, id string) (state, error) {
	var current state
	if !identifier.MatchString(id) {
		return current, errDenied
	}
	err := g.get(ctx, entry, projectPath(id)+"/state", nil, &current)
	if err == nil && (current.Project != id || !slices.Contains(current.Actions, "read") || current.Versions["access"] == "") {
		return state{}, errUnavailable
	}
	return current, err
}

func (g *Grid) resolve(ctx context.Context, entry mapping, id string) (state, access.ProjectAccess, error) {
	current, err := g.state(ctx, entry, id)
	if err != nil || current.Member == "" {
		return current, access.ProjectAccess{}, err
	}
	var roster struct {
		Project string       `json:"project_id"`
		Members []gridMember `json:"members"`
	}
	if err := g.get(ctx, entry, projectPath(id)+"/members", nil, &roster); err != nil {
		return current, access.ProjectAccess{}, err
	}
	if roster.Project != id {
		return current, access.ProjectAccess{}, errUnavailable
	}
	formal := slices.ContainsFunc(roster.Members, func(member gridMember) bool {
		return member.ID == current.Member && member.Project == id && member.Active && member.Roster == "formal"
	})
	if !formal {
		return current, access.ProjectAccess{}, nil
	}
	verified, err := g.state(ctx, entry, id)
	if err != nil {
		return current, access.ProjectAccess{}, err
	}
	if verified.Member != current.Member || verified.Versions["access"] != current.Versions["access"] {
		return current, access.ProjectAccess{}, errConflict
	}
	// Keep delegated authority bounded by OmniGrid's opaque access revision.
	// A change in the revision invalidates local delegated settings authority.
	return verified, access.ProjectAccess{
		Member:               true,
		Manager:              slices.Contains(verified.Actions, "manage_access"),
		CanAssign:            slices.Contains(verified.Actions, "confirm_tasks"),
		MembershipGeneration: fmt.Sprintf("%s:%s", verified.Member, verified.Versions["access"]),
	}, nil
}

func (g *Grid) ResolveProjectAccess(ctx context.Context, actor identity.Principal, id string) (access.ProjectAccess, error) {
	entry, err := g.credential(ctx, actor)
	if errors.Is(err, errUnmapped) {
		return access.ProjectAccess{}, nil
	}
	if err != nil {
		return access.ProjectAccess{}, err
	}
	_, result, err := g.resolve(ctx, entry, id)
	if errors.Is(err, errDenied) {
		return access.ProjectAccess{}, nil
	}
	return result, err
}

func (g *Grid) candidates(ctx context.Context, entry mapping, filter url.Values) ([]Project, error) {
	var page struct {
		Items   []Project
		Total   int
		HasMore bool `json:"has_more"`
	}
	query := url.Values{"all_items": {"true"}, "summary": {"false"}}
	for key, values := range filter {
		query[key] = values
	}
	err := g.get(ctx, entry, "/projects", query, &page)
	if err == nil && (page.HasMore || page.Total != len(page.Items)) {
		err = errUnavailable
	}
	return page.Items, err
}

func (g *Grid) HasProjectAccess(ctx context.Context, actor identity.Principal) (bool, error) {
	entry, err := g.credential(ctx, actor)
	if errors.Is(err, errUnmapped) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	items, err := g.candidates(ctx, entry, nil)
	if err != nil {
		return false, err
	}
	for _, item := range items {
		_, grant, err := g.resolve(ctx, entry, item.ID)
		if errors.Is(err, errDenied) {
			continue
		}
		if err != nil || grant.Member {
			return grant.Member, err
		}
	}
	return false, nil
}
