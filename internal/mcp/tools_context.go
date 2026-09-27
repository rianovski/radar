package mcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/k8s"
)

// registerContextTools adds list_contexts and switch_context. switch_context
// is a write: with a pool it moves only the requesting user's context, without
// one it reconnects the whole process (single-user / no-auth mode only).
func registerContextTools(server *mcp.Server, includeWrites bool, paramRegistry *toolParamRegistry, readOnly, writeTool *mcp.ToolAnnotations) {
	addToolWithRegistry(paramRegistry, server, &mcp.Tool{
		Name:        "list_contexts",
		Description: "List all available kubeconfig contexts. Shows which context is currently active. Use before switch_context to discover valid context names.",
		Annotations: readOnly,
	}, logToolCall("list_contexts", handleListContexts))

	if !includeWrites {
		return
	}
	addToolWithRegistry(paramRegistry, server, &mcp.Tool{
		Name:        "switch_context",
		Description: "Switch the active Kubernetes context. All subsequent tool calls will target the new cluster. Use list_contexts first to see available context names.",
		Annotations: writeTool,
	}, logToolCall("switch_context", handleSwitchContext))
}

type listContextsInput struct{}

type switchContextInput struct {
	Name string `json:"name" jsonschema:"kubeconfig context name to switch to"`
}

func handleListContexts(ctx context.Context, req *mcp.CallToolRequest, _ listContextsInput) (*mcp.CallToolResult, any, error) {
	contexts, err := k8s.GetAvailableContexts()
	if err != nil {
		return nil, nil, err
	}
	// When the pool is active, override IsCurrent to reflect the per-user
	// context rather than the global kubeconfig current context.
	if mcpPool != nil {
		userCtx := mcpPool.ContextForUser(mcpUsername(ctx))
		if userCtx != "" {
			for i := range contexts {
				contexts[i].IsCurrent = contexts[i].Name == userCtx
			}
		}
	}
	return toJSONResult(contexts)
}

func handleSwitchContext(ctx context.Context, req *mcp.CallToolRequest, input switchContextInput) (*mcp.CallToolResult, any, error) {
	if input.Name == "" {
		return nil, nil, fmt.Errorf("context name is required")
	}
	if k8s.IsInCluster() {
		return nil, nil, fmt.Errorf("cannot switch context when running in-cluster")
	}
	// Pool-based per-user switch: only affects the requesting user's context.
	if mcpPool != nil {
		username := mcpUsername(ctx)
		if err := mcpPool.Switch(ctx, username, input.Name); err != nil {
			return nil, nil, err
		}
		return toJSONResult(map[string]string{
			"status":  "ok",
			"context": input.Name,
		})
	}
	// Global switch fallback (single-user / no-auth mode).
	if err := k8s.PerformContextSwitch(input.Name); err != nil {
		k8s.SetConnectionStatus(k8s.ConnectionStatus{
			State:   k8s.StateDisconnected,
			Context: input.Name,
			Error:   err.Error(),
		})
		return nil, nil, err
	}
	k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State:       k8s.StateConnected,
		Context:     k8s.GetContextName(),
		ClusterName: k8s.GetClusterName(),
	})
	return toJSONResult(map[string]string{
		"status":  "ok",
		"context": k8s.GetContextName(),
		"cluster": k8s.GetClusterName(),
	})
}
