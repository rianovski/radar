package mcp

import (
	"context"

	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/k8s"
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
