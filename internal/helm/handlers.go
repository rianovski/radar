package helm

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/client-go/rest"
)

// IsForbiddenError checks if an error is a Kubernetes RBAC forbidden error
func IsForbiddenError(err error) bool {
	if err == nil {
		return false
	}
	errLower := strings.ToLower(err.Error())
	return strings.Contains(errLower, "forbidden") || strings.Contains(errLower, "unauthorized")
}

// userCreds pulls the auth user off the request for *AsUser helpers.
// Returns ("", nil) when no user is attached (auth disabled / local binary),
// which the *AsUser methods treat as "use the SA identity".
func userCreds(r *http.Request) (string, []string) {
	if user := auth.UserFromContext(r.Context()); user != nil {
		return user.Username, user.Groups
	}
	return "", nil
}

// Handlers provides HTTP handlers for Helm endpoints.
//
// ContextResolver is called per-request to determine which cluster the
// Helm action should target. Returning a non-nil rest.Config + a context
// name routes the action through that cluster (used when the caller is on
// a non-default pool context). Returning (nil, "") falls back to the
// process-global Helm Client, which reads k8s.GetConfig() / GetContextName().
// Wiring it from server.New keeps this package free of a pool dependency.
type Handlers struct {
	ContextResolver func(r *http.Request) (*rest.Config, string)
}

// NewHandlers creates a new Handlers instance. resolver may be nil (the
// default-context path is used unconditionally in that case).
func NewHandlers(resolver func(r *http.Request) (*rest.Config, string)) *Handlers {
	return &Handlers{ContextResolver: resolver}
}

// resolveContext returns the per-request (restConfig, contextName) pair, or
// (nil, "") when no resolver is wired or the resolver maps the request to
// the default context.
func (h *Handlers) resolveContext(r *http.Request) (*rest.Config, string) {
	if h.ContextResolver == nil {
		return nil, ""
	}
	return h.ContextResolver(r)
}

// listReleases / getRelease / getManifest / getValues / getManifestDiff
// are thin per-context dispatchers used by the read handlers. They
// either delegate to the global Client (default context) or build an
// action.Configuration against the per-request rest.Config so reads hit
// the user's switched-to cluster.
func (h *Handlers) listReleases(r *http.Request, client *Client, namespace, username string, groups []string) ([]HelmRelease, error) {
	if h.ContextResolver == nil {
		return client.ListReleasesAsUser(namespace, username, groups)
	}
	restCfg, ctxName := h.ContextResolver(r)
	if restCfg == nil {
		return client.ListReleasesAsUser(namespace, username, groups)
	}
	actionConfig, err := client.GetActionConfigForUserWith(restCfg, ctxName, namespace, username, groups)
	if err != nil {
		return nil, err
	}
	return ListReleasesWith(actionConfig, namespace, username, groups)
}

func (h *Handlers) getRelease(r *http.Request, client *Client, namespace, name, username string, groups []string) (*HelmReleaseDetail, error) {
	if h.ContextResolver == nil {
		return client.GetReleaseAsUser(namespace, name, username, groups)
	}
	restCfg, ctxName := h.ContextResolver(r)
	if restCfg == nil {
		return client.GetReleaseAsUser(namespace, name, username, groups)
	}
	actionConfig, err := client.GetActionConfigForUserWith(restCfg, ctxName, namespace, username, groups)
	if err != nil {
		return nil, err
	}
	return GetReleaseWith(actionConfig, namespace, name)
}

func (h *Handlers) getManifest(r *http.Request, client *Client, namespace, name string, revision int, username string, groups []string) (string, error) {
	if h.ContextResolver == nil {
		return client.GetManifestAsUser(namespace, name, revision, username, groups)
	}
	restCfg, ctxName := h.ContextResolver(r)
	if restCfg == nil {
		return client.GetManifestAsUser(namespace, name, revision, username, groups)
	}
	actionConfig, err := client.GetActionConfigForUserWith(restCfg, ctxName, namespace, username, groups)
	if err != nil {
		return "", err
	}
	return GetManifestWith(actionConfig, name, revision)
}

func (h *Handlers) getValues(r *http.Request, client *Client, namespace, name string, allValues bool, username string, groups []string) (*HelmValues, error) {
	if h.ContextResolver == nil {
		return client.GetValuesAsUser(namespace, name, allValues, username, groups)
	}
	restCfg, ctxName := h.ContextResolver(r)
	if restCfg == nil {
		return client.GetValuesAsUser(namespace, name, allValues, username, groups)
	}
	actionConfig, err := client.GetActionConfigForUserWith(restCfg, ctxName, namespace, username, groups)
	if err != nil {
		return nil, err
	}
	return GetValuesWith(actionConfig, name, allValues)
}

// Write dispatchers. Each one routes a mutating Helm action through the
// per-user pool context when one is resolved, falling back to the global
// Client methods (default context) otherwise. Without these, destructive
// operations like Uninstall would always target the default cluster even
// when the user is on a different one — silently destroying releases on
// the wrong cluster.

func (h *Handlers) uninstall(r *http.Request, client *Client, namespace, name string) error {
	username, groups := userCreds(r)
	restCfg, ctxName := h.resolveContext(r)
	if restCfg == nil {
		if username != "" {
			return client.UninstallAsUser(namespace, name, username, groups)
		}
		return client.Uninstall(namespace, name)
	}
	actionConfig, err := client.GetActionConfigForUserWith(restCfg, ctxName, namespace, username, groups)
	if err != nil {
		return err
	}
	return client.UninstallWith(actionConfig, name)
}

func (h *Handlers) rollback(r *http.Request, client *Client, namespace, name string, revision int) error {
	return h.rollbackWithProgress(r, client, namespace, name, revision, nil)
}

func (h *Handlers) rollbackWithProgress(r *http.Request, client *Client, namespace, name string, revision int, progressCh chan<- InstallProgress) error {
	username, groups := userCreds(r)
	restCfg, ctxName := h.resolveContext(r)
	if restCfg == nil {
		if progressCh != nil {
			return client.RollbackWithProgress(namespace, name, revision, progressCh)
		}
		if username != "" {
			return client.RollbackAsUser(namespace, name, revision, username, groups)
		}
		return client.Rollback(namespace, name, revision)
	}
	actionConfig, err := client.GetActionConfigForUserWith(restCfg, ctxName, namespace, username, groups)
	if err != nil {
		return err
	}
	// Direct rollbackWith ignores progress; per-user streaming progress is best-effort.
	return client.RollbackWith(actionConfig, name, revision)
}

func (h *Handlers) upgrade(r *http.Request, client *Client, namespace, name, targetVersion, repositoryName string, progressCh chan<- InstallProgress) error {
	username, groups := userCreds(r)
	restCfg, ctxName := h.resolveContext(r)
	if restCfg == nil {
		if progressCh != nil {
			if username != "" {
				return client.UpgradeWithProgressAsUser(namespace, name, targetVersion, repositoryName, username, groups, progressCh)
			}
			return client.UpgradeWithProgress(namespace, name, targetVersion, repositoryName, progressCh)
		}
		if username != "" {
			return client.UpgradeAsUser(namespace, name, targetVersion, repositoryName, username, groups)
		}
		return client.Upgrade(namespace, name, targetVersion, repositoryName)
	}
	actionConfig, err := client.GetActionConfigForUserWith(restCfg, ctxName, namespace, username, groups)
	if err != nil {
		return err
	}
	return client.UpgradeWith(actionConfig, name, targetVersion, repositoryName, progressCh)
}

func (h *Handlers) applyValues(r *http.Request, client *Client, namespace, name string, newValues map[string]any) error {
	username, groups := userCreds(r)
	restCfg, ctxName := h.resolveContext(r)
	if restCfg == nil {
		if username != "" {
			return client.ApplyValuesAsUser(namespace, name, newValues, username, groups)
		}
		return client.ApplyValues(namespace, name, newValues)
	}
	actionConfig, err := client.GetActionConfigForUserWith(restCfg, ctxName, namespace, username, groups)
	if err != nil {
		return err
	}
	return client.ApplyValuesWith(actionConfig, name, newValues)
}

func (h *Handlers) install(r *http.Request, client *Client, req *InstallRequest, progressCh chan<- InstallProgress) (*HelmRelease, error) {
	username, groups := userCreds(r)
	restCfg, ctxName := h.resolveContext(r)
	if restCfg == nil {
		if progressCh != nil {
			if username != "" {
				return client.InstallWithProgressAsUser(req, progressCh, username, groups)
			}
			return client.InstallWithProgress(req, progressCh)
		}
		if username != "" {
			return client.InstallAsUser(req, username, groups)
		}
		return client.Install(req)
	}
	actionConfig, err := client.GetActionConfigForUserWith(restCfg, ctxName, req.Namespace, username, groups)
	if err != nil {
		return nil, err
	}
	return client.InstallWith(actionConfig, req, progressCh)
}

func (h *Handlers) getManifestDiff(r *http.Request, client *Client, namespace, name string, rev1, rev2 int, username string, groups []string) (*ManifestDiff, error) {
	m1, err := h.getManifest(r, client, namespace, name, rev1, username, groups)
	if err != nil {
		return nil, err
	}
	m2, err := h.getManifest(r, client, namespace, name, rev2, username, groups)
	if err != nil {
		return nil, err
	}
	return &ManifestDiff{
		Revision1: rev1,
		Revision2: rev2,
		Diff:      computeDiff(m1, m2, rev1, rev2),
	}, nil
}

// RegisterRoutes registers Helm routes on the given router
func (h *Handlers) RegisterRoutes(r chi.Router) {
	r.Route("/helm", func(r chi.Router) {
		// Release management
		r.Get("/releases", h.handleListReleases)
		r.Post("/releases", h.handleInstall)
		r.Post("/releases/install-stream", h.handleInstallStream)
		r.Get("/releases/{namespace}/{name}", h.handleGetRelease)
		r.Get("/releases/{namespace}/{name}/manifest", h.handleGetManifest)
		r.Get("/releases/{namespace}/{name}/values", h.handleGetValues)
		r.Get("/releases/{namespace}/{name}/diff", h.handleGetDiff)
		r.Get("/releases/{namespace}/{name}/upgrade-info", h.handleCheckUpgrade)
		r.Get("/upgrade-check", h.handleBatchUpgradeCheck)
		// Actions (write operations)
		r.Post("/releases/{namespace}/{name}/rollback", h.handleRollback)
		r.Post("/releases/{namespace}/{name}/rollback-stream", h.handleRollbackStream)
		r.Post("/releases/{namespace}/{name}/upgrade", h.handleUpgrade)
		r.Post("/releases/{namespace}/{name}/upgrade-stream", h.handleUpgradeStream)
		r.Post("/releases/{namespace}/{name}/values/preview", h.handlePreviewValues)
		r.Put("/releases/{namespace}/{name}/values", h.handleApplyValues)
		r.Delete("/releases/{namespace}/{name}", h.handleUninstall)

		// Chart browser (local repositories)
		r.Get("/repositories", h.handleListRepositories)
		r.Post("/repositories/{name}/update", h.handleUpdateRepository)
		r.Get("/charts", h.handleSearchCharts)
		r.Get("/charts/{repo}/{chart}", h.handleGetChartDetail)
		r.Get("/charts/{repo}/{chart}/{version}", h.handleGetChartDetailVersion)

		// ArtifactHub integration
		r.Get("/artifacthub/search", h.handleArtifactHubSearch)
		r.Get("/artifacthub/charts/{repo}/{chart}", h.handleArtifactHubChart)
		r.Get("/artifacthub/charts/{repo}/{chart}/{version}", h.handleArtifactHubChartVersion)
	})
}

// handleListReleases returns all Helm releases
func (h *Handlers) handleListReleases(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := r.URL.Query().Get("namespace")

	username, groups := userCreds(r)
	releases, err := h.listReleases(r, client, namespace, username, groups)
	if err != nil {
		if IsForbiddenError(err) {
			writeError(w, http.StatusForbidden, "insufficient permissions to list Helm releases")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, releases)
}

// handleGetRelease returns details for a specific release
func (h *Handlers) handleGetRelease(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	username, groups := userCreds(r)
	release, err := h.getRelease(r, client, namespace, name, username, groups)
	if err != nil {
		if IsForbiddenError(err) {
			writeError(w, http.StatusForbidden, "insufficient permissions to get Helm release")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, release)
}

// handleGetManifest returns the rendered manifest for a release.
// Member+ only — manifests can inline literal Secret resources with
// base64-encoded data, which K8s 'view' (the default cloud:viewer
// binding) excludes.
func (h *Handlers) handleGetManifest(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "view Helm release manifests") {
		return
	}
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	// Optional revision parameter
	revision := 0
	if revStr := r.URL.Query().Get("revision"); revStr != "" {
		if rev, err := strconv.Atoi(revStr); err == nil {
			revision = rev
		}
	}

	username, groups := userCreds(r)
	manifest, err := h.getManifest(r, client, namespace, name, revision, username, groups)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Return as plain text YAML
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(manifest))
}

// handleGetValues returns the values for a release. Member+ only —
// values may contain credentials set via --set or values.yaml.
func (h *Handlers) handleGetValues(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "view Helm release values") {
		return
	}
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	allValues := r.URL.Query().Get("all") == "true"

	username, groups := userCreds(r)
	values, err := h.getValues(r, client, namespace, name, allValues, username, groups)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, values)
}

// handleGetDiff returns the diff between two revisions. Member+ only
// — same surface as GetManifest (renders both revisions).
func (h *Handlers) handleGetDiff(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "diff Helm release manifests") {
		return
	}
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	rev1Str := r.URL.Query().Get("revision1")
	rev2Str := r.URL.Query().Get("revision2")

	if rev1Str == "" || rev2Str == "" {
		writeError(w, http.StatusBadRequest, "revision1 and revision2 parameters are required")
		return
	}

	rev1, err := strconv.Atoi(rev1Str)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid revision1 parameter")
		return
	}

	rev2, err := strconv.Atoi(rev2Str)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid revision2 parameter")
		return
	}

	username, groups := userCreds(r)
	diff, err := h.getManifestDiff(r, client, namespace, name, rev1, rev2, username, groups)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, diff)
}

// handleCheckUpgrade checks if a newer version is available
func (h *Handlers) handleCheckUpgrade(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	username, groups := userCreds(r)
	info, err := client.CheckForUpgradeAsUser(namespace, name, username, groups)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, info)
}

// handleBatchUpgradeCheck checks all releases for upgrades at once
func (h *Handlers) handleBatchUpgradeCheck(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := r.URL.Query().Get("namespace")

	username, groups := userCreds(r)
	info, err := client.BatchCheckUpgradesAsUser(namespace, username, groups)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, info)
}

// handleRollback rolls back a release to a previous revision
func (h *Handlers) handleRollback(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "rollback Helm releases") {
		return
	}
	if !requireHelmWrite(w, r) {
		return
	}

	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	revStr := r.URL.Query().Get("revision")
	if revStr == "" {
		writeError(w, http.StatusBadRequest, "revision parameter is required")
		return
	}

	revision, err := strconv.Atoi(revStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid revision parameter")
		return
	}

	auth.AuditLog(r, namespace, name)
	if err := h.rollback(r, client, namespace, name, revision); err != nil {
		if IsForbiddenError(err) {
			writeError(w, http.StatusForbidden, "insufficient permissions to rollback Helm release")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, map[string]string{"status": "success", "message": "Rollback completed"})
}

// handleRollbackStream rolls back a release with SSE progress streaming
func (h *Handlers) handleRollbackStream(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "rollback Helm releases") {
		return
	}
	if !requireHelmWrite(w, r) {
		return
	}

	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	revStr := r.URL.Query().Get("revision")
	if revStr == "" {
		writeError(w, http.StatusBadRequest, "revision parameter is required")
		return
	}

	revision, err := strconv.Atoi(revStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid revision parameter")
		return
	}

	// Set up SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	progressCh := make(chan InstallProgress, 10)
	defer close(progressCh)

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- h.rollbackWithProgress(r, client, namespace, name, revision, progressCh)
	}()

	for {
		select {
		case progress, ok := <-progressCh:
			if !ok {
				return
			}
			event := map[string]any{
				"type":    "progress",
				"phase":   progress.Phase,
				"message": progress.Message,
			}
			if progress.Detail != "" {
				event["detail"] = progress.Detail
			}
			data, _ := json.Marshal(event)
			w.Write([]byte("data: " + string(data) + "\n\n"))
			flusher.Flush()

		case err := <-resultCh:
			if err != nil {
				event := map[string]any{
					"type":    "error",
					"message": err.Error(),
				}
				data, _ := json.Marshal(event)
				w.Write([]byte("data: " + string(data) + "\n\n"))
			} else {
				event := map[string]any{
					"type":    "complete",
					"message": "Rollback completed successfully",
				}
				data, _ := json.Marshal(event)
				w.Write([]byte("data: " + string(data) + "\n\n"))
			}
			flusher.Flush()
			return

		case <-r.Context().Done():
			return
		}
	}
}

// handleUninstall removes a release
func (h *Handlers) handleUninstall(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "uninstall Helm releases") {
		return
	}
	if !requireHelmWrite(w, r) {
		return
	}

	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	auth.AuditLog(r, namespace, name)
	if err := h.uninstall(r, client, namespace, name); err != nil {
		if IsForbiddenError(err) {
			writeError(w, http.StatusForbidden, "insufficient permissions to uninstall Helm release")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, map[string]string{"status": "success", "message": "Release uninstalled"})
}

// handleUpgrade upgrades a release to a new version
func (h *Handlers) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "upgrade Helm releases") {
		return
	}
	if !requireHelmWrite(w, r) {
		return
	}

	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	version := r.URL.Query().Get("version")
	if version == "" {
		writeError(w, http.StatusBadRequest, "version parameter is required")
		return
	}
	repositoryName := r.URL.Query().Get("repository")

	auth.AuditLog(r, namespace, name)
	if err := h.upgrade(r, client, namespace, name, version, repositoryName, nil); err != nil {
		if IsForbiddenError(err) {
			writeError(w, http.StatusForbidden, "insufficient permissions to upgrade Helm release")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, map[string]string{"status": "success", "message": "Upgrade completed"})
}

// handleUpgradeStream upgrades a release with SSE progress streaming
func (h *Handlers) handleUpgradeStream(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "upgrade Helm releases") {
		return
	}
	if !requireHelmWrite(w, r) {
		return
	}

	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	version := r.URL.Query().Get("version")
	if version == "" {
		writeError(w, http.StatusBadRequest, "version parameter is required")
		return
	}
	repositoryName := r.URL.Query().Get("repository")

	// Set up SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	progressCh := make(chan InstallProgress, 10)
	defer close(progressCh)

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- h.upgrade(r, client, namespace, name, version, repositoryName, progressCh)
	}()

	for {
		select {
		case progress, ok := <-progressCh:
			if !ok {
				return
			}
			event := map[string]any{
				"type":    "progress",
				"phase":   progress.Phase,
				"message": progress.Message,
			}
			if progress.Detail != "" {
				event["detail"] = progress.Detail
			}
			data, _ := json.Marshal(event)
			w.Write([]byte("data: " + string(data) + "\n\n"))
			flusher.Flush()

		case err := <-resultCh:
			if err != nil {
				event := map[string]any{
					"type":    "error",
					"message": err.Error(),
				}
				data, _ := json.Marshal(event)
				w.Write([]byte("data: " + string(data) + "\n\n"))
			} else {
				event := map[string]any{
					"type":    "complete",
					"message": "Upgrade completed successfully",
				}
				data, _ := json.Marshal(event)
				w.Write([]byte("data: " + string(data) + "\n\n"))
			}
			flusher.Flush()
			return

		case <-r.Context().Done():
			return
		}
	}
}

// handlePreviewValues previews the effect of new values on a release.
// Member+ — renders the chart with proposed values, same surface as
// GetManifest.
func (h *Handlers) handlePreviewValues(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "preview Helm release values") {
		return
	}
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	var req ApplyValuesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	preview, err := client.PreviewValuesChange(namespace, name, req.Values)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, preview)
}

// handleApplyValues applies new values to a release
func (h *Handlers) handleApplyValues(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "apply Helm release values") {
		return
	}
	if !requireHelmWrite(w, r) {
		return
	}

	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	var req ApplyValuesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	auth.AuditLog(r, namespace, name)
	if err := h.applyValues(r, client, namespace, name, req.Values); err != nil {
		if IsForbiddenError(err) {
			writeError(w, http.StatusForbidden, "insufficient permissions to apply Helm values")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, map[string]string{"status": "success", "message": "Values applied successfully"})
}

// ============================================================================
// Chart Browser Handlers
// ============================================================================

// handleListRepositories returns all configured Helm repositories
func (h *Handlers) handleListRepositories(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	repos, err := client.ListRepositories()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, repos)
}

// handleUpdateRepository updates the index for a specific repository.
//
// Deliberately NOT gated by requireCloudRole: this fetches chart
// metadata from external repos (artifacthub.io, oci://, etc.) and
// caches it on the radar pod's local filesystem. It mutates pod-local
// state, not cluster state — refresh-the-catalog rather than
// modify-the-cluster. requireHelmWrite still gates it because a future
// install/upgrade depends on a fresh repo cache, but a viewer
// triggering a repo refresh has no security or product cost beyond a
// few HTTP calls to public chart hosts.
func (h *Handlers) handleUpdateRepository(w http.ResponseWriter, r *http.Request) {
	if !requireHelmWrite(w, r) {
		return
	}

	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	repoName := chi.URLParam(r, "name")
	if repoName == "" {
		writeError(w, http.StatusBadRequest, "repository name is required")
		return
	}

	if err := client.UpdateRepository(repoName); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, map[string]string{"status": "success", "message": "Repository updated"})
}

// handleSearchCharts searches for charts across all repositories
func (h *Handlers) handleSearchCharts(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	query := r.URL.Query().Get("query")
	allVersions := r.URL.Query().Get("allVersions") == "true"

	result, err := client.SearchCharts(query, allVersions)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, result)
}

// handleGetChartDetail returns detailed info about a chart (latest version)
func (h *Handlers) handleGetChartDetail(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	repoName := chi.URLParam(r, "repo")
	chartName := chi.URLParam(r, "chart")

	detail, err := client.GetChartDetail(repoName, chartName, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, detail)
}

// handleGetChartDetailVersion returns detailed info about a specific chart version
func (h *Handlers) handleGetChartDetailVersion(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	repoName := chi.URLParam(r, "repo")
	chartName := chi.URLParam(r, "chart")
	version := chi.URLParam(r, "version")

	detail, err := client.GetChartDetail(repoName, chartName, version)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, detail)
}

// handleInstall installs a new Helm release (non-streaming version)
func (h *Handlers) handleInstall(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "install Helm releases") {
		return
	}
	if !requireHelmWrite(w, r) {
		return
	}

	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	var req InstallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// Validate required fields
	if req.ReleaseName == "" {
		writeError(w, http.StatusBadRequest, "releaseName is required")
		return
	}
	if req.Namespace == "" {
		writeError(w, http.StatusBadRequest, "namespace is required")
		return
	}
	if req.ChartName == "" {
		writeError(w, http.StatusBadRequest, "chartName is required")
		return
	}
	if req.Repository == "" {
		writeError(w, http.StatusBadRequest, "repository is required")
		return
	}

	auth.AuditLog(r, req.Namespace, req.ReleaseName)
	release, installErr := h.install(r, client, &req, nil)
	if err := installErr; err != nil {
		log.Printf("[helm] install %q/%q (chart=%q repo=%q) failed: %v", req.Namespace, req.ReleaseName, req.ChartName, req.Repository, err)
		writeInstallError(w, err)
		return
	}

	writeJSON(w, release)
}

// handleInstallStream installs a Helm release with SSE progress streaming
func (h *Handlers) handleInstallStream(w http.ResponseWriter, r *http.Request) {
	if !requireCloudRole(w, r, auth.RoleMember, "install Helm releases") {
		return
	}
	if !requireHelmWrite(w, r) {
		return
	}

	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Helm client not initialized")
		return
	}

	var req InstallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// Validate required fields
	if req.ReleaseName == "" {
		writeError(w, http.StatusBadRequest, "releaseName is required")
		return
	}
	if req.Namespace == "" {
		writeError(w, http.StatusBadRequest, "namespace is required")
		return
	}
	if req.ChartName == "" {
		writeError(w, http.StatusBadRequest, "chartName is required")
		return
	}
	if req.Repository == "" {
		writeError(w, http.StatusBadRequest, "repository is required")
		return
	}

	// Set up SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Create progress channel
	progressCh := make(chan InstallProgress, 10)
	defer close(progressCh)

	// Start install in goroutine
	auth.AuditLog(r, req.Namespace, req.ReleaseName)
	resultCh := make(chan installResult, 1)
	go func() {
		release, err := h.install(r, client, &req, progressCh)
		resultCh <- installResult{release: release, err: err}
	}()

	// Stream progress events
	for {
		select {
		case progress, ok := <-progressCh:
			if !ok {
				return
			}
			event := map[string]any{
				"type":    "progress",
				"phase":   progress.Phase,
				"message": progress.Message,
			}
			if progress.Detail != "" {
				event["detail"] = progress.Detail
			}
			data, _ := json.Marshal(event)
			w.Write([]byte("data: " + string(data) + "\n\n"))
			flusher.Flush()

		case result := <-resultCh:
			if result.err != nil {
				log.Printf("[helm] install %q/%q (chart=%q repo=%q) failed: %v", req.Namespace, req.ReleaseName, req.ChartName, req.Repository, result.err)
				data, _ := json.Marshal(installStreamErrorEvent(result.err))
				w.Write([]byte("data: " + string(data) + "\n\n"))
			} else {
				event := map[string]any{
					"type":    "complete",
					"release": result.release,
				}
				data, _ := json.Marshal(event)
				w.Write([]byte("data: " + string(data) + "\n\n"))
			}
			flusher.Flush()
			return

		case <-r.Context().Done():
			return
		}
	}
}

type installResult struct {
	release *HelmRelease
	err     error
}

// requireHelmWrite checks if the service account has Helm write permissions.
// Uses secrets/create as a sentinel check — if the service account can create
// secrets, it likely has the broad RBAC granted by rbac.helm=true.
// Returns true if the request should proceed, false if an error was written.
func requireHelmWrite(w http.ResponseWriter, r *http.Request) bool {
	caps, err := k8s.CheckCapabilities(r.Context())
	if err != nil {
		log.Printf("[helm] Failed to check capabilities for %s %s: %v", r.Method, r.URL.Path, err)
		writeError(w, http.StatusInternalServerError, "failed to check capabilities: "+err.Error())
		return false
	}
	if !caps.HelmWrite {
		log.Printf("[helm] Denied %s %s: helmWrite capability not available", r.Method, r.URL.Path)
		writeError(w, http.StatusForbidden, "Helm write operations require additional RBAC permissions. Set rbac.helm=true in the Radar Helm chart values.")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	if status >= 500 {
		errorlog.Record("helm", "error", "%s", message)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// writeErrorCode is writeError with a stable machine-readable error_code
// in the response body so the SPA + MCP clients can branch on the error
// type without parsing the human message. Used for role-gated 403s and
// any other case where the consumer wants to react differently per code.
func writeErrorCode(w http.ResponseWriter, status int, code, message string) {
	if status >= 500 {
		errorlog.Record("helm", "error", "%s", message)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"error":      message,
		"error_code": code,
	})
}

// requireCloudRole gates a handler on the caller's Cloud role tier.
// Returns true if the request should proceed.
//
// When the caller has no Cloud role (OSS deploy, or running outside
// Cloud's tunnel), CloudRole.AtLeast bypasses the gate — radar OSS
// continues to use only K8s RBAC for authorization, no Cloud-specific
// product gating. This is the same behavior as before; the gate is
// strictly additive for Cloud-attributed callers.
//
// When the caller IS Cloud-attributed and their tier is below `min`,
// returns 403 with error_code=cloud_role_insufficient so the SPA can
// render a friendly "your role doesn't allow this" message instead of
// a generic auth failure.
func requireCloudRole(w http.ResponseWriter, r *http.Request, min auth.CloudRole, opName string) bool {
	role := auth.CloudRoleFromContext(r.Context())
	if role.AtLeast(min) {
		return true
	}
	username := "unknown"
	if u := auth.UserFromContext(r.Context()); u != nil {
		username = u.Username
	}
	// All user-controlled values use %q so log-line injection via CR/LF
	// in headers or path is escaped. opName is a compile-time literal.
	log.Printf("[helm] Cloud role %q denied %s for user %q (need at least %q): %q", role, opName, username, min, r.URL.Path)
	writeErrorCode(w, http.StatusForbidden, auth.ErrCodeCloudRoleInsufficient,
		"Your Radar Cloud role ("+role.String()+") cannot "+opName+". Requires "+string(min)+" or higher.")
	return false
}

// ============================================================================
// ArtifactHub Handlers
// ============================================================================

// handleArtifactHubSearch searches for charts on ArtifactHub
func (h *Handlers) handleArtifactHubSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("query")
	if query == "" {
		query = "*" // Search all
	}

	// Parse pagination params
	offset := 0
	limit := 60
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if val, err := strconv.Atoi(offsetStr); err == nil {
			offset = val
		}
	}
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if val, err := strconv.Atoi(limitStr); err == nil && val > 0 && val <= 100 {
			limit = val
		}
	}

	// Parse filters
	official := r.URL.Query().Get("official") == "true"
	verified := r.URL.Query().Get("verified") == "true"

	// Parse sort parameter (relevance, stars, last_updated)
	sort := r.URL.Query().Get("sort")

	result, err := SearchArtifactHub(query, offset, limit, official, verified, sort)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, result)
}

// handleArtifactHubChart gets chart details from ArtifactHub (latest version)
func (h *Handlers) handleArtifactHubChart(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")
	chartName := chi.URLParam(r, "chart")

	detail, err := GetArtifactHubChart(repoName, chartName, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, detail)
}

// handleArtifactHubChartVersion gets chart details from ArtifactHub for a specific version
func (h *Handlers) handleArtifactHubChartVersion(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")
	chartName := chi.URLParam(r, "chart")
	version := chi.URLParam(r, "version")

	detail, err := GetArtifactHubChart(repoName, chartName, version)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, detail)
}
