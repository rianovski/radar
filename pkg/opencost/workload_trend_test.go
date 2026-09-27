package opencost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/prom"
)

func TestComputeWorkloadCostTrendFromProm_DeploymentUsesOwnerMetrics(t *testing.T) {
	var query string
	client := workloadTrendProm(t, func(q string) string {
		query = q
		return matrixBody([]namespaceSeries{
			{"", []dpoint{{1700000000, 2}, {1700003600, 3}}},
		})
	})

	got := ComputeWorkloadCostTrendFromProm(context.Background(), client, WorkloadTrendOptions{
		Range:     "24h",
		Namespace: "default",
		Kind:      "Deployment",
		Name:      "checkout",
	})
	if !got.Available {
		t.Fatalf("expected Available=true, got %+v", got)
	}
	if !strings.Contains(query, `kube_pod_owner{namespace="default", owner_kind="ReplicaSet", owner_is_controller="true"}`) {
		t.Fatalf("deployment query does not join pod owners through ReplicaSets:\n%s", query)
	}
	if !strings.Contains(query, `kube_replicaset_owner{namespace="default", owner_kind="Deployment", owner_name="checkout", owner_is_controller="true"}`) {
		t.Fatalf("deployment query does not join ReplicaSets to target Deployment:\n%s", query)
	}
	if !strings.Contains(query, `max by (namespace, pod, replicaset)`) {
		t.Fatalf("deployment query must dedupe duplicate pod owner series before joining:\n%s", query)
	}
	if !strings.Contains(query, `max by (namespace, replicaset)`) {
		t.Fatalf("deployment query must dedupe duplicate replicaset owner series before joining:\n%s", query)
	}
	if strings.Contains(query, "kube_pod_labels") {
		t.Fatalf("deployment query must not depend on kube_pod_labels allowlists:\n%s", query)
	}
	if got.WindowTotalCost != 3 {
		t.Fatalf("WindowTotalCost = %v, want 3", got.WindowTotalCost)
	}
}

func TestComputeWorkloadCostTrendFromProm_StatefulSetUsesDirectPodOwner(t *testing.T) {
	var query string
	client := workloadTrendProm(t, func(q string) string {
		query = q
		return matrixBody([]namespaceSeries{
			{"", []dpoint{{1700000000, 1}, {1700003600, 1}}},
		})
	})

	got := ComputeWorkloadCostTrendFromProm(context.Background(), client, WorkloadTrendOptions{
		Range:     "6h",
		Namespace: "db",
		Kind:      "StatefulSet",
		Name:      "postgres",
	})
	if !got.Available {
		t.Fatalf("expected Available=true, got %+v", got)
	}
	if !strings.Contains(query, `kube_pod_owner{namespace="db", owner_kind="StatefulSet", owner_name="postgres", owner_is_controller="true"}`) {
		t.Fatalf("statefulset query does not use direct pod owner:\n%s", query)
	}
	if !strings.Contains(query, `max by (namespace, pod, owner_kind, owner_name)`) {
		t.Fatalf("statefulset query must dedupe duplicate pod owner series before joining:\n%s", query)
	}
	if strings.Contains(query, "kube_replicaset_owner") {
		t.Fatalf("statefulset query should not use ReplicaSet owner join:\n%s", query)
	}
}

func TestComputeWorkloadCostTrendFromProm_NoSeriesReturnsNoMetrics(t *testing.T) {
	client := workloadTrendProm(t, func(string) string {
		return `{"status":"success","data":{"resultType":"matrix","result":[]}}`
	})

	got := ComputeWorkloadCostTrendFromProm(context.Background(), client, WorkloadTrendOptions{
		Range:     "7d",
		Namespace: "default",
		Kind:      "DaemonSet",
		Name:      "agent",
	})
	if got.Available {
		t.Fatalf("expected unavailable response, got %+v", got)
	}
	if got.Reason != ReasonNoMetrics {
		t.Fatalf("Reason = %q, want %q", got.Reason, ReasonNoMetrics)
	}
}

func workloadTrendProm(t *testing.T, bodyForQuery func(string) string) *prom.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bodyForQuery(r.URL.Query().Get("query"))))
	}))
	t.Cleanup(srv.Close)
	return prom.NewClient(prom.NewHTTPTransport(srv.URL, "", nil))
}
