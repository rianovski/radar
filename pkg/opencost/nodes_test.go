package opencost

import (
	"context"
	"encoding/json"
	"testing"
)

func TestComputeNodeCosts_DedupesTotalsByNodeAndKeepsMetadata(t *testing.T) {
	client := scriptedProm(t, []scriptedCase{
		{
			contains: `max by (node) (node_total_hourly_cost)`,
			body: nodeVectorBody([]nodeSample{
				{labels: map[string]string{"node": "node-a"}, value: 0.42},
			}),
		},
		{
			contains: `max by (node, instance_type, region) (node_total_hourly_cost)`,
			body: nodeVectorBody([]nodeSample{
				{labels: map[string]string{"node": "node-a", "instance_type": "e2-standard-4", "region": "us-east1"}, value: 0.42},
				{labels: map[string]string{"node": "node-a", "instance_type": "", "region": "us-east1"}, value: 0.41},
			}),
		},
	})

	got := ComputeNodeCosts(context.Background(), client)
	if !got.Available {
		t.Fatalf("node costs unavailable: %+v", got)
	}
	if len(got.Nodes) != 1 {
		t.Fatalf("len(Nodes)=%d, want 1: %+v", len(got.Nodes), got.Nodes)
	}
	node := got.Nodes[0]
	if node.Name != "node-a" || node.HourlyCost != 0.42 {
		t.Fatalf("node cost mismatch: %+v", node)
	}
	if node.InstanceType != "e2-standard-4" || node.Region != "us-east1" {
		t.Fatalf("node metadata mismatch: %+v", node)
	}
}

type nodeSample struct {
	labels map[string]string
	value  float64
}

func nodeVectorBody(samples []nodeSample) string {
	type result struct {
		Metric map[string]string `json:"metric"`
		Value  []interface{}     `json:"value"`
	}
	body := struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string   `json:"resultType"`
			Result     []result `json:"result"`
		} `json:"data"`
	}{Status: "success"}
	body.Data.ResultType = "vector"
	for _, sample := range samples {
		body.Data.Result = append(body.Data.Result, result{
			Metric: sample.labels,
			Value:  []interface{}{1700000000.0, formatFloat(sample.value)},
		})
	}
	b, _ := json.Marshal(body)
	return string(b)
}
