package mcp

import (
	"context"

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
