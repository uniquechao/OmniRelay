package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coordination "k8s.io/client-go/kubernetes/typed/coordination/v1"
	"k8s.io/client-go/rest"
)

func TestLeaseHandoffWaitsForWorker(t *testing.T) {
	client := leaseTestAPI(t)
	a := &Lease{client: client, namespace: "staging", identity: "pod-a", duration: 2 * time.Second,
		deadline: 300 * time.Millisecond, retry: 50 * time.Millisecond}
	b := *a
	b.identity = "pod-b"

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	firstStarted, firstClosing, letFirstClose := make(chan struct{}), make(chan struct{}), make(chan struct{})
	firstDone := make(chan struct{})
	first, stopFirst := context.WithCancel(ctx)
	defer stopFirst()
	go func() {
		defer close(firstDone)
		a.Run(first, func(ctx context.Context, verify func(context.Context) error) error {
			if err := verify(ctx); err != nil {
				return err
			}
			close(firstStarted)
			<-ctx.Done()
			close(firstClosing)
			select {
			case <-letFirstClose:
			case <-t.Context().Done():
			}
			return ctx.Err()
		})
	}()
	receive(t, ctx, firstStarted)

	secondStarted, secondDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(secondDone)
		b.Run(ctx, func(ctx context.Context, verify func(context.Context) error) error {
			if err := verify(ctx); err != nil {
				return err
			}
			close(secondStarted)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	stopFirst()
	receive(t, ctx, firstClosing)
	select {
	case <-secondStarted:
		t.Fatal("second holder opened a connection before the first finished closing")
	case <-time.After(180 * time.Millisecond):
	}
	close(letFirstClose)
	receive(t, ctx, firstDone)
	receive(t, ctx, secondStarted)
	cancel()
	wait, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	receive(t, wait, secondDone)
}

func leaseTestAPI(t *testing.T) coordination.LeasesGetter {
	t.Helper()
	var mu sync.Mutex
	revision := 1
	current := coordinationv1.Lease{
		TypeMeta:   metav1.TypeMeta{APIVersion: "coordination.k8s.io/v1", Kind: "Lease"},
		ObjectMeta: metav1.ObjectMeta{Name: BotLeaseName, Namespace: "staging", ResourceVersion: "1"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/coordination.k8s.io/v1/namespaces/staging/leases/"+BotLeaseName {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodGet:
		case http.MethodPut:
			var requested coordinationv1.Lease
			if json.NewDecoder(r.Body).Decode(&requested) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if requested.ResourceVersion != current.ResourceVersion {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonConflict, Code: 409})
				return
			}
			current = requested
			revision++
			current.ResourceVersion = strconv.Itoa(revision)
		default:
			t.Errorf("unexpected Lease verb %s", r.Method)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(current)
	}))
	t.Cleanup(server.Close)
	client, err := coordination.NewForConfig(&rest.Config{Host: server.URL, Timeout: time.Second, QPS: -1,
		ContentConfig: rest.ContentConfig{ContentType: "application/json", AcceptContentTypes: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestLeaseReplacementPreservesNewHolder(t *testing.T) {
	client := leaseTestAPI(t)
	lease := &Lease{client: client, namespace: "staging", identity: "pod-old", duration: 2 * time.Second,
		deadline: 300 * time.Millisecond, retry: 50 * time.Millisecond}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	started, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		_ = lease.elect(ctx, func(ctx context.Context, verify func(context.Context) error) error {
			close(started)
			for ctx.Err() == nil {
				if err := verify(ctx); err != nil {
					return err
				}
				sleepContext(ctx, 20*time.Millisecond)
			}
			return ctx.Err()
		})
	}()
	receive(t, ctx, started)

	leases := client.Leases("staging")
	replacement := "pod-new"
	for {
		current, err := leases.Get(ctx, BotLeaseName, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		current.Spec.HolderIdentity = &replacement
		if _, err := leases.Update(ctx, current, metav1.UpdateOptions{}); !apierrors.IsConflict(err) {
			if err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	receive(t, ctx, stopped)
	current, err := leases.Get(ctx, BotLeaseName, metav1.GetOptions{})
	if err != nil || current.Spec.HolderIdentity == nil || *current.Spec.HolderIdentity != replacement {
		t.Fatalf("old holder overwrote replacement: %v", err)
	}
}
