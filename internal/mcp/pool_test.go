package mcp

import (
	"context"
	"testing"

	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestMCPPermScopeSeparatesContexts(t *testing.T) {
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
	prev := mcpPool
	SetPool(pool)
	t.Cleanup(func() { mcpPool = prev })

	key, got := mcpPermScope("alice")
	if key != "alice\x01other" || got != client {
		t.Errorf("switched user should get a context-qualified key and that context's client, got %q", key)
	}
	if key, _ := mcpPermScope("bob"); key != "bob" {
		t.Errorf("default-context user keeps the plain key, got %q", key)
	}
}
