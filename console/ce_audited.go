package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

// ── WP-AO-62: CE Viewer Unit-Level Governance Chain ──────────────
//
// POST /ce/{workflow}/{stage}/audited
//
// Single-CE end-to-end: L1 P-check → F (CE invoke) → L2 O-check.
// Returns all three results in one envelope so the CE Viewer can
// render the full governance chain for an isolated CE — judges click
// the per-node CE viewer modal and see L1 verdict, F output, tool
// calls, L2 verdict without leaving the modal.
//
// Composition mirrors workflow_runner.go::runNode but trace emit is
// dropped (this endpoint is non-streaming; the viewer's UI handles
// per-section rendering after the full response arrives). To keep
// blast radius small the audit composition is duplicated here rather
// than refactored into a shared helper — that refactor is tech debt
// for a follow-up WP.

// AuditedCEResponse is the envelope returned by /audited.
type AuditedCEResponse struct {
	Status          string             `json:"status"` // ok | halted_l1 | ce_error | halted_l2 | schema_mismatch
	L1              *AuditedGateResult `json:"l1,omitempty"`
	Output          interface{}        `json:"output,omitempty"`
	RawOutput       string             `json:"raw_output,omitempty"`
	ToolCalls       []ToolCallTrace    `json:"tool_calls,omitempty"`
	L2              *AuditedGateResult `json:"l2,omitempty"`
	CESlug          string             `json:"ce_slug"`
	VultrModel      string             `json:"vultr_model"`
	Error           string             `json:"error,omitempty"`
	Durations       map[string]int64   `json:"durations"` // l1_ms, exec_ms, l2_ms, total_ms
	TotalDurationMs int64              `json:"total_duration_ms"`
}

// AuditedGateResult is the per-gate slice of the envelope. Mirrors
// AuditGateResponse but always present even on skipped/error paths.
type AuditedGateResult struct {
	Verdict   string          `json:"verdict"`
	Score     int             `json:"score"`
	Reasons   []string        `json:"reasons,omitempty"`
	Meta      json.RawMessage `json:"meta,omitempty"`
	LatencyMs int64           `json:"latency_ms"`
	Error     string          `json:"error,omitempty"`
}

// handleCEInvokeAudited runs L1 → F → L2 against a single CE and
// returns the combined envelope. Halt semantics: L1 DENY skips F + L2;
// CE error skips L2 (L2 has nothing to audit); L2 FAILURE returns
// everything (judges still see the verdict + reasons).
func handleCEInvokeAudited(w http.ResponseWriter, r *http.Request) {
	workflow := r.PathValue("workflow")
	stage := r.PathValue("stage")
	totalStart := time.Now()

	spec, err := loadCESpec(workflow, stage)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"error":"ce_not_found","ce_slug":"%s/%s"}`, workflow, stage)
			return
		}
		log.Printf("[CE_AUDITED] load error %s/%s: %v", workflow, stage, err)
		http.Error(w, `{"error":"ce_load_error"}`, http.StatusInternalServerError)
		return
	}

	var req CEInvokeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_request_body"}`, http.StatusBadRequest)
		return
	}
	if req.I == nil {
		http.Error(w, `{"error":"missing_field","detail":"'i' (input value) is required"}`, http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	ceSlug := workflow + "/" + stage
	model := spec.resolvedVultrModel()
	envelope := &AuditedCEResponse{
		CESlug:     ceSlug,
		VultrModel: model,
		Durations:  map[string]int64{},
	}

	// ── 1. L1 P-check ────────────────────────────────────────────
	k := retrievalK()
	l1Start := time.Now()
	ledgerEviL1 := ledgerCheckForStage(ctx, workflow, stage, req.I, k)
	l1Req := AuditGateRequest{
		I:              req.I,
		A:              spec.A,
		P:              append([]string{}, spec.PPre...),
		Evidence:       ledgerEviL1,
		T:              spec.TrustGateL1,
		SessionContext: fmt.Sprintf("%s / stage=%s / pre-execution (viewer)", workflow, stage),
		Provider:       defaultAuditProvider(),
		Gem2APIKey:     os.Getenv("GEM2_API_KEY"),
	}
	if l1Req.P == nil {
		l1Req.P = []string{}
	}
	injectProviderKey(&l1Req)
	l1Resp, l1Err := callPCheck(ctx, l1Req)
	envelope.Durations["l1_ms"] = time.Since(l1Start).Milliseconds()
	if l1Err != nil {
		envelope.Status = "halted_l1"
		envelope.L1 = &AuditedGateResult{LatencyMs: envelope.Durations["l1_ms"], Error: l1Err.Error()}
		envelope.Error = l1Err.Error()
		envelope.TotalDurationMs = time.Since(totalStart).Milliseconds()
		writeAuditedJSON(w, envelope, http.StatusOK)
		return
	}
	envelope.L1 = &AuditedGateResult{
		Verdict: l1Resp.Verdict, Score: l1Resp.Score, Reasons: l1Resp.Reasons,
		Meta: l1Resp.Meta, LatencyMs: envelope.Durations["l1_ms"],
	}
	if l1Resp.Verdict != "ALLOW" {
		envelope.Status = "halted_l1"
		envelope.TotalDurationMs = time.Since(totalStart).Milliseconds()
		writeAuditedJSON(w, envelope, http.StatusOK)
		return
	}

	// ── 2. F: CE Executor — routes between v1 (LLM tool loop) and v2
	//        (server prefetch + single Solar call). Default v2 per WP-AO-63.
	vultrKey := os.Getenv("VULTR_INFERENCE_API_KEY") // used only if GEM2_CE_RUNTIME=v1

	var output interface{}
	var raw string
	var toolTrace []ToolCallTrace
	var ceErr error

	if ceRuntimeVersion() == "v2" {
		var v2dur map[string]int64
		output, raw, toolTrace, v2dur, ceErr = runCEv2(ctx, spec, req.I)
		envelope.Durations["prefetch_ms"] = v2dur["prefetch_ms"]
		envelope.Durations["exec_ms"] = v2dur["exec_ms"]
	} else {
		// v1 fallback
		tools, terr := toolsForProject(spec.WorkflowSlug)
		if terr != nil {
			log.Printf("[CE_AUDITED] toolsForProject %s warning: %v", spec.WorkflowSlug, terr)
			tools = staticTools()
		}
		systemPrompt := buildCESystemPrompt(spec)
		userPrompt := buildCEUserPrompt(spec, req.I, req.ReferenceData)
		execStart := time.Now()
		raw, toolTrace, ceErr = vultrToolCallLoop(
			ctx, vultrKey, vultrAPIModelID(model),
			systemPrompt, userPrompt, tools, spec.WorkflowSlug,
		)
		envelope.Durations["exec_ms"] = time.Since(execStart).Milliseconds()
		if ceErr == nil {
			stripped := stripCodeFencesForJSON(raw)
			if jerr := json.Unmarshal([]byte(stripped), &output); jerr != nil {
				ceErr = fmt.Errorf("LLM output is not valid JSON: %w", jerr)
			}
		}
	}

	envelope.ToolCalls = toolTrace

	if ceErr != nil {
		status := "ce_error"
		if raw != "" && output == nil {
			status = "schema_mismatch"
			envelope.RawOutput = raw
		}
		envelope.Status = status
		envelope.Error = ceErr.Error()
		envelope.TotalDurationMs = time.Since(totalStart).Milliseconds()
		writeAuditedJSON(w, envelope, http.StatusOK)
		return
	}
	envelope.Output = output

	// ── 3. L2 O-check ────────────────────────────────────────────
	l2Start := time.Now()
	ledgerEviL2 := ledgerCheckForStage(ctx, workflow, stage, output, k)
	complianceEvi := complianceCheckForStage(ctx, workflow, stage, output, k)
	l2Evidence := append([]string{}, ledgerEviL2...)
	l2Evidence = append(l2Evidence, complianceEvi...)
	l2Req := AuditGateRequest{
		O:              output,
		B:              spec.B,
		P:              append([]string{}, spec.PPost...),
		Evidence:       l2Evidence,
		T:              spec.TrustGateL2,
		SessionContext: fmt.Sprintf("%s / stage=%s / post-execution (viewer)", workflow, stage),
		Provider:       defaultAuditProvider(),
		Gem2APIKey:     os.Getenv("GEM2_API_KEY"),
	}
	if l2Req.P == nil {
		l2Req.P = []string{}
	}
	injectProviderKey(&l2Req)
	l2Resp, l2Err := callOCheck(ctx, l2Req)
	envelope.Durations["l2_ms"] = time.Since(l2Start).Milliseconds()
	if l2Err != nil {
		envelope.Status = "halted_l2"
		envelope.L2 = &AuditedGateResult{LatencyMs: envelope.Durations["l2_ms"], Error: l2Err.Error()}
		envelope.Error = l2Err.Error()
		envelope.TotalDurationMs = time.Since(totalStart).Milliseconds()
		writeAuditedJSON(w, envelope, http.StatusOK)
		return
	}
	envelope.L2 = &AuditedGateResult{
		Verdict: l2Resp.Verdict, Score: l2Resp.Score, Reasons: l2Resp.Reasons,
		Meta: l2Resp.Meta, LatencyMs: envelope.Durations["l2_ms"],
	}

	switch l2Resp.Verdict {
	case "SUCCESS":
		envelope.Status = "ok"
	case "FAILURE":
		envelope.Status = "halted_l2"
	default:
		envelope.Status = "halted_l2"
	}
	envelope.TotalDurationMs = time.Since(totalStart).Milliseconds()
	writeAuditedJSON(w, envelope, http.StatusOK)
}

func writeAuditedJSON(w http.ResponseWriter, env *AuditedCEResponse, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}
