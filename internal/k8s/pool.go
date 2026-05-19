package k8s

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// PoolEntry groups the per-cluster resources that are created for each
// independently connected kubeconfig context.
type PoolEntry struct {
	Cache     *ResourceCache
	DynCache  *DynamicResourceCache
	Discovery *ResourceDiscovery
}

// poolRef wraps a PoolEntry with its lifecycle cancel and the set of
// usernames currently attached to it.
type poolRef struct {
	entry  PoolEntry
	cancel context.CancelFunc
	users  map[string]struct{}
}

// CachePool manages one PoolEntry per kubeconfig context with reference
// counting. When the last user leaves a context its entry is shut down so no
// watch connections remain open in the background.
//
// All methods are safe for concurrent use.
type CachePool struct {
	mu          sync.Mutex
	entries     map[string]*poolRef // contextName → ref
	userContext map[string]string   // username → contextName
	defaultCtx  string
	// onRelease is called (outside the lock) after an entry is removed.
	onRelease func(contextName string)
}

// NewCachePool creates an empty pool whose default context is defaultCtx.
// onRelease is invoked when a context's last user leaves and its entry is
// torn down — the server uses this to stop the associated SSE broadcaster.
func NewCachePool(defaultCtx string, onRelease func(contextName string)) *CachePool {
	return &CachePool{
		entries:     make(map[string]*poolRef),
		userContext: make(map[string]string),
		defaultCtx:  defaultCtx,
		onRelease:   onRelease,
	}
}

// Seed registers an already-running PoolEntry as the default context.
// Called at server startup so the initial cache is pool-managed from the start.
func (p *CachePool) Seed(contextName string, entry PoolEntry, cancel context.CancelFunc) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entries[contextName] = &poolRef{
		entry:  entry,
		cancel: cancel,
		users:  make(map[string]struct{}),
	}
}

// EntryForUser returns the PoolEntry for username's current context.
// Falls back to the default context entry if the user has not switched.
// Returns nil if no entry exists yet (still connecting).
func (p *CachePool) EntryForUser(username string) *PoolEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	ref := p.refForUserLocked(username)
	if ref == nil {
		return nil
	}
	e := ref.entry
	return &e
}

// ContextForUser returns the context name currently active for username.
func (p *CachePool) ContextForUser(username string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.userContextLocked(username)
}

// Switch lazily connects username to newContext, releasing the previous
// context if no other users remain on it.
//
// Connecting a new context is done outside the pool lock so other users are
// not blocked. A double-check under the lock handles the race where two
// goroutines build the same entry concurrently.
func (p *CachePool) Switch(ctx context.Context, username, newContext string) error {
	if IsInCluster() {
		return fmt.Errorf("cannot switch context when running in-cluster")
	}

	p.mu.Lock()
	oldContext := p.userContextLocked(username)
	// If already on the target context, nothing to do.
	if oldContext == newContext {
		p.mu.Unlock()
		return nil
	}
	// If the target entry already exists, join it directly.
	if ref, ok := p.entries[newContext]; ok {
		ref.users[username] = struct{}{}
		p.userContext[username] = newContext
		p.mu.Unlock()
		p.releaseContext(username, oldContext)
		return nil
	}
	p.mu.Unlock()

	// Build the new entry outside the lock (expensive: kubeconfig load, RBAC probes, informer start).
	log.Printf("[pool] building entry for context %q (user=%s)", newContext, username)
	entry, cancel, err := BuildEntryForContext(ctx, newContext)
	if err != nil {
		return fmt.Errorf("connect to context %q: %w", newContext, err)
	}

	p.mu.Lock()
	if existing, ok := p.entries[newContext]; ok {
		// Lost the race — use the existing entry and discard ours.
		cancel()
		existing.users[username] = struct{}{}
	} else {
		p.entries[newContext] = &poolRef{
			entry:  *entry,
			cancel: cancel,
			users:  map[string]struct{}{username: {}},
		}
	}
	p.userContext[username] = newContext
	p.mu.Unlock()

	p.releaseContext(username, oldContext)
	return nil
}

// ReleaseUser detaches username from its current context.
// If no users remain on that context it is shut down.
// Call this when the user's session ends (SSE disconnect, logout, etc.).
func (p *CachePool) ReleaseUser(username string) {
	p.mu.Lock()
	ctx := p.userContextLocked(username)
	p.mu.Unlock()
	if ctx != "" {
		p.releaseContext(username, ctx)
	}
}

// releaseContext removes username from contextName's user set.
// If the set becomes empty (and it is not the default context) the entry
// is canceled and removed so no watches linger in the background.
func (p *CachePool) releaseContext(username, contextName string) {
	if contextName == "" {
		return
	}
	p.mu.Lock()
	ref, ok := p.entries[contextName]
	if !ok {
		p.mu.Unlock()
		return
	}
	delete(ref.users, username)
	// Keep the default context alive even with zero active users so the server
	// always has a baseline connection. Non-default contexts are torn down.
	if len(ref.users) == 0 && contextName != p.defaultCtx {
		ref.cancel()
		delete(p.entries, contextName)
		delete(p.userContext, username)
		p.mu.Unlock()
		log.Printf("[pool] context %q shut down — no remaining users", contextName)
		if p.onRelease != nil {
			p.onRelease(contextName)
		}
		return
	}
	p.mu.Unlock()
}

// userContextLocked returns the context name for username.
// Caller must hold p.mu.
func (p *CachePool) userContextLocked(username string) string {
	if ctx, ok := p.userContext[username]; ok {
		return ctx
	}
	return p.defaultCtx
}

// refForUserLocked returns the poolRef for username's current context.
// Caller must hold p.mu.
func (p *CachePool) refForUserLocked(username string) *poolRef {
	return p.entries[p.userContextLocked(username)]
}

// BuildEntryForContext creates a fully independent cluster connection for the
// named kubeconfig context. It does not touch any global variables. The
// returned cancel function shuts down all informers when called.
func BuildEntryForContext(ctx context.Context, contextName string) (*PoolEntry, context.CancelFunc, error) {
	restCfg, defaultNS, err := RestConfigForContext(contextName)
	if err != nil {
		return nil, nil, err
	}

	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("create clientset for %q: %w", contextName, err)
	}
	discClient, err := discovery.NewDiscoveryClientForConfig(restCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("create discovery client for %q: %w", contextName, err)
	}
	dynClient, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("create dynamic client for %q: %w", contextName, err)
	}

	// Lifecycle context: canceled when this pool entry is released.
	_, cancel := context.WithCancel(context.Background())

	// RBAC probes — authoritative for which informers to start.
	probeCtx, probeCancel := context.WithTimeout(ctx, 20*time.Second)
	permResult := CheckPermissionsForDynClient(probeCtx, dynClient, defaultNS)
	probeCancel()

	scopes := permResult.Scopes
	if scopes == nil {
		scopes = map[string]k8score.ResourceScope{}
	}

	// Typed resource cache. Use a pointer that the OnEventChange closure
	// captures by reference so it resolves after construction.
	var typedCache *ResourceCache

	coreCfg := k8score.CacheConfig{
		Client:              client,
		ResourceScopes:      scopes,
		DeferredTypes:       deferredResources,
		DebugEvents:         DebugEvents,
		TimingLogger:        func(string, ...any) {}, // silent for pool entries
		PatienceWindow:      firstPaintPatience,
		MinimalSet:          minimalFirstPaintSet,
		SyncTimeout:         firstPaintBackstop,
		DeferredSyncTimeout: 3 * time.Minute,

		OnChange: func(change k8score.ResourceChange, obj, oldObj any) {
			recordToTimelineStore(change.Kind, change.Namespace, change.Name, change.UID, change.Operation, oldObj, obj)
		},
		OnEventChange: func(obj any, op string) {
			if op == "delete" {
				return
			}
			// Owner lookup uses typedCache (set after construction).
			recordK8sEventToTimelineWithCache(obj, typedCache)
		},
		OnDrop: func(kind, ns, name, reason, op string) {
			timeline.RecordDrop(kind, ns, name, reason, op)
		},
		ComputeDiff:     func(kind string, oldObj, newObj any) *k8score.DiffInfo { return ComputeDiff(kind, oldObj, newObj) },
		IsNoisyResource: isNoisyResource,
	}

	core, err := k8score.NewResourceCache(coreCfg)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("create resource cache for %q: %w", contextName, err)
	}

	typedCache = &ResourceCache{ResourceCache: core, secretsEnabled: scopes["secrets"].Enabled}

	// API resource discovery — async, non-blocking.
	coreDisc, err := k8score.NewResourceDiscovery(discClient)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("create discovery for %q: %w", contextName, err)
	}
	disc := &ResourceDiscovery{ResourceDiscovery: coreDisc}

	// Dynamic (CRD) cache — forwards its changes through the typed cache's
	// raw changes channel so the SSE broadcaster sees everything on one stream.
	nsFallback := ""
	if permResult.NamespaceScoped && permResult.Namespace != "" {
		nsFallback = permResult.Namespace
	}
	dynCore, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{
		DynamicClient:     dynClient,
		Discovery:         coreDisc,
		Changes:           core.ChangesRaw(),
		NamespaceFallback: nsFallback,
		DebugEvents:       DebugEvents,
		OnChange: func(change k8score.ResourceChange, obj, oldObj any) {
			if u := extractUnstructured(obj); u != nil {
				recordToTimelineStore(change.Kind, change.Namespace, change.Name, change.UID, change.Operation, oldObj, obj)
			}
		},
		OnDrop: func(kind, ns, name, reason, op string) { timeline.RecordDrop(kind, ns, name, reason, op) },
		ComputeDiff: func(kind string, oldObj, newObj any) *k8score.DiffInfo {
			return ComputeDiff(kind, oldObj, newObj)
		},
	})
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("create dynamic cache for %q: %w", contextName, err)
	}

	entry := &PoolEntry{
		Cache:     typedCache,
		DynCache:  &DynamicResourceCache{DynamicResourceCache: dynCore},
		Discovery: disc,
	}
	return entry, cancel, nil
}
