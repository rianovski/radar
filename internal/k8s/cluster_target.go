package k8s

import (
	"context"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// ClusterTarget is the kubeconfig context a request is bound to when it is
// not the process-global one — a per-user pool entry. Request-scoped client
// helpers (ClientFromContext and friends) build on it instead of the globals,
// so every call site that already threads ctx follows the user's context.
type ClusterTarget struct {
	Config      *rest.Config
	ContextName string
	Client      kubernetes.Interface
	Dynamic     dynamic.Interface
}

type clusterTargetKey struct{}

// WithClusterTarget binds ctx to t. A nil t leaves ctx unbound (global cluster).
func WithClusterTarget(ctx context.Context, t *ClusterTarget) context.Context {
	if t == nil {
		return ctx
	}
	return context.WithValue(ctx, clusterTargetKey{}, t)
}

// ClusterTargetFromContext returns the cluster ctx is bound to, or nil for the
// process-global cluster.
func ClusterTargetFromContext(ctx context.Context) *ClusterTarget {
	if ctx == nil {
		return nil
	}
	t, _ := ctx.Value(clusterTargetKey{}).(*ClusterTarget)
	return t
}

// Target returns the ClusterTarget for this entry. Nil when the entry has no
// rest.Config (not connected).
func (e *PoolEntry) Target() *ClusterTarget {
	if e == nil || e.RestConfig == nil {
		return nil
	}
	return &ClusterTarget{
		Config:      e.RestConfig,
		ContextName: e.ContextName,
		Client:      e.Client,
		Dynamic:     e.Dynamic,
	}
}
