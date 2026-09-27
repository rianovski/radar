package k8s

import (
	"context"
	"log"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// ClientFromContext returns a typed client scoped to the user on the context.
// When a user is attached (auth enabled), returns an impersonated client so
// K8s RBAC applies to the caller. When no user is attached (auth disabled
// or local-binary path), returns the shared ServiceAccount client.
//
// Returns nil if impersonation is required but fails — callers must handle
// nil rather than falling back to the SA client, which would silently
// escalate the user's privileges.
//
// When ctx carries a ClusterTarget (a per-user pool context), the client
// targets that cluster instead of the process-global one.
//
// Used by code paths that don't have access to *http.Request (e.g. MCP
// tools, which only receive ctx). REST handlers should prefer
// Server.getClientForRequest for consistency.
func ClientFromContext(ctx context.Context) kubernetes.Interface {
	target := ClusterTargetFromContext(ctx)
	if user := pkgauth.UserFromContext(ctx); user != nil {
		var client kubernetes.Interface
		var err error
		if target != nil {
			client, err = pkgauth.ImpersonatedClient(target.Config, user.Username, user.Groups)
		} else {
			client, err = ImpersonatedClient(user.Username, user.Groups)
		}
		if err != nil {
			log.Printf("[auth] Impersonation failed for %s: %v", user.Username, err)
			return nil
		}
		return client
	}
	if target != nil {
		return target.Client
	}
	// Guard against the typed-nil trap: GetClient returns *Clientset, which
	// can be nil before the K8s connection is established. Assigning it to
	// kubernetes.Interface would produce a non-nil interface wrapping a nil
	// pointer, and callers' `if client == nil` checks would slip through.
	if c := GetClient(); c != nil {
		return c
	}
	return nil
}

// DynamicClientFromContext is the dynamic-client analog of ClientFromContext.
// Same nil-on-impersonation-failure contract.
func DynamicClientFromContext(ctx context.Context) dynamic.Interface {
	target := ClusterTargetFromContext(ctx)
	if user := pkgauth.UserFromContext(ctx); user != nil {
		var client dynamic.Interface
		var err error
		if target != nil {
			client, err = pkgauth.ImpersonatedDynamicClient(target.Config, user.Username, user.Groups)
		} else {
			client, err = ImpersonatedDynamicClient(user.Username, user.Groups)
		}
		if err != nil {
			log.Printf("[auth] Impersonation failed for %s: %v", user.Username, err)
			return nil
		}
		return client
	}
	if target != nil {
		return target.Dynamic
	}
	// Same typed-nil guard as ClientFromContext.
	if c := GetDynamicClient(); c != nil {
		return c
	}
	return nil
}

// ConfigFromContext is the REST-config analog of ClientFromContext.
// Returns nil on impersonation failure (same fail-closed contract).
func ConfigFromContext(ctx context.Context) *rest.Config {
	target := ClusterTargetFromContext(ctx)
	if user := pkgauth.UserFromContext(ctx); user != nil {
		if target != nil {
			return pkgauth.ImpersonatedConfig(target.Config, user.Username, user.Groups)
		}
		cfg, err := ImpersonatedConfig(user.Username, user.Groups)
		if err != nil {
			log.Printf("[auth] Impersonation failed for %s: %v", user.Username, err)
			return nil
		}
		return cfg
	}
	if target != nil {
		return target.Config
	}
	return GetConfig()
}

// ServiceClientFromContext returns Radar's own (non-impersonated) client for
// the cluster ctx is bound to — the client SubjectAccessReviews are sent with.
// Nil when that cluster has no client yet.
func ServiceClientFromContext(ctx context.Context) kubernetes.Interface {
	if target := ClusterTargetFromContext(ctx); target != nil {
		return target.Client
	}
	if c := GetClient(); c != nil {
		return c
	}
	return nil
}
