package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
)

func newProxyTestRouter(s *Server) http.Handler {
	r := chi.NewRouter()
	r.HandleFunc("/api/portforwards/{id}/proxy", s.handlePortForwardProxyRoot)
	r.HandleFunc("/api/portforwards/{id}/proxy/*", s.handlePortForwardProxy)
	return r
}

func withProxySession(t *testing.T, id string, upstream *httptest.Server) {
	t.Helper()
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	pfManager.mu.Lock()
	pfManager.sessions[id] = &PortForwardSession{ID: id, LocalPort: port, Status: "running"}
	pfManager.mu.Unlock()
	t.Cleanup(func() {
		pfManager.mu.Lock()
		delete(pfManager.sessions, id)
		pfManager.mu.Unlock()
	})
}

func TestPortForwardProxy_StripsPrefixAndForwardsRequest(t *testing.T) {
	var got struct {
		path, query, prefix, method, body string
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.query = r.URL.RawQuery
		got.prefix = r.Header.Get("X-Forwarded-Prefix")
		got.method = r.Method
		b := make([]byte, 64)
		n, _ := r.Body.Read(b)
		got.body = string(b[:n])
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte("hello"))
	}))
	defer upstream.Close()
	withProxySession(t, "pf-proxy-1", upstream)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/portforwards/pf-proxy-1/proxy/app/index.html?a=1", nil)
	req.Body = http.NoBody
	newProxyTestRouter(&Server{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusTeapot, rec.Body.String())
	}
	if rec.Body.String() != "hello" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "hello")
	}
	if got.path != "/app/index.html" {
		t.Errorf("upstream path = %q, want /app/index.html", got.path)
	}
	if got.query != "a=1" {
		t.Errorf("upstream query = %q, want a=1", got.query)
	}
	if got.prefix != "/api/portforwards/pf-proxy-1/proxy" {
		t.Errorf("X-Forwarded-Prefix = %q", got.prefix)
	}
	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
}

func TestPortForwardProxy_RewritesAbsoluteRedirects(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login?next=1", http.StatusFound)
	}))
	defer upstream.Close()
	withProxySession(t, "pf-proxy-2", upstream)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/portforwards/pf-proxy-2/proxy/", nil)
	newProxyTestRouter(&Server{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/api/portforwards/pf-proxy-2/proxy/login?next=1" {
		t.Errorf("Location = %q", loc)
	}
}

func TestPortForwardProxy_UnknownOrStoppedSessionIs404(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	withProxySession(t, "pf-proxy-3", upstream)
	pfManager.mu.Lock()
	pfManager.sessions["pf-proxy-3"].Status = "stopped"
	pfManager.mu.Unlock()

	for _, id := range []string{"pf-proxy-3", "pf-does-not-exist"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/portforwards/"+id+"/proxy/", nil)
		newProxyTestRouter(&Server{}).ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", id, rec.Code)
		}
		var body map[string]string
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body["error"] == "" {
			t.Errorf("%s: expected JSON error body, got %q", id, rec.Body.String())
		}
	}
}

func TestPortForwardProxy_RootRedirectsToTrailingSlash(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/portforwards/pf-x/proxy?x=1", nil)
	newProxyTestRouter(&Server{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/api/portforwards/pf-x/proxy/?x=1" {
		t.Errorf("Location = %q", loc)
	}
}
