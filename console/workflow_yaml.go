package main

// Drawflow ↔ canonical workflow.json shim.
//
// Drawflow's editor.export() emits a nested structure keyed by string-integer
// IDs. The canonical workflow.json (Docs/workflow-json-spec.md v1.0) uses
// string node IDs ("n<int>") and a flat edges[] array.
//
// This shim is the single source of truth for the translation. The frontend
// JS mirrors the same rules client-side so the canvas-save and the runner
// agree on workflow shape.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DrawflowExport mirrors editor.export()'s top-level shape.
type DrawflowExport struct {
	Drawflow DrawflowModules `json:"drawflow"`
}

type DrawflowModules map[string]DrawflowModule

type DrawflowModule struct {
	Data map[string]DrawflowNode `json:"data"`
}

type DrawflowNode struct {
	ID       int                          `json:"id"`
	Name     string                       `json:"name"`
	Data     map[string]any               `json:"data"`
	Class    string                       `json:"class"`
	HTML     string                       `json:"html"`
	TypeNode bool                         `json:"typenode"`
	Inputs   map[string]DrawflowEndpoint  `json:"inputs"`
	Outputs  map[string]DrawflowEndpoint  `json:"outputs"`
	PosX     int                          `json:"pos_x"`
	PosY     int                          `json:"pos_y"`
}

type DrawflowEndpoint struct {
	Connections []DrawflowConnection `json:"connections"`
}

// DrawflowConnection: Drawflow names confuse Go readers.
// For an OUTPUT endpoint's connections[]:
//   - Node    = target node id (string-int)
//   - Output  = target's INPUT port name (e.g. "input_1") — yes, Drawflow's
//               field name is "output" but it points to the receiving port.
type DrawflowConnection struct {
	Node   string `json:"node"`
	Output string `json:"output"`
}

// DrawflowToCanonical parses editor.export() output and returns the canonical
// workflow.json. The active module defaults to "Home" but any module name is
// accepted (Drawflow supports multi-module workspaces).
func DrawflowToCanonical(drawflowJSON []byte, workflowSlug, title string) (WorkflowJSON, error) {
	var exp DrawflowExport
	if err := json.Unmarshal(drawflowJSON, &exp); err != nil {
		return WorkflowJSON{}, fmt.Errorf("drawflow decode: %w", err)
	}
	mod, ok := exp.Drawflow["Home"]
	if !ok {
		// fall back to the first module
		for _, m := range exp.Drawflow {
			mod = m
			break
		}
	}

	wf := WorkflowJSON{
		SchemaVersion: "1.0",
		WorkflowSlug:  workflowSlug,
		Title:         title,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		Nodes:         make([]WorkflowNode, 0, len(mod.Data)),
		Edges:         make([]WorkflowEdge, 0),
	}

	// Sort node IDs for deterministic output.
	idStrings := make([]string, 0, len(mod.Data))
	for k := range mod.Data {
		idStrings = append(idStrings, k)
	}
	sort.Slice(idStrings, func(i, j int) bool {
		ai, _ := strconv.Atoi(idStrings[i])
		aj, _ := strconv.Atoi(idStrings[j])
		return ai < aj
	})

	indegree := make(map[string]int)
	outdegree := make(map[string]int)

	for _, k := range idStrings {
		n := mod.Data[k]
		canonicalID := "n" + strconv.Itoa(n.ID)
		ceSlug, _ := n.Data["ce_slug"].(string)
		node := WorkflowNode{
			ID:       canonicalID,
			CESlug:   ceSlug,
			Position: WorkflowPosition{X: n.PosX, Y: n.PosY},
		}
		wf.Nodes = append(wf.Nodes, node)
		indegree[canonicalID] = 0
		outdegree[canonicalID] = 0
	}

	// Walk OUTPUT endpoints to materialize edges.
	for _, k := range idStrings {
		n := mod.Data[k]
		sourceID := "n" + strconv.Itoa(n.ID)
		outKeys := sortedKeys(n.Outputs)
		for _, outPort := range outKeys {
			endpoint := n.Outputs[outPort]
			for _, conn := range endpoint.Connections {
				targetID := "n" + conn.Node
				edge := WorkflowEdge{
					From: sourceID + "." + canonicalizePort(outPort, "output"),
					To:   targetID + "." + canonicalizePort(conn.Output, "input"),
				}
				wf.Edges = append(wf.Edges, edge)
				outdegree[sourceID]++
				indegree[targetID]++
			}
		}
	}

	// Sort edges for determinism.
	sort.Slice(wf.Edges, func(i, j int) bool {
		if wf.Edges[i].From != wf.Edges[j].From {
			return wf.Edges[i].From < wf.Edges[j].From
		}
		return wf.Edges[i].To < wf.Edges[j].To
	})

	// Derive entry/exit nodes — exactly one each in v1 (linear).
	var entries, exits []string
	for _, n := range wf.Nodes {
		if indegree[n.ID] == 0 {
			entries = append(entries, n.ID)
		}
		if outdegree[n.ID] == 0 {
			exits = append(exits, n.ID)
		}
	}
	sortStrings(entries)
	sortStrings(exits)
	if len(entries) > 0 {
		wf.EntryNode = entries[0]
	}
	if len(exits) > 0 {
		wf.ExitNode = exits[0]
	}

	return wf, nil
}

// CanonicalToDrawflow translates a workflow.json back to Drawflow's editor.import()
// payload. Module name defaults to "Home".
func CanonicalToDrawflow(wf WorkflowJSON) ([]byte, error) {
	mod := DrawflowModule{Data: make(map[string]DrawflowNode, len(wf.Nodes))}

	// First pass: nodes.
	for _, n := range wf.Nodes {
		drawflowID, err := strconv.Atoi(strings.TrimPrefix(n.ID, "n"))
		if err != nil {
			return nil, fmt.Errorf("invalid canonical node id %q (expected n<int>): %w", n.ID, err)
		}
		mod.Data[strconv.Itoa(drawflowID)] = DrawflowNode{
			ID:       drawflowID,
			Name:     "ce-card",
			Data:     map[string]any{"ce_slug": n.CESlug},
			Class:    "ce-card",
			HTML:     "", // canvas re-renders HTML from template on import
			TypeNode: false,
			Inputs:   make(map[string]DrawflowEndpoint),
			Outputs:  make(map[string]DrawflowEndpoint),
			PosX:     n.Position.X,
			PosY:     n.Position.Y,
		}
	}

	// Second pass: edges → fill inputs/outputs endpoint lists on both sides.
	for _, e := range wf.Edges {
		fromNode, fromPort := splitPortRef(e.From) // "n1", "output" or "output_2"
		toNode, toPort := splitPortRef(e.To)
		fromInt := strings.TrimPrefix(fromNode, "n")
		toInt := strings.TrimPrefix(toNode, "n")

		drawflowOutPort := drawflowPort(fromPort, "output")
		drawflowInPort := drawflowPort(toPort, "input")

		// source node's outputs[outPort].connections += {node: toInt, output: inPort}
		src := mod.Data[fromInt]
		ep := src.Outputs[drawflowOutPort]
		ep.Connections = append(ep.Connections, DrawflowConnection{
			Node:   toInt,
			Output: drawflowInPort,
		})
		src.Outputs[drawflowOutPort] = ep
		mod.Data[fromInt] = src

		// target node's inputs[inPort].connections += {node: fromInt, output: outPort}
		dst := mod.Data[toInt]
		dep := dst.Inputs[drawflowInPort]
		dep.Connections = append(dep.Connections, DrawflowConnection{
			Node:   fromInt,
			Output: drawflowOutPort,
		})
		dst.Inputs[drawflowInPort] = dep
		mod.Data[toInt] = dst
	}

	exp := DrawflowExport{Drawflow: DrawflowModules{"Home": mod}}
	return json.Marshal(exp)
}

// canonicalizePort: "output_1" → "output", "output_2" → "output_2".
// (Single-port endpoints use the bare name; multi-port keep the index.)
func canonicalizePort(drawflowPortName, side string) string {
	want := side + "_1"
	if drawflowPortName == want {
		return side
	}
	return drawflowPortName
}

// drawflowPort: "output" → "output_1", "output_2" → "output_2".
func drawflowPort(canonicalPortName, side string) string {
	if canonicalPortName == side {
		return side + "_1"
	}
	return canonicalPortName
}

// splitPortRef: "n1.output" → ("n1", "output"); "n3.input_2" → ("n3", "input_2").
func splitPortRef(ref string) (node, port string) {
	for i := 0; i < len(ref); i++ {
		if ref[i] == '.' {
			return ref[:i], ref[i+1:]
		}
	}
	return ref, ""
}

func sortedKeys(m map[string]DrawflowEndpoint) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
