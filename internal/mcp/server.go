package mcp

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/version"
)

// NewHandler creates the MCP server, registers all tools and resources,
// and returns an http.Handler to mount on chi.
// pool enables per-user context isolation; pass nil for single-user/no-auth mode.
func NewHandler(pool *k8s.CachePool) http.Handler {
	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    "radar",
			Version: version.Current,
		},
		nil,
	)

	registerTools(server, pool)
	registerResources(server)

	handler := mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)

	// go-sdk v1.6 removed the implicit cross-origin protection default;
	// wrap the handler so a malicious page can't drive the local MCP server.
	return http.NewCrossOriginProtection().Handler(handler)
}
