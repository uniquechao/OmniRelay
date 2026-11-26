package dsh

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRuntimeReadiness(t *testing.T) {
	status := 200
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" || r.Method != "GET" {
			t.Errorf("unexpected readiness request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(status)
	}))
	c := Client{base: server.URL, http: server.Client()}
	for code, want := range map[int]string{200: "ready", 503: "not_ready", 403: "unreachable"} {
		status = code
		if got := c.Readiness(context.Background()); got != want {
			t.Errorf("status %d: got %s, want %s", code, got, want)
		}
	}
	server.Close()
	if got := c.Readiness(context.Background()); got != "unreachable" {
		t.Fatalf("stopped server: %s", got)
	}
}
