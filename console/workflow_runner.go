package main

// Workflow DAG executor — Phase-2 / Stage-3 backend.
//
// RunWorkflow walks a workflow.json (per Docs/workflow-json-spec.md v1.0) in
// topological order. For every node it brackets the CE invocation with:
//
//   1. L1 P-check via callPCheck (audit_gate_client.go from WP-AO-25)
//      Halts on DENY before any CE is called.
//   2. CE call via HTTP loopback to POST /ce/{workflow}/{stage}/
//      (the handler is owned by Stage 2 — we only call it, never edit it).
//   3. L2 O-check via callOCheck.
//      Halts on FAILURE before the output is forwarded to the next node.
//
// Every event (start, l1, exec, l2, halt, end) is sent on the trace channel for
// SSE consumption and persisted to .gem-squared/truth-logs/{run_id}.jsonl.
//
// ¬B: this file does NOT touch console/ce_*.go or console/audit_gate_*.go —
// it only consumes their exported APIs.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WorkflowJSON mirrors Docs/workflow-json-spec.md §2.
type WorkflowJSON struct {
	SchemaVersion string         `json:"schema_version"`
	WorkflowSlug  string         `json:"workflow_slug"`
	Title         string         `json:"title"`
	CreatedAt     string         `json:"created_at,omitempty"`
	Description   string         `json:"description,omitempty"`
	EntryNode     string         `json:"entry_node"`
	ExitNode      string         `json:"exit_node"`
	Nodes         []WorkflowNode `json:"nodes"`
	Edges         []WorkflowEdge `json:"edges"`

	// WP-AO-50: per-workflow audit toggles. Default true (GEM² governance
	// ON). Setting false skips the corresponding SaaS call in runNode and
	// emits a synthetic SKIPPED phase event in its place. Lets demos run
	// end-to-end without depending on the audit-gate SaaS.
	AuditL1 *bool `json:"audit_l1,omitempty"`
	AuditL2 *bool `json:"audit_l2,omitempty"`
}

// auditL1Enabled returns whether L1 P-check should run for this workflow.
// Pointer-bool semantics: nil → ON (preserve GEM² governance default),
// explicit false → SKIP.
func (wf *WorkflowJSON) auditL1Enabled() bool {
	if wf.AuditL1 == nil {
		return true
	}
	return *wf.AuditL1
}

func (wf *WorkflowJSON) auditL2Enabled() bool {
	if wf.AuditL2 == nil {
		return true
	}
	return *wf.AuditL2
}

// WorkflowNode per spec §3.
type WorkflowNode struct {
	ID       string            `json:"id"`      // "n<int>"
	CESlug   string            `json:"ce_slug"` // "{workflow}/{stage}"
	Position WorkflowPosition  `json:"position"`
	Config   map[string]any    `json:"config,omitempty"`
}

type WorkflowPosition struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// WorkflowEdge per spec §4. v1: from = "<node_id>.output[_k]", to = "<node_id>.input[_k]".
type WorkflowEdge struct {
	From               string `json:"from"`
	To                 string `json:"to"`
	ExpectedSchemaHash string `json:"expected_schema_hash,omitempty"`
}

// RunEvent is one event in the run trace. Streamed via SSE and appended JSONL.
type RunEvent struct {
	Timestamp string          `json:"timestamp"`
	RunID     string          `json:"run_id"`
	Phase     string          `json:"phase"` // start | l1 | tool | exec | l2 | halt | end
	NodeID    string          `json:"node_id,omitempty"`
	CESlug    string          `json:"ce_slug,omitempty"`
	Verdict   string          `json:"verdict,omitempty"`
	Score     int             `json:"score,omitempty"`
	Reasons   []string        `json:"reasons,omitempty"`
	Meta      json.RawMessage `json:"meta,omitempty"`
	LatencyMs int64           `json:"latency_ms,omitempty"`
	Error     string          `json:"error,omitempty"`
	Output    any             `json:"output,omitempty"` // node's CE output, for the chain step

	// WP-AO-53 Unit 4 — tool-call trace fields. Populated when Phase=="tool".
	Tool        string `json:"tool,omitempty"`
	ToolArgs    any    `json:"tool_args,omitempty"`
	ToolSummary string `json:"tool_summary,omitempty"`

	// WP-AO-59 — phase lifecycle marker so client can animate the active row.
	// "running" = blocking call in progress (no verdict yet);
	// "completed" = blocking call finished with verdict;
	// "skipped" = audit toggle off → synthetic SKIPPED verdict;
	// "" = legacy/back-compat (treated as completed by client).
	State string `json:"state,omitempty"`
}

// RunFinalStatus is the overall outcome of one RunWorkflow call.
type RunFinalStatus string

const (
	RunSuccess     RunFinalStatus = "SUCCESS"
	RunHaltedL0    RunFinalStatus = "HALTED_L0" // WP-01 U4
	RunHaltedL1    RunFinalStatus = "HALTED_L1"
	RunHaltedL2    RunFinalStatus = "HALTED_L2"
	RunHaltedL3    RunFinalStatus = "HALTED_L3" // WP-01 U4
	RunCEError     RunFinalStatus = "CE_ERROR"
	RunCycleError  RunFinalStatus = "CYCLE_ERROR"
	RunSchemaError RunFinalStatus = "SCHEMA_ERROR"
)

// RunWorkflow executes the DAG. trace is a non-blocking send channel; if the
// consumer is slow we drop the event after 100 ms rather than block the
// executor. Truth-log JSONL is written regardless of trace consumption.
//
// loopbackBase = http://localhost:{PORT} — used to call our own /ce/{wf}/{stage}/
// authKey      = the AUTH_KEYS-valid bearer key to authenticate the CE call
//                (the canvas user's key is forwarded so /ce/ stays guarded)
func RunWorkflow(ctx context.Context, wf WorkflowJSON, input map[string]any, runID, loopbackBase, authKey string, trace chan<- RunEvent) RunFinalStatus {
	emit := func(evt RunEvent) {
		evt.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
		evt.RunID = runID
		_ = appendTruthLog(runID, evt)
		// non-blocking send to trace (drop on slow consumer)
		select {
		case trace <- evt:
		case <-time.After(100 * time.Millisecond):
		}
	}

	emit(RunEvent{Phase: "start"})

	// WP-AO-53 Unit 2 — materialize CESpec.reference_data into per-project
	// SQLite before topological execution. Idempotent across runs; failure
	// is non-fatal (workflow falls back to no-DB tool surface; LLM may still
	// answer from F-block alone).
	if wf.WorkflowSlug != "" {
		if berr := bootstrapProjectDB(ctx, wf.WorkflowSlug); berr != nil {
			log.Printf("[WORKFLOW-RUN] sqlite bootstrap warning for %s: %v", wf.WorkflowSlug, berr)
		}
	}

	order, err := topoSort(wf)
	if err != nil {
		emit(RunEvent{Phase: "halt", Error: err.Error()})
		emit(RunEvent{Phase: "end", Error: err.Error()})
		return RunCycleError
	}

	nodeByID := make(map[string]WorkflowNode, len(wf.Nodes))
	for _, n := range wf.Nodes {
		nodeByID[n.ID] = n
	}

	// payload threads through the linear chain. v1: linear only, so each
	// node consumes the previous node's output. Input to first node = user input.
	var payload any = input

	for _, nodeID := range order {
		node := nodeByID[nodeID]

		select {
		case <-ctx.Done():
			emit(RunEvent{Phase: "halt", NodeID: nodeID, Error: "context canceled"})
			emit(RunEvent{Phase: "end", Error: "canceled"})
			return RunHaltedL1 // treat as halt-before-execution
		default:
		}

		// WP-01 2026-05-19 — per-node judge-injection.
		// L0 of node N only fires on node N's input when the workflow REACHES
		// node N. If THIS node has SampleI != OriginalSampleI, override the
		// chain input with the edited sample for THIS node only. Nodes that
		// weren't edited use prev-output as normal.
		if wfSlug, stageSlug := splitCESlug(node.CESlug); wfSlug != "" && stageSlug != "" {
			if spec, lerr := loadCESpec(wfSlug, stageSlug); lerr == nil && spec != nil &&
				spec.SampleI != "" && spec.OriginalSampleI != "" && spec.SampleI != spec.OriginalSampleI {
				var edited any
				if jerr := json.Unmarshal([]byte(spec.SampleI), &edited); jerr == nil {
					log.Printf("[WORKFLOW-RUN] judge-edit at %s — overriding chain input for this node only", node.CESlug)
					payload = edited
				}
			}
		}

		out, halt, phase, err := runNode(ctx, node, payload, runID, loopbackBase, authKey, wf.WorkflowSlug, wf.auditL1Enabled(), wf.auditL2Enabled(), emit)
		if err != nil {
			emit(RunEvent{Phase: "halt", NodeID: nodeID, Error: err.Error()})
			emit(RunEvent{Phase: "end", Error: err.Error()})
			switch phase {
			case "l0":
				return RunHaltedL0
			case "l1":
				return RunHaltedL1
			case "l2":
				return RunHaltedL2
			case "l3":
				return RunHaltedL3
			default:
				return RunCEError
			}
		}
		if halt {
			emit(RunEvent{Phase: "end"})
			switch phase {
			case "l0":
				return RunHaltedL0
			case "l1":
				return RunHaltedL1
			case "l2":
				return RunHaltedL2
			case "l3":
				return RunHaltedL3
			default:
				return RunCEError
			}
		}
		payload = out
	}

	emit(RunEvent{Phase: "end", Output: payload})
	return RunSuccess
}

// topoSort implements Kahn's algorithm over WorkflowJSON edges.
// Stable order: when multiple nodes have indegree 0, ties broken by node ID.
func topoSort(wf WorkflowJSON) ([]string, error) {
	indegree := make(map[string]int, len(wf.Nodes))
	for _, n := range wf.Nodes {
		indegree[n.ID] = 0
	}
	outgoing := make(map[string][]string, len(wf.Nodes))
	for _, e := range wf.Edges {
		from := edgeNode(e.From)
		to := edgeNode(e.To)
		if _, ok := indegree[from]; !ok {
			return nil, fmt.Errorf("edge references unknown node %q (from)", from)
		}
		if _, ok := indegree[to]; !ok {
			return nil, fmt.Errorf("edge references unknown node %q (to)", to)
		}
		outgoing[from] = append(outgoing[from], to)
		indegree[to]++
	}

	// Initial queue: indegree 0, sorted for determinism
	var queue []string
	for id, d := range indegree {
		if d == 0 {
			queue = append(queue, id)
		}
	}
	sortStrings(queue)

	var order []string
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		order = append(order, n)
		next := outgoing[n]
		sortStrings(next)
		for _, m := range next {
			indegree[m]--
			if indegree[m] == 0 {
				queue = append(queue, m)
			}
		}
		sortStrings(queue)
	}

	if len(order) != len(wf.Nodes) {
		return nil, fmt.Errorf("cycle detected: visited %d of %d nodes", len(order), len(wf.Nodes))
	}
	return order, nil
}

// edgeNode extracts "n1" from "n1.output" or "n1.output_2".
func edgeNode(endpoint string) string {
	for i := 0; i < len(endpoint); i++ {
		if endpoint[i] == '.' {
			return endpoint[:i]
		}
	}
	return endpoint
}

// sortStrings is a tiny ascending sort (avoids importing "sort" for a 3-line need).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// runNode runs L1 → CE → L2 for one node. Returns (output, halt, lastPhase, err).
// halt=true means a verdict halted execution but no Go-level error occurred.
//
// WP-AO-50: auditL1Enabled / auditL2Enabled flags govern whether the L1 / L2
// SaaS calls fire. When false, a synthetic SKIPPED phase event is emitted in
// place of the call and the node proceeds to the next step. Lets workflows
// run end-to-end without the audit-gate SaaS as a dependency.
func runNode(ctx context.Context, node WorkflowNode, input any, runID, loopbackBase, authKey, projectSlug string, auditL1Enabled, auditL2Enabled bool, emit func(RunEvent)) (output any, halt bool, lastPhase string, err error) {
	wfSlug, stageSlug := splitCESlug(node.CESlug)
	spec, ldErr := loadCESpec(wfSlug, stageSlug)
	if ldErr != nil {
		return nil, false, "ce", fmt.Errorf("loadCESpec(%s/%s): %w", wfSlug, stageSlug, ldErr)
	}

	// ── 0. L0 Lobster Trap ingress (WP-01 U4) ───────────────────────
	// Pure-Go regex DPI + LLM canonicalize (Gemini default, Vultr switchable).
	// Sequencing: TrustGateL0 == 0 → layer skipped; ≥ 1 → run.
	// Verdict semantics: DENY halts node before L1/F/L2/L3; LOG continues but
	// persists to SQLite; ALLOW continues silently. UI maps LOG→ALLOW visually.
	if spec.TrustGateL0 == 0 {
		emit(RunEvent{
			Phase: "l0", NodeID: node.ID, CESlug: node.CESlug,
			Verdict: "SKIPPED", Reasons: []string{"trust_gate_L0=0 on CE spec"},
			State: "skipped",
		})
	} else {
		emit(RunEvent{Phase: "l0", NodeID: node.ID, CESlug: node.CESlug, State: "running"})
		l0Start := time.Now()
		inputJSON, _ := json.Marshal(input)
		l0Result := ltInspectWithLLM(string(inputJSON), true)
		l0Latency := time.Since(l0Start).Milliseconds()
		l0Meta, _ := json.Marshal(map[string]interface{}{
			"risk_score":   l0Result.RiskScore,
			"matched_rule": l0Result.MatchedRule,
			"flags":        l0Result.Flags,
			"deny_message": l0Result.DenyMessage,
			"duration_ms":  l0Result.DurationMs,
			"raw":          l0Result.Raw,
		})
		uiVerdict := l0Result.Verdict
		if uiVerdict == "LOG" {
			uiVerdict = "ALLOW" // binary UI mapping per David spec 2026-05-19
		}
		emit(RunEvent{
			Phase: "l0", NodeID: node.ID, CESlug: node.CESlug,
			Verdict: uiVerdict, Meta: l0Meta, LatencyMs: l0Latency, State: "completed",
		})
		// Persist every L0 verdict (ALLOW + LOG + DENY) to SQLite for forensics.
		go AppendLayerAudit(context.Background(), projectSlug, runID, node.CESlug, "L0",
			l0Result.Verdict, l0Result.MatchedRule, l0Result.DenyMessage,
			0, l0Result.RiskScore, l0Result.Flags, input)
		if l0Result.Verdict == "DENY" {
			return nil, true, "l0", nil
		}
	}

	// ── 1. L1 P-check (or SKIP per WP-AO-50 flag) ───────────────────
	if !auditL1Enabled {
		emit(RunEvent{
			Phase: "l1", NodeID: node.ID, CESlug: node.CESlug,
			Verdict: "SKIPPED", Reasons: []string{"audit_l1=false on workflow"},
			State: "skipped",
		})
	} else {
		// WP-AO-59 — phase-start event so the client can animate the active row.
		emit(RunEvent{Phase: "l1", NodeID: node.ID, CESlug: node.CESlug, State: "running"})
		l1Start := time.Now()
		// WP-AO-54 + WP-AO-61 refinement — retrieval-augmented L1.
		// p[] = contract P_pre rules verbatim (rules-to-evaluate).
		// evidence[] = retrieved ledger rows + compliance snippets (grounding).
		// Clean separation per external mapping verification 2026-05-18.
		k := retrievalK()
		ledgerEvi := ledgerCheckForStage(ctx, wfSlug, stageSlug, input, k)
		l1Req := AuditGateRequest{
			I:              input,
			A:              spec.A,
			P:              append([]string{}, spec.PPre...),
			Evidence:       ledgerEvi,
			T:              spec.TrustGateL1,
			SessionContext: fmt.Sprintf("%s / stage=%s / pre-execution", wfSlug, stageSlug),
			Provider:       defaultAuditProvider(),
			GeminiModel:    defaultAuditGeminiModel(),
			Gem2APIKey:     os.Getenv("GEM2_API_KEY"),
		}
		// WP-AO-49 Unit 2 — defensive nil→empty for P. spec.PPre can be nil
		// if the contract has no preconditions in that block; JSON-encodes
		// as `"p": null` which the SaaS rejects as "invalid request body".
		if l1Req.P == nil {
			l1Req.P = []string{}
		}
		injectProviderKey(&l1Req)

		l1Resp, l1Err := callPCheck(ctx, l1Req)
		l1Latency := time.Since(l1Start).Milliseconds()
		if l1Err != nil {
			emit(RunEvent{Phase: "l1", NodeID: node.ID, CESlug: node.CESlug, LatencyMs: l1Latency, Error: l1Err.Error(), State: "completed"})
			return nil, false, "l1", l1Err
		}
		emit(RunEvent{
			Phase: "l1", NodeID: node.ID, CESlug: node.CESlug,
			Verdict: l1Resp.Verdict, Score: l1Resp.Score, Reasons: l1Resp.Reasons,
			Meta: l1Resp.Meta, LatencyMs: l1Latency, State: "completed",
		})
		// WP-01 — persist L1 verdict to SQLite layer_audit_log
		l1Reasons := strings.Join(l1Resp.Reasons, " · ")
		go AppendLayerAudit(context.Background(), projectSlug, runID, node.CESlug, "L1",
			l1Resp.Verdict, "p-check", l1Reasons, 0,
			float64(l1Resp.Score)/100.0, nil, input)
		if l1Resp.Verdict != "ALLOW" {
			return nil, true, "l1", nil
		}
	}

	// ── 2. CE call (HTTP loopback) ──────────────────────────────────
	// WP-AO-59 — phase-start event for the exec sweep animation.
	emit(RunEvent{Phase: "exec", NodeID: node.ID, CESlug: node.CESlug, State: "running"})
	execStart := time.Now()
	ceOut, toolTrace, ceErr := invokeCEViaLoopback(ctx, loopbackBase, authKey, wfSlug, stageSlug, input)
	execLatency := time.Since(execStart).Milliseconds()

	// WP-AO-53 Unit 4 — replay each tool call as a chronological trace event
	// so the canvas run trace shows query_policies(...), now_utc8(), etc.
	// inline between L1 and exec. Emitted even on CE error so judges can see
	// what the LLM tried before it failed.
	for _, tc := range toolTrace {
		errMsg := tc.Error
		emit(RunEvent{
			Phase: "tool", NodeID: node.ID, CESlug: node.CESlug,
			Tool: tc.Tool, ToolArgs: tc.Args, ToolSummary: tc.Summary,
			LatencyMs: tc.LatencyMs, Error: errMsg,
		})
	}

	if ceErr != nil {
		emit(RunEvent{Phase: "exec", NodeID: node.ID, CESlug: node.CESlug, LatencyMs: execLatency, Error: ceErr.Error(), State: "completed"})
		return nil, false, "exec", ceErr
	}
	emit(RunEvent{
		Phase: "exec", NodeID: node.ID, CESlug: node.CESlug,
		LatencyMs: execLatency, Output: ceOut, State: "completed",
	})
	// WP-AO-67 — persist this stage's last successful F-execution output
	// so CE Viewer can surface it via GET /api/ce/{wf}/{stage}/last-run.
	// Best-effort, non-blocking.
	go persistLastRun(wfSlug, stageSlug, node.ID, runID, ceOut)

	// ── 3. L2 O-check (or SKIP per WP-AO-50 flag) ───────────────────
	if !auditL2Enabled {
		emit(RunEvent{
			Phase: "l2", NodeID: node.ID, CESlug: node.CESlug,
			Verdict: "SKIPPED", Reasons: []string{"audit_l2=false on workflow"},
			Output: ceOut, State: "skipped",
		})
		return ceOut, false, "l2", nil
	}
	// WP-AO-59 — phase-start event for the L2 sweep animation.
	emit(RunEvent{Phase: "l2", NodeID: node.ID, CESlug: node.CESlug, State: "running"})
	l2Start := time.Now()
	// WP-AO-54 + WP-AO-61 refinement — retrieval-augmented L2.
	// p[] = contract P_post rules verbatim.
	// evidence[] = ledger rows matching output keys + top-k compliance snippets.
	kPost := retrievalK()
	complianceEvi := complianceCheckForStage(ctx, wfSlug, stageSlug, ceOut, kPost)
	ledgerEviL2 := ledgerCheckForStage(ctx, wfSlug, stageSlug, ceOut, kPost)
	l2Evidence := append([]string{}, ledgerEviL2...)
	l2Evidence = append(l2Evidence, complianceEvi...)
	l2Req := AuditGateRequest{
		O:              ceOut,
		B:              spec.B,
		P:              append([]string{}, spec.PPost...),
		Evidence:       l2Evidence,
		T:              spec.TrustGateL2,
		SessionContext: fmt.Sprintf("%s / stage=%s / post-execution", wfSlug, stageSlug),
		Provider:       defaultAuditProvider(),
		GeminiModel:    defaultAuditGeminiModel(),
		Gem2APIKey:     os.Getenv("GEM2_API_KEY"),
	}
	// WP-AO-49 Unit 2 — defensive nil→empty for P. Same rationale as L1.
	if l2Req.P == nil {
		l2Req.P = []string{}
	}
	injectProviderKey(&l2Req)

	l2Resp, l2Err := callOCheck(ctx, l2Req)
	l2Latency := time.Since(l2Start).Milliseconds()
	if l2Err != nil {
		emit(RunEvent{Phase: "l2", NodeID: node.ID, CESlug: node.CESlug, LatencyMs: l2Latency, Error: l2Err.Error(), State: "completed"})
		return nil, false, "l2", l2Err
	}
	emit(RunEvent{
		Phase: "l2", NodeID: node.ID, CESlug: node.CESlug,
		Verdict: l2Resp.Verdict, Score: l2Resp.Score, Reasons: l2Resp.Reasons,
		Meta: l2Resp.Meta, LatencyMs: l2Latency, State: "completed",
	})
	// WP-01 — persist L2 verdict to SQLite layer_audit_log
	l2Reasons := strings.Join(l2Resp.Reasons, " · ")
	go AppendLayerAudit(context.Background(), projectSlug, runID, node.CESlug, "L2",
		l2Resp.Verdict, "o-check", l2Reasons, 0,
		float64(l2Resp.Score)/100.0, nil, ceOut)
	if l2Resp.Verdict != "SUCCESS" {
		// FAILURE or unknown verdict — halt before forwarding payload.
		return nil, true, "l2", nil
	}

	// ── 4. L3 Lobster Trap egress (WP-01 U4) ────────────────────────
	// Egress mirror of L0: scrub banking → render JSON-as-NL → regex pre-scan
	// → ltInspectWithLLM. TrustGateL3 == 0 → layer skipped.
	if spec.TrustGateL3 == 0 {
		emit(RunEvent{
			Phase: "l3", NodeID: node.ID, CESlug: node.CESlug,
			Verdict: "SKIPPED", Reasons: []string{"trust_gate_L3=0 on CE spec"},
			Output: ceOut, State: "skipped",
		})
		return ceOut, false, "l3", nil
	}
	emit(RunEvent{Phase: "l3", NodeID: node.ID, CESlug: node.CESlug, State: "running"})
	l3Start := time.Now()
	l3Resp := l3EgressInspect(ceOut, true)
	l3Latency := time.Since(l3Start).Milliseconds()
	l3Verdict, _ := l3Resp["verdict"].(string)
	l3Rule, _ := l3Resp["matched_rule"].(string)
	l3DenyMsg, _ := l3Resp["deny_message"].(string)
	l3Risk, _ := l3Resp["risk_score"].(float64)
	var l3Flags []string
	if rawFlags, ok := l3Resp["flags"].([]string); ok {
		l3Flags = rawFlags
	}
	l3Meta, _ := json.Marshal(l3Resp)
	uiL3Verdict := l3Verdict
	if uiL3Verdict == "LOG" {
		uiL3Verdict = "ALLOW"
	}
	emit(RunEvent{
		Phase: "l3", NodeID: node.ID, CESlug: node.CESlug,
		Verdict: uiL3Verdict, Meta: l3Meta, LatencyMs: l3Latency, State: "completed",
	})
	go AppendLayerAudit(context.Background(), projectSlug, runID, node.CESlug, "L3",
		l3Verdict, l3Rule, l3DenyMsg, 0, l3Risk, l3Flags, ceOut)
	if l3Verdict == "DENY" {
		return nil, true, "l3", nil
	}

	return ceOut, false, "l3", nil
}

func splitCESlug(s string) (wf, stage string) {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}

// defaultAuditProvider returns the audit LLM provider. Solar is preferred
// when UPSTAGE_API_KEY is set; override via GEM2_GATE_PROVIDER env.
func defaultAuditProvider() string {
	if v := os.Getenv("GEM2_GATE_PROVIDER"); v != "" {
		return v
	}
	if os.Getenv("UPSTAGE_API_KEY") != "" {
		return "solar"
	}
	return "gemini"
}

// defaultAuditGeminiModel — model used by the L1/L2 audit-gate SaaS when
// provider=gemini. WP-01 (David 2026-05-19): default to gemini-2.5-pro for
// the demo (better epistemic verdict quality on contract preconditions).
// Override via GEM2_GATE_MODEL env. Empty string lets the SaaS pick its own.
func defaultAuditGeminiModel() string {
	if v := strings.TrimSpace(os.Getenv("GEM2_GATE_MODEL")); v != "" {
		return v
	}
	return "gemini-2.5-pro"
}

// injectProviderKey reads the matching env var into the AuditGateRequest.
func injectProviderKey(req *AuditGateRequest) {
	switch req.Provider {
	case "gemini":
		req.GeminiAPIKey = os.Getenv("GEMINI_API_KEY")
	case "claude":
		req.AnthropicAPIKey = os.Getenv("ANTHROPIC_API_KEY")
	case "openai":
		req.OpenAIAPIKey = os.Getenv("OPENAI_API_KEY")
	}
}

// invokeCEViaLoopback POSTs to our own /ce/{wf}/{stage}/ handler. Loopback over
// HTTP keeps Stage 2 the single source of truth for CE execution (we don't
// re-implement the LLM call inside the runner).
// WP-AO-53 Unit 4: now returns the tool-call trace from the envelope so the
// runner can emit per-tool RunEvents to the SSE trace channel.
func invokeCEViaLoopback(ctx context.Context, base, authKey, wf, stage string, input any) (any, []ToolCallTrace, error) {
	body, _ := json.Marshal(CEInvokeRequest{I: input})
	url := fmt.Sprintf("%s/ce/%s/%s/", base, wf, stage)
	req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if authKey != "" {
		req.Header.Set("X-Access-Key", authKey) // matches authGuard semantics
	}
	resp, err := workflowHTTPClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("ce invoke: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, nil, fmt.Errorf("ce invoke %d: %s", resp.StatusCode, string(respBody))
	}
	var envelope CEInvokeResponse
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return nil, nil, fmt.Errorf("ce response decode: %w", err)
	}
	if envelope.Status != "ok" {
		return nil, envelope.ToolCalls, fmt.Errorf("ce status=%s: %s", envelope.Status, envelope.Error)
	}
	return envelope.Output, envelope.ToolCalls, nil
}

// workflowHTTPClient is the shared loopback client. 5-minute total deadline
// per CE call (LLM-bounded — Vultr models can take 30-90s for heavy reasoning).
var workflowHTTPClient = &http.Client{Timeout: 5 * time.Minute}

// appendTruthLog appends one JSONL line. Creates the dir on first call.
func appendTruthLog(runID string, evt RunEvent) error {
	dir := filepath.Join(baseDir, ".gem-squared", "truth-logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, runID+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	enc = append(enc, '\n')
	_, err = f.Write(enc)
	return err
}
