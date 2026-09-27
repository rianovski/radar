package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

// newSwitchedPool returns a pool where alice is on context "other" and every
// other user is on the process-global default.
func newSwitchedPool(t *testing.T) (*k8s.CachePool, *fake.Clientset) {
	t.Helper()
	t.Cleanup(k8s.SetTestLocalMode())
	client := fake.NewSimpleClientset()
	pool := k8s.NewCachePool(k8s.GetContextName(), nil)
	pool.Seed("other", k8s.PoolEntry{
		Client:      client,
		RestConfig:  &rest.Config{Host: "https://other.example"},
		ContextName: "other",
	}, func() {})
	if err := pool.Switch(context.Background(), "alice", "other"); err != nil {
		t.Fatalf("switch: %v", err)
	}
	return pool, client
}

func TestPermScopeSeparatesContexts(t *testing.T) {
	pool, client := newSwitchedPool(t)
	s := &Server{pool: pool}

	key, got := s.permScope("alice")
	if key != "alice\x01other" {
		t.Errorf("switched user should get a context-qualified key, got %q", key)
	}
	if got != client {
		t.Errorf("switched user's SARs should use that context's client")
	}

	key, _ = s.permScope("bob")
	if key != "bob" {
		t.Errorf("default-context user keeps the plain key, got %q", key)
	}
}

func TestClusterTargetMiddlewareBindsSwitchedUsers(t *testing.T) {
	pool, _ := newSwitchedPool(t)
	s := &Server{pool: pool}

	var seen *k8s.ClusterTarget
	h := s.clusterTargetMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = k8s.ClusterTargetFromContext(r.Context())
	}))

	serve := func(username string) *k8s.ClusterTarget {
		seen = nil
		req := httptest.NewRequest(http.MethodGet, "/api/pods", nil)
		req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{Username: username}))
		h.ServeHTTP(httptest.NewRecorder(), req)
		return seen
	}

	if got := serve("alice"); got == nil || got.ContextName != "other" {
		t.Fatalf("switched user should be bound to their context, got %+v", got)
	}
	if got := serve("bob"); got != nil {
		t.Fatalf("default-context user must stay unbound, got %+v", got)
	}
}
