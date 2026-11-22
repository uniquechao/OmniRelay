package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUserCommands(t *testing.T) {
	for _, tc := range []struct {
		args []string
		path string
	}{
		{[]string{"whoami"}, "/api/v1/me"},
		{[]string{"projects"}, "/api/v1/projects"},
		{[]string{"projects", "2"}, "/api/v1/projects?page=2"},
		{[]string{"project", "north"}, "/api/v1/projects/north/overview"},
		{[]string{"pm", "messages", "north"}, "/api/v1/projects/north/bot/messages"},
		{[]string{"pm", "ask", "north", "request-01", "今天有什么进展？"}, "/api/v1/projects/north/bot/messages"},
	} {
		t.Run(strings.Join(tc.args, "_"), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.RequestURI() != tc.path || r.Header.Get("Authorization") != "Bearer test-user" || r.Header.Get("Cookie") != "" {
					t.Error("incorrect path or credentials")
				}
				if len(tc.args) == 5 {
					var body map[string]string
					if r.Method != http.MethodPost || r.Header.Get("Idempotency-Key") != tc.args[3] ||
						json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 1 || body["text"] != tc.args[4] {
						t.Error("wrong PM submission")
					}
					w.WriteHeader(http.StatusAccepted)
				} else if r.Method != http.MethodGet {
					t.Errorf("unexpected method %s", r.Method)
				}
				_, _ = w.Write([]byte(`{"id":"work-1"}`))
			}))
			defer server.Close()
			var out bytes.Buffer
			if err := run(tc.args, server.URL, "test-user", server.Client().Transport, &out); err != nil || out.String() != "{\"id\":\"work-1\"}\n" {
				t.Fatalf("output %q, error %v", &out, err)
			}
		})
	}
}

func TestRefusedRequests(t *testing.T) {
	for _, tc := range []struct {
		args []string
		url  string
		key  string
	}{
		{[]string{"whoami"}, "http://example.org", "test-user"},
		{[]string{"whoami"}, "https://example.org", ""},
		{[]string{"project", "../other"}, "https://example.org", "test-user"},
		{[]string{"pm", "ask", "north", "short", "test"}, "https://example.org", "test-user"},
	} {
		var out bytes.Buffer
		if err := run(tc.args, tc.url, tc.key, nil, &out); err == nil || out.Len() != 0 {
			t.Errorf("expected local rejection for %v", tc.args)
		}
	}
	for _, code := range []int{301, 401, 403, 409} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/me" {
					t.Error("redirect was followed")
				}
				w.Header().Set("Location", "/redirect")
				w.WriteHeader(code)
			}))
			defer server.Close()
			var out bytes.Buffer
			err := run([]string{"whoami"}, server.URL, "test-user", server.Client().Transport, &out)
			if err == nil || !strings.Contains(err.Error(), http.StatusText(code)) || strings.Contains(err.Error(), "test-user") || out.Len() != 0 {
				t.Errorf("unexpected error or output: %v, %q", err, &out)
			}
		})
	}
}
