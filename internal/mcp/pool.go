package mcp

import (
	"context"

	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/search"
	"github.com/skyhook-io/radar/pkg/auth"
)

// mcpPool is set once at startup via SetPool, before any handler serves.
var mcpPool *k8s.CachePool

// SetPool enables per-user context isolation for MCP tools. Leave unset for
// single-user / no-auth mode, where tools read the process-global caches.
func SetPool(pool *k8s.CachePool) {
	mcpPool = pool
}

func mcpUsername(ctx context.Context) string {
	if user := auth.UserFromContext(ctx); user != nil {
		return user.Username
	}
	return ""
}

func mcpEntry(ctx context.Context) *k8s.PoolEntry {
	if mcpPool != nil {
		return mcpPool.EntryForUser(mcpUsername(ctx))
	}
	return nil
}

func mcpCache(ctx context.Context) *k8s.ResourceCache {
	if e := mcpEntry(ctx); e != nil {
		return e.Cache
	}
	return k8s.GetResourceCache()
}

func mcpDynCache(ctx context.Context) *k8s.DynamicResourceCache {
	if e := mcpEntry(ctx); e != nil {
		return e.DynCache
	}
	return k8s.GetDynamicResourceCache()
}

func mcpDiscovery(ctx context.Context) *k8s.ResourceDiscovery {
	if e := mcpEntry(ctx); e != nil {
		return e.Discovery
	}
	return k8s.GetResourceDiscovery()
}

// mcpPermScope resolves the permission-cache key and the ServiceAccount client
// SubjectAccessReviews must use for username, qualified by the user's pool
// context so one cluster's RBAC never answers for another's. \x01 cannot
// appear in a Kubernetes username, so the qualified key can't collide.
func mcpPermScope(username string) (key string, client kubernetes.Interface) {
	if mcpPool != nil {
		if ctxName := mcpPool.ContextForUser(username); ctxName != "" && ctxName != k8s.GetContextName() {
			if e := mcpPool.EntryForContext(ctxName); e != nil && e.Client != nil {
				return username + "\x01" + ctxName, e.Client
			}
			return username + "\x01" + ctxName, nil
		}
	}
	if c := k8s.GetClient(); c != nil {
		return username, c
	}
	return username, nil
}

// mcpNonDefaultEntry is the caller's pool entry when it targets a context other
// than the process-global default, else nil.
func mcpNonDefaultEntry(ctx context.Context) *k8s.PoolEntry {
	if mcpPool == nil || mcpPool.ContextForUser(mcpUsername(ctx)) == k8s.GetContextName() {
		return nil
	}
	if e := mcpEntry(ctx); e != nil && e.ContextName != k8s.GetContextName() {
		return e
	}
	return nil
}

// mcpHelmClient returns the Helm client bound to the caller's context.
func mcpHelmClient(ctx context.Context) *helm.Client {
	c := helm.GetClient()
	if e := mcpNonDefaultEntry(ctx); e != nil {
		return c.ForContext(e.RestConfig, e.ContextName, e.Cache)
	}
	return c
}

// mcpContextName is the caller's pool context, or "" for the default one.
func mcpContextName(ctx context.Context) string {
	if e := mcpNonDefaultEntry(ctx); e != nil {
		return e.ContextName
	}
	return ""
}

// mcpIssuesProvider is the issues engine's provider over the caller's caches.
func mcpIssuesProvider(ctx context.Context) *issues.CacheProvider {
	if e := mcpNonDefaultEntry(ctx); e != nil {
		return issues.NewCacheProviderFor(e.Cache, e.DynCache, e.Discovery)
	}
	return issues.NewCacheProvider()
}

// mcpSearchProvider is the search provider over the caller's caches.
func mcpSearchProvider(ctx context.Context) *search.CacheProvider {
	if e := mcpNonDefaultEntry(ctx); e != nil {
		return search.NewCacheProviderFor(e.Cache, e.DynCache, e.Discovery)
	}
	return search.NewCacheProvider()
}
