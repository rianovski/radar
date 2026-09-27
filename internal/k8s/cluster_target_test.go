package k8s

import (
	"context"
	"testing"

	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

func TestClusterTargetRoutesRequestClients(t *testing.T) {
	target := &ClusterTarget{
		Config:      &rest.Config{Host: "https://other.example"},
		ContextName: "other",
		Client:      fake.NewSimpleClientset(),
	}
	ctx := WithClusterTarget(context.Background(), target)

	if got := ClusterTargetFromContext(ctx); got != target {
		t.Fatalf("target not carried on ctx")
	}
	if got := ServiceClientFromContext(ctx); got != target.Client {
		t.Errorf("ServiceClientFromContext should return the target's client")
	}
	if got := ClientFromContext(ctx); got != target.Client {
		t.Errorf("ClientFromContext without a user should return the target's client")
	}
	if got := ConfigFromContext(ctx); got != target.Config {
		t.Errorf("ConfigFromContext without a user should return the target's config")
	}

	userCtx := pkgauth.ContextWithUser(ctx, &pkgauth.User{Username: "alice", Groups: []string{"dev"}})
	cfg := ConfigFromContext(userCtx)
	if cfg == nil || cfg.Host != "https://other.example" {
		t.Fatalf("impersonated config should target the bound cluster, got %+v", cfg)
	}
	if cfg.Impersonate.UserName != "alice" {
		t.Errorf("impersonated config should act as the user, got %q", cfg.Impersonate.UserName)
	}
	if target.Config.Impersonate.UserName != "" {
		t.Errorf("impersonation must not mutate the shared target config")
	}
	if ClientFromContext(userCtx) == nil {
		t.Errorf("ClientFromContext with a user should build an impersonated client for the target")
	}
}

func TestWithClusterTargetNilLeavesContextUnbound(t *testing.T) {
	ctx := WithClusterTarget(context.Background(), nil)
	if ClusterTargetFromContext(ctx) != nil {
		t.Fatal("nil target must leave ctx unbound")
	}
}

func TestPoolEntryTarget(t *testing.T) {
	var nilEntry *PoolEntry
	if nilEntry.Target() != nil {
		t.Error("nil entry has no target")
	}
	if (&PoolEntry{ContextName: "x"}).Target() != nil {
		t.Error("entry without a rest.Config has no target")
	}
	e := &PoolEntry{RestConfig: &rest.Config{Host: "h"}, ContextName: "x"}
	if got := e.Target(); got == nil || got.ContextName != "x" || got.Config != e.RestConfig {
		t.Errorf("unexpected target %+v", got)
	}
}
