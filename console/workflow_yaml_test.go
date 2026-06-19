package main

import (
	"encoding/json"
	"testing"
)

func TestWorkflowYamlRoundtrip(t *testing.T) {
	// Spec §5 worked example: claim-intake → eligibility-check → payout-calculation
	original := WorkflowJSON{
		SchemaVersion: "1.0",
		WorkflowSlug:  "claims-end-to-end",
		Title:         "Health Insurance Claim",
		EntryNode:     "n1",
		ExitNode:      "n3",
		Nodes: []WorkflowNode{
			{ID: "n1", CESlug: "insurance-claims-workflow/claim-intake", Position: WorkflowPosition{X: 80, Y: 200}},
			{ID: "n2", CESlug: "insurance-claims-workflow/eligibility-check", Position: WorkflowPosition{X: 360, Y: 200}},
			{ID: "n3", CESlug: "insurance-claims-workflow/payout-calculation", Position: WorkflowPosition{X: 640, Y: 200}},
		},
		Edges: []WorkflowEdge{
			{From: "n1.output", To: "n2.input"},
			{From: "n2.output", To: "n3.input"},
		},
	}

	drawflowBytes, err := CanonicalToDrawflow(original)
	if err != nil {
		t.Fatalf("CanonicalToDrawflow: %v", err)
	}

	got, err := DrawflowToCanonical(drawflowBytes, original.WorkflowSlug, original.Title)
	if err != nil {
		t.Fatalf("DrawflowToCanonical: %v", err)
	}

	// Ignore CreatedAt drift between original (empty) and round-tripped (now).
	got.CreatedAt = ""

	if len(got.Nodes) != len(original.Nodes) {
		t.Fatalf("node count: want %d, got %d", len(original.Nodes), len(got.Nodes))
	}
	for i, n := range original.Nodes {
		gn := got.Nodes[i]
		if gn.ID != n.ID || gn.CESlug != n.CESlug ||
			gn.Position.X != n.Position.X || gn.Position.Y != n.Position.Y {
			t.Fatalf("node[%d] mismatch: want %+v, got %+v", i, n, gn)
		}
	}

	if len(got.Edges) != len(original.Edges) {
		t.Fatalf("edge count: want %d, got %d", len(original.Edges), len(got.Edges))
	}
	for i, e := range original.Edges {
		ge := got.Edges[i]
		if ge.From != e.From || ge.To != e.To {
			t.Fatalf("edge[%d] mismatch: want %+v, got %+v", i, e, ge)
		}
	}

	if got.EntryNode != "n1" {
		t.Fatalf("entry_node: want n1, got %q", got.EntryNode)
	}
	if got.ExitNode != "n3" {
		t.Fatalf("exit_node: want n3, got %q", got.ExitNode)
	}
}

func TestTopoSortLinear(t *testing.T) {
	wf := WorkflowJSON{
		Nodes: []WorkflowNode{{ID: "n1"}, {ID: "n2"}, {ID: "n3"}},
		Edges: []WorkflowEdge{
			{From: "n1.output", To: "n2.input"},
			{From: "n2.output", To: "n3.input"},
		},
	}
	order, err := topoSort(wf)
	if err != nil {
		t.Fatalf("topoSort: %v", err)
	}
	want := []string{"n1", "n2", "n3"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order[%d]: want %s, got %s (full=%v)", i, want[i], order[i], order)
		}
	}
}

func TestTopoSortCycleDetected(t *testing.T) {
	wf := WorkflowJSON{
		Nodes: []WorkflowNode{{ID: "a"}, {ID: "b"}},
		Edges: []WorkflowEdge{
			{From: "a.output", To: "b.input"},
			{From: "b.output", To: "a.input"},
		},
	}
	if _, err := topoSort(wf); err == nil {
		t.Fatalf("topoSort: expected cycle error, got nil")
	}
}

func TestDrawflowExportFormatStable(t *testing.T) {
	// Verify the produced Drawflow export is valid JSON and has the expected shape.
	wf := WorkflowJSON{
		Nodes: []WorkflowNode{
			{ID: "n1", CESlug: "a/b", Position: WorkflowPosition{X: 10, Y: 20}},
		},
	}
	out, err := CanonicalToDrawflow(wf)
	if err != nil {
		t.Fatalf("CanonicalToDrawflow: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if _, ok := parsed["drawflow"]; !ok {
		t.Fatalf("missing top-level drawflow key: %s", out)
	}
}
