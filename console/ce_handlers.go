package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ── CE Invoke Handler ─────────────────────────────────────────────
//
// POST /ce/{workflow}/{stage}/  →  handleCEInvoke
//
// Loads the CESpec from the registry, calls Vultr LLM with FBlock as the
// system prompt body, and returns the parsed output in a wrapped envelope.
//
// DESIGN INVARIANT (per WP-AO-26 + David 2026-05-16):
//   The CE handler is EXECUTE-ONLY. It does NOT call L1 P-check or L2 O-check.
//   Verification is the orchestrator's job (future WP-AO-27).
//   "The sheep executes. The Wolfi verifies. No agent marks its own homework."

// CEInvokeRequest is what the caller (or Phase-2 orchestrator) POSTs to /ce/{wf}/{stage}/.
type CEInvokeRequest struct {
	I             interface{} `json:"i"`
	ReferenceData interface{} `json:"reference_data,omitempty"`
}

// CEInvokeResponse is the wrapped envelope returned on success or partial success.
type CEInvokeResponse struct {
	Status     string          `json:"status"` // "ok" | "ce_error" | "schema_mismatch"
	Output     interface{}     `json:"output,omitempty"`
	RawOutput  string          `json:"raw_output,omitempty"` // populated only when status=schema_mismatch
	Error      string          `json:"error,omitempty"`
	CESlug     string          `json:"ce_slug"`
	VultrModel string          `json:"vultr_model"`
	DurationMs int64           `json:"duration_ms"`
	// WP-AO-53 Unit 4 — tool-call trace per CE invocation. Populated when the
	// LLM uses query_/count_/now_utc8 tools during F-block execution. The
	// workflow_runner consumes this to emit per-tool RunEvents to the canvas
	// trace channel.
	ToolCalls []ToolCallTrace `json:"tool_calls,omitempty"`
}

func handleCEInvoke(w http.ResponseWriter, r *http.Request) {
	workflow := r.PathValue("workflow")
	stage := r.PathValue("stage")

	// 1. Load registry entry
	spec, err := loadCESpec(workflow, stage)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"error":"ce_not_found","ce_slug":"%s/%s","detail":"no registered CE at this slug; create one via the /create-ce skill"}`, workflow, stage)
			return
		}
		log.Printf("[CE_INVOKE] load error %s/%s: %v", workflow, stage, err)
		http.Error(w, `{"error":"ce_load_error"}`, http.StatusInternalServerError)
		return
	}

	// 2. Decode caller payload
	var req CEInvokeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_request_body"}`, http.StatusBadRequest)
		return
	}
	if req.I == nil {
		http.Error(w, `{"error":"missing_field","detail":"'i' (input value) is required"}`, http.StatusBadRequest)
		return
	}

	model := spec.resolvedVultrModel()
	vultrKey := os.Getenv("VULTR_INFERENCE_API_KEY") // used only if GEM2_CE_RUNTIME=v1

	clientIP := authClientIP(r)
	ceSlug := workflow + "/" + stage

	// WP-AO-63 — route v1 (vultrToolCallLoop, LLM-driven tool calls) vs
	// v2 (deterministic prefetch + single Vultr call). Default v2.
	if ceRuntimeVersion() == "v2" {
		start := time.Now()
		output, raw, toolTrace, dur, runErr := runCEv2(r.Context(), spec, req.I)
		totalMs := time.Since(start).Milliseconds()
		if runErr != nil {
			status := "ce_error"
			if raw != "" {
				status = "schema_mismatch"
			}
			log.Printf("[CE_INVOKE] v2 slug=%s model=%s prefetch_ms=%d exec_ms=%d total_ms=%d status=%s ip=%s err=%v",
				ceSlug, model, dur["prefetch_ms"], dur["exec_ms"], totalMs, status, clientIP, runErr)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(CEInvokeResponse{
				Status:     status,
				RawOutput:  raw,
				Error:      runErr.Error(),
				CESlug:     ceSlug,
				VultrModel: model,
				DurationMs: totalMs,
				ToolCalls:  toolTrace,
			})
			return
		}
		log.Printf("[CE_INVOKE] v2 slug=%s model=%s prefetch_ms=%d exec_ms=%d total_ms=%d status=ok tools_used=%d ip=%s",
			ceSlug, model, dur["prefetch_ms"], dur["exec_ms"], totalMs, len(toolTrace), clientIP)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CEInvokeResponse{
			Status:     "ok",
			Output:     output,
			CESlug:     ceSlug,
			VultrModel: model,
			DurationMs: totalMs,
			ToolCalls:  toolTrace,
		})
		return
	}

	// ── v1 fallback (env GEM2_CE_RUNTIME=v1) — multi-turn tool-call loop ──
	systemPrompt := buildCESystemPrompt(spec)
	userPrompt := buildCEUserPrompt(spec, req.I, req.ReferenceData)
	tools, terr := toolsForProject(spec.WorkflowSlug)
	if terr != nil {
		log.Printf("[CE_INVOKE] toolsForProject %s warning: %v (continuing with static tools only)", spec.WorkflowSlug, terr)
		tools = staticTools()
	}

	start := time.Now()
	raw, toolTrace, llmErr := vultrToolCallLoop(
		r.Context(),
		vultrKey,
		vultrAPIModelID(model),
		systemPrompt,
		userPrompt,
		tools,
		spec.WorkflowSlug,
	)
	durationMs := time.Since(start).Milliseconds()

	if llmErr != nil {
		log.Printf("[CE_INVOKE] v1 slug=%s model=%s duration_ms=%d status=ce_error tools_used=%d ip=%s err=%v",
			ceSlug, model, durationMs, len(toolTrace), clientIP, llmErr)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(CEInvokeResponse{
			Status:     "ce_error",
			Error:      llmErr.Error(),
			CESlug:     ceSlug,
			VultrModel: model,
			DurationMs: durationMs,
			ToolCalls:  toolTrace,
		})
		return
	}

	rawStripped := stripCodeFencesForJSON(raw)
	var output interface{}
	if err := json.Unmarshal([]byte(rawStripped), &output); err != nil {
		log.Printf("[CE_INVOKE] v1 slug=%s model=%s duration_ms=%d status=schema_mismatch tools_used=%d ip=%s parse_err=%v",
			ceSlug, model, durationMs, len(toolTrace), clientIP, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(CEInvokeResponse{
			Status:     "schema_mismatch",
			RawOutput:  raw,
			Error:      fmt.Sprintf("LLM output is not valid JSON: %v", err),
			CESlug:     ceSlug,
			VultrModel: model,
			DurationMs: durationMs,
			ToolCalls:  toolTrace,
		})
		return
	}

	log.Printf("[CE_INVOKE] v1 slug=%s model=%s duration_ms=%d status=ok tools_used=%d ip=%s",
		ceSlug, model, durationMs, len(toolTrace), clientIP)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(CEInvokeResponse{
		Status:     "ok",
		Output:     output,
		CESlug:     ceSlug,
		VultrModel: model,
		DurationMs: durationMs,
		ToolCalls:  toolTrace,
	})
}

// stripCodeFencesForJSON extracts a JSON payload from LLM output that may
// be wrapped in prose. Handles three shapes commonly produced by DeepSeek-V3
// and other instruction-tuned models when they ignore the "JSON-only" rule:
//
//   1. Whole response is a JSON object (no extraction needed)
//   2. Whole response is wrapped in ```json ... ``` fences
//   3. Response has prose around an embedded ```json ... ``` block
//   4. Response has prose around a bare {...} object
//
// Returns the inner JSON text, stripped of leading/trailing whitespace.
// Idempotent on already-clean JSON.
func stripCodeFencesForJSON(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}

	// Shape 1: already a clean JSON object → return as-is
	if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
		return s
	}

	// Shape 2/3: ```json ... ``` block anywhere in the response.
	// Match the LAST such block (LLMs sometimes emit multiple, the final one
	// is the actual answer per their reasoning convention).
	if idx := strings.LastIndex(s, "```json"); idx >= 0 {
		rest := s[idx+len("```json"):]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if end := strings.Index(rest, "```"); end >= 0 {
			return strings.TrimSpace(rest[:end])
		}
	}
	// Also handle bare ``` (no "json" tag) fenced blocks
	if strings.HasPrefix(s, "```") {
		if nl := strings.IndexByte(s, '\n'); nl >= 0 {
			s = s[nl+1:]
		}
		if idx := strings.LastIndex(s, "```"); idx >= 0 {
			s = s[:idx]
		}
		return strings.TrimSpace(s)
	}

	// Shape 4: prose-wrapped bare object. Slice from first `{` to last `}`
	// and validate by trying to parse.
	open := strings.Index(s, "{")
	close := strings.LastIndex(s, "}")
	if open >= 0 && close > open {
		candidate := strings.TrimSpace(s[open : close+1])
		return candidate
	}

	return s
}

// buildCESystemPrompt composes the Vultr LLM system message from the contract's
// FBlock plus a short header + the B schema (so the LLM knows the output shape).
// NOTE: We DO NOT include P_pre or P_post in the CE prompt — those are
// orchestrator concerns (L1/L2). CE just runs F.
//
// WP-AO-60 hotfix: the closing constraint is shouted to discourage
// chain-of-thought-style prose responses that DeepSeek-V3 emits when given
// reasoning-heavy F-blocks. The tool-call section MANDATES that the LLM
// actually query the registered SQLite tables (instead of hallucinating
// "would require database query"). Server tolerates fenced JSON via
// stripCodeFencesForJSON, but clean output is preferred.
func buildCESystemPrompt(spec *CESpec) string {
	// Build the mandatory tool-call directive from this CE's reference_data
	// table list. If the contract registered policies + plan_benefits +
	// claim_utilisation, the prompt tells the LLM exactly which queries it
	// is required to make.
	var toolDirective strings.Builder
	toolDirective.WriteString("## TOOL USE — MANDATORY\n\nYou have these tools available:\n")
	toolDirective.WriteString("- `now_utc8()` — current server-side wall-clock time, ISO-8601 UTC+8. CALL THIS FIRST for any temporal check (claim_date == today, age, submission lag).\n")
	if len(spec.ReferenceData) > 0 {
		tableNames := make([]string, 0, len(spec.ReferenceData))
		for t := range spec.ReferenceData {
			tableNames = append(tableNames, t)
		}
		sort.Strings(tableNames)
		for _, t := range tableNames {
			toolDirective.WriteString(fmt.Sprintf("- `query_%s(where, limit)` and `count_%s(where)` — real SQLite-backed reads against the live `%s` table.\n", t, t, t))
		}
		toolDirective.WriteString("\nBEFORE producing the JSON answer you MUST call the relevant query_<table> tools for every table the F-block references — do NOT hallucinate or write \"would require database query\". The data is live; query it. Multiple tools per turn is allowed (parallel tool_calls). After you have all the evidence, emit the final JSON.\n\nDATE / TIMESTAMP FIELDS: derive ALL `*_timestamp`, `*_date`, and date-based reference IDs (e.g. `claim_reference_draft` shaped like `DRAFT-YYYYMMDD-#####`) from the now_utc8() tool result. NEVER copy a year from `policy_no` (e.g. `HIC-2024-00123` contains the policy-purchase year, NOT today's year) or any other input field. The 'YYYY' segment of any reference ID MUST match now_utc8()'s year.\n")
	} else {
		toolDirective.WriteString("\nUse the time tool whenever you need to know the current date.\n")
	}

	return fmt.Sprintf(
		"You are a Contract-Executor for %q. Apply the F: Processing Logic below to the provided input I.\n\n## F: Processing Logic\n\n%s\n\n## Output schema (B)\n\n%s\n\n%s\n## OUTPUT FORMAT — CRITICAL\n\nYour FINAL response (after all tool calls complete) MUST be a single JSON object matching schema B above. Do NOT include any prose, explanations, reasoning steps, or markdown around the JSON. Do NOT wrap it in ```json fences. Do NOT add fields outside schema B. Return JSON ONLY.",
		spec.ContractTitle, spec.FBlock, asString(spec.B), toolDirective.String())
}

// buildCEUserPrompt assembles the user message with I and any caller-supplied
// reference_data. WP-AO-53 Unit 4: contract-bound ReferenceData is NO LONGER
// inlined into the prompt — the SQLite tool surface (query_<table>, count_<table>)
// is the canonical retrieval path. The prompt mentions the table names so the
// LLM knows what's queryable.
func buildCEUserPrompt(spec *CESpec, i, ref interface{}) string {
	iJSON, _ := json.MarshalIndent(i, "", "  ")
	var b string
	b += "## Input I\n\n```json\n" + string(iJSON) + "\n```\n"
	if ref != nil {
		refJSON, _ := json.MarshalIndent(ref, "", "  ")
		b += "\n## Reference data (caller-supplied)\n\n```json\n" + string(refJSON) + "\n```\n"
	}
	if len(spec.ReferenceData) > 0 {
		// Just enumerate table names — actual rows live in SQLite, retrieved
		// via the registered query_<table>/count_<table> tools.
		var tableNames []string
		for tname := range spec.ReferenceData {
			tableNames = append(tableNames, tname)
		}
		sort.Strings(tableNames)
		b += "\n## Available data tables (use query_<name>/count_<name> tools to retrieve)\n\n"
		for _, t := range tableNames {
			b += "- `" + t + "`\n"
		}
	}
	b += "\nProduce the JSON output now."
	return b
}

// vultrAPIModelID maps our internal "vultr/Model" slug to Vultr's full
// namespaced model ID required by the /v1/chat/completions API.
// Verified live via GET /v1/models (see WP-AO-23 Unit 1).
//
// The existing vultrModelName() helper strips the "vultr/" prefix but does
// NOT add the publisher namespace — that suffices for some legacy code paths
// but fails for the CE handler because the API requires the full ID.
func vultrAPIModelID(slug string) string {
	switch slug {
	case "vultr/Kimi-K2.6":
		return "moonshotai/Kimi-K2.6"
	case "vultr/DeepSeek-V3.2-NVFP4":
		return "nvidia/DeepSeek-V3.2-NVFP4"
	case "vultr/Llama-3.1-Nemotron-Safety-Guard-8B-v3":
		return "nvidia/Llama-3.1-Nemotron-Safety-Guard-8B-v3"
	case "vultr/Nemotron-3-Nano-Omni-30B-A3B-Reasoning-BF16":
		return "nvidia/Nemotron-3-Nano-Omni-30B-A3B-Reasoning-BF16"
	case "vultr/Nemotron-Cascade-2-30B-A3B":
		return "nvidia/Nemotron-Cascade-2-30B-A3B"
	case "vultr/GLM-5.1-FP8":
		return "zai-org/GLM-5.1-FP8"
	case "vultr/MiniMax-M2.7":
		return "MiniMaxAI/MiniMax-M2.7"
	}
	// Fallback: strip "vultr/" prefix (legacy behavior). Vultr may reject it
	// but the error surfaces cleanly via ce_error envelope.
	return vultrModelName(slug)
}

func asString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	if b, err := json.MarshalIndent(v, "", "  "); err == nil {
		return string(b)
	}
	return fmt.Sprintf("%v", v)
}

// ── CE Bundle Static Serve ────────────────────────────────────────
//
// GET /{slug}/ce/{ce_name}/{path...}
// Serves the per-CE HTML wrapper bundle from
// {baseDir}/.gem-squared/workspace/{slug}/ce/{ce_name}/{path}.
// authGuard wrapped — judges must hold the same access key as the rest
// of the console. WP-AO-30 Unit 3.

// handleCEBundleServe — static-serves files under a project's ce/{ce_name}/ dir.
func handleCEBundleServe(w http.ResponseWriter, r *http.Request) {
	projectSlug := r.PathValue("slug")
	ceName := r.PathValue("ce_name")
	subPath := r.PathValue("path")

	// Slug validation (reuses registry slug regex from ce_registry.go)
	if !slugPattern.MatchString(projectSlug) {
		http.Error(w, `{"error":"invalid_slug"}`, http.StatusBadRequest)
		return
	}
	if !slugPattern.MatchString(ceName) {
		http.Error(w, `{"error":"invalid_ce_name"}`, http.StatusBadRequest)
		return
	}

	bundleDir := filepath.Join(baseDir, ".gem-squared", "workspace", projectSlug, "ce", ceName)
	if info, err := os.Stat(bundleDir); err != nil || !info.IsDir() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":"ce_bundle_not_found","ce_slug":"%s/%s","detail":"no CE bundle at this path; create one via the /create-ce skill"}`, projectSlug, ceName)
		return
	}

	if subPath == "" {
		subPath = "index.html"
	}
	// Reject path traversal — Clean + check it doesn't escape bundleDir.
	clean := filepath.Clean("/" + subPath)
	if strings.Contains(clean, "..") {
		http.Error(w, `{"error":"invalid_path"}`, http.StatusBadRequest)
		return
	}
	fullPath := filepath.Join(bundleDir, strings.TrimPrefix(clean, "/"))
	// Final safety: resolved path must stay under bundleDir
	absBundle, _ := filepath.Abs(bundleDir)
	absFull, _ := filepath.Abs(fullPath)
	if !strings.HasPrefix(absFull, absBundle) {
		http.Error(w, `{"error":"path_traversal_rejected"}`, http.StatusBadRequest)
		return
	}

	info, err := os.Stat(fullPath)
	if err != nil || info.IsDir() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":"file_not_found","path":"%s"}`, subPath)
		return
	}

	http.ServeFile(w, r, fullPath)
}

// ── CE Registry Management Handlers ───────────────────────────────

// handleCEGlobal — POST /api/crafter/ce-global
// Walks all workspace projects' ce/{name}/ bundles and returns the cross-
// project union with both html_url and api_endpoint_url. WP-AO-30 Unit 4.
// POST (not GET) per David's no-GET rule for new API endpoints.
func handleCEGlobal(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		ProjectSlug    string `json:"project_slug"`
		CEName         string `json:"ce_name"`
		ContractTitle  string `json:"contract_title"`
		WorkflowSlug   string `json:"workflow_slug"`
		StageSlug      string `json:"stage_slug"`
		HTMLURL        string `json:"html_url"`
		APIEndpointURL string `json:"api_endpoint_url"`
		VultrModel     string `json:"vultr_model"`
		CreatedAt      string `json:"created_at"`
		UpdatedAt      string `json:"updated_at"`
	}

	workspaceRoot := filepath.Join(baseDir, ".gem-squared", "workspace")
	entries := []entry{}

	projDirs, err := os.ReadDir(workspaceRoot)
	if err != nil {
		// Empty workspace is fine
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"count": 0, "ces": entries})
		return
	}

	for _, pd := range projDirs {
		if !pd.IsDir() {
			continue
		}
		projectSlug := pd.Name()
		ceRoot := filepath.Join(workspaceRoot, projectSlug, "ce")
		ceDirs, err := os.ReadDir(ceRoot)
		if err != nil {
			continue // project has no ce/ dir
		}
		for _, cd := range ceDirs {
			if !cd.IsDir() {
				continue
			}
			ceName := cd.Name()
			// Must have an index.html to be considered a CE bundle
			if _, err := os.Stat(filepath.Join(ceRoot, ceName, "index.html")); err != nil {
				continue
			}
			// Try to load CESpec from registry (workflow_slug == projectSlug? not necessarily)
			// Heuristic: the registry stores by {workflow_slug}/{stage_slug}. We have stage_slug
			// (ceName); workflow_slug is in the spec's CESpec.WorkflowSlug field. Walk the
			// global registry and match.
			spec := findCESpecByStage(ceName, projectSlug)
			ent := entry{
				ProjectSlug: projectSlug,
				CEName:      ceName,
				HTMLURL:     fmt.Sprintf("%s/p/%s/ce/%s/", ceProductionHost, projectSlug, ceName),
			}
			if spec != nil {
				ent.ContractTitle = spec.ContractTitle
				ent.WorkflowSlug = spec.WorkflowSlug
				ent.StageSlug = spec.StageSlug
				ent.APIEndpointURL = fmt.Sprintf("%s/ce/%s/%s/", ceProductionHost, spec.WorkflowSlug, spec.StageSlug)
				ent.VultrModel = spec.resolvedVultrModel()
				ent.CreatedAt = spec.CreatedAt
				ent.UpdatedAt = spec.UpdatedAt
			}
			entries = append(entries, ent)
		}
	}

	// Sort by project_slug then ce_name
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].ProjectSlug != entries[j].ProjectSlug {
			return entries[i].ProjectSlug < entries[j].ProjectSlug
		}
		return entries[i].CEName < entries[j].CEName
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"count": len(entries),
		"ces":   entries,
	})
}

// findCESpecByStage searches the global ce-registry for a spec whose StageSlug
// matches. Used by handleCEGlobal to attach registry metadata to bundle entries.
func findCESpecByStage(stageSlug, projectHint string) *CESpec {
	specs, err := listCESpecs()
	if err != nil {
		return nil
	}
	for i := range specs {
		if specs[i].StageSlug == stageSlug {
			return &specs[i]
		}
	}
	_ = projectHint // not used yet — would help disambiguate same stage_slug across workflows
	return nil
}

func handleCEList(w http.ResponseWriter, r *http.Request) {
	specs, err := listCESpecs()
	if err != nil {
		http.Error(w, `{"error":"list_error"}`, http.StatusInternalServerError)
		return
	}

	// Light projection — drop FBlock content (could be large) but keep counts.
	type ceListEntry struct {
		WorkflowSlug  string `json:"workflow_slug"`
		StageSlug     string `json:"stage_slug"`
		ContractTitle string `json:"contract_title"`
		Domain        string `json:"domain,omitempty"`
		PPreCount     int    `json:"p_pre_count"`
		PPostCount    int    `json:"p_post_count"`
		TrustGateL0   int    `json:"trust_gate_l0"`
		TrustGateL1   int    `json:"trust_gate_l1"`
		TrustGateL2   int    `json:"trust_gate_l2"`
		TrustGateL3   int    `json:"trust_gate_l3"`
		VultrModel    string `json:"vultr_model"`
		StageType     string `json:"stage_type,omitempty"`
		AgentRole     string `json:"agent_role,omitempty"`
		EndpointURL   string `json:"endpoint_url"`
		CreatedAt     string `json:"created_at"`
		UpdatedAt     string `json:"updated_at"`
	}

	out := make([]ceListEntry, 0, len(specs))
	for _, s := range specs {
		out = append(out, ceListEntry{
			WorkflowSlug:  s.WorkflowSlug,
			StageSlug:     s.StageSlug,
			ContractTitle: s.ContractTitle,
			Domain:        s.Domain,
			PPreCount:     len(s.PPre),
			PPostCount:    len(s.PPost),
			TrustGateL0:   s.TrustGateL0,
			TrustGateL1:   s.TrustGateL1,
			TrustGateL2:   s.TrustGateL2,
			TrustGateL3:   s.TrustGateL3,
			VultrModel:    s.resolvedVultrModel(),
			StageType:     s.StageType,
			AgentRole:     s.AgentRole,
			EndpointURL:   fmt.Sprintf("%s/ce/%s/%s/", ceProductionHost, s.WorkflowSlug, s.StageSlug),
			CreatedAt:     s.CreatedAt,
			UpdatedAt:     s.UpdatedAt,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"count": len(out),
		"ces":   out,
	})
}
