// Command omnirelaycli calls OmniRelay as the owner of a personal API key.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const usage = `Usage:
  omnirelaycli whoami
  omnirelaycli projects [page]
  omnirelaycli project <project-id>
  omnirelaycli pm messages <project-id>
  omnirelaycli pm ask <project-id> <request-id> <question>

Set OMNIRELAY_URL to the HTTPS origin and OMNIRELAY_API_KEY to your personal key.
Reuse the same request ID and question when retrying a submission.
Commands write the API's JSON response to standard output.`

var (
	projectID = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.:-]{0,199}$`)
	requestID = regexp.MustCompile(`^[A-Za-z0-9_-]{8,96}$`)
)

func main() {
	if err := run(os.Args[1:], os.Getenv("OMNIRELAY_URL"), os.Getenv("OMNIRELAY_API_KEY"), nil, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, origin, key string, transport http.RoundTripper, out io.Writer) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(out, usage)
		return err
	}
	method, path := http.MethodGet, ""
	var query url.Values
	var body io.Reader
	var idempotencyKey string
	switch {
	case len(args) == 1 && args[0] == "whoami":
		path = "/api/v1/me"
	case (len(args) == 1 || len(args) == 2) && args[0] == "projects":
		path = "/api/v1/projects"
		if len(args) == 2 {
			page, err := strconv.Atoi(args[1])
			if err != nil || page < 1 || page > 100_000 {
				return errors.New("page must be between 1 and 100000")
			}
			query = url.Values{"page": {strconv.Itoa(page)}}
		}
	case len(args) == 2 && args[0] == "project" && projectID.MatchString(args[1]):
		path = "/api/v1/projects/" + args[1] + "/overview"
	case len(args) == 3 && args[0] == "pm" && args[1] == "messages" && projectID.MatchString(args[2]):
		path = "/api/v1/projects/" + args[2] + "/bot/messages"
	case len(args) == 5 && args[0] == "pm" && args[1] == "ask" && projectID.MatchString(args[2]):
		idempotencyKey = args[3]
		if !requestID.MatchString(idempotencyKey) {
			return errors.New("request ID must contain 8 to 96 letters, digits, underscores or hyphens")
		}
		method, path = http.MethodPost, "/api/v1/projects/"+args[2]+"/bot/messages"
		data, err := json.Marshal(map[string]string{"text": args[4]})
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	default:
		return errors.New(usage)
	}

	base, err := url.Parse(origin)
	if err != nil || base.Scheme != "https" || base.Hostname() == "" || base.User != nil ||
		(base.Path != "" && base.Path != "/") || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" {
		return errors.New("OMNIRELAY_URL must be an HTTPS origin, for example https://relay.example.org")
	}
	if strings.TrimSpace(key) == "" {
		return errors.New("set OMNIRELAY_API_KEY to your personal API key")
	}
	base.Path = path
	base.RawQuery = query.Encode()
	req, err := http.NewRequest(method, base.String(), body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	client := http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("OmniRelay returned HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if !json.Valid(data) {
		return errors.New("OmniRelay response is not valid JSON")
	}
	_, err = fmt.Fprintln(out, string(bytes.TrimSpace(data)))
	return err
}
