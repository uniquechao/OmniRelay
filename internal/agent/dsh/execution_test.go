package dsh

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestExecutionDeadlineAndConflictClassification(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		permanent  bool
	}{
		{"immutable", `{"error":"immutable_work_binding"}`, 409, true},
		{"fenced", `{}`, 409, false},
		{"busy", `{}`, 202, false},
		{"transient", `{"error":"immutable_work_binding"}`, 503, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := time.Now().Add(2 * time.Minute).UnixMilli()
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				var submitted Binding
				if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil || submitted.ExecuteBeforeMS != before {
					t.Error("incorrect execution deadline", submitted.ExecuteBeforeMS, err)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			c := Client{base: server.URL, http: server.Client()}
			_, err := c.Execute(t.Context(), Binding{Work: "example", ExecuteBeforeMS: before})
			if err == nil || errors.Is(err, ErrWorkBindingConflict) != tc.permanent {
				t.Fatal(err)
			}
		})
	}
}

func TestExpiredExecutionRecoversOnlyExistingCompletion(t *testing.T) {
	before := time.Now().Add(-time.Second).UnixMilli()
	status := http.StatusOK
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("started a model after its execution deadline")
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"session":"existing","turn":1}`))
		}
	}))
	defer server.Close()
	c := Client{base: server.URL, http: server.Client()}
	b := Binding{Work: "example", ExecuteBeforeMS: before}
	if done, err := c.Execute(t.Context(), b); err != nil || done.Session != "existing" {
		t.Fatal(done, err)
	}
	status = http.StatusAccepted
	if _, err := c.Execute(t.Context(), b); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
