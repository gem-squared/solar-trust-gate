package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// buildCEViewerURL produces the one-click /ce-viewer link with API endpoint,
// pre-generated sample input, contract title, and access key encoded as
// URL params. Judges click in chat → form auto-fills → one click on Run.
// The access key falls back to firstAuthKey() (single-tenant demo); if no
// AUTH_KEYS env is set, the key param is omitted and the page's inline
// auth gate prompts the user.
func buildCEViewerURL(host string, spec *CESpec, apiPath string) string {
	q := url.Values{}
	q.Set("api", apiPath)
	if spec.ContractTitle != "" {
		q.Set("title", spec.ContractTitle)
	}
	if spec.SampleI != "" {
		q.Set("sample", spec.SampleI)
	}
	if k := firstAuthKey(); k != "" {
		q.Set("key", k)
	}
	return strings.TrimRight(host, "/") + "/ce-viewer?" + q.Encode()
}

// generateSampleInput asks the Vultr LLM to produce a single valid example JSON
// input that satisfies all of the contract's P_pre preconditions. The result is
// stored on spec.SampleI and used by the HTML test page to pre-fill the input
// textarea. Soft-fail: returns ("", err) on any failure; caller decides whether
// to keep going with an empty sample (current policy: yes, continue).
func generateSampleInput(spec *CESpec) (string, error) {
	apiKey := os.Getenv("VULTR_INFERENCE_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("VULTR_INFERENCE_API_KEY not set")
	}
	pPreText := "(none)"
	if len(spec.PPre) > 0 {
		pPreText = "- " + strings.Join(spec.PPre, "\n- ")
	}
	sys := "You generate one valid example JSON input for a TPMN contract. Output ONLY raw JSON — no markdown fences, no commentary, no prose."
	user := fmt.Sprintf(`Contract: %s

## A (input schema):
%s

## P_pre (preconditions the input MUST satisfy):
%s

Produce a SINGLE JSON object that is a valid example of input I. It must satisfy every P_pre rule above (type alignment, format validation, regulation/compliance gates). Use realistic plausible values. Return ONLY the JSON object — nothing else.`,
		spec.ContractTitle, asString(spec.A), pPreText)

	raw, err := vultrSheepCall(apiKey, vultrModelName(spec.resolvedVultrModel()), sys, user)
	if err != nil {
		return "", fmt.Errorf("vultr call: %w", err)
	}
	raw = strings.TrimSpace(raw)
	// Strip markdown fences defensively
	if strings.HasPrefix(raw, "```") {
		if i := strings.Index(raw, "\n"); i > 0 {
			raw = raw[i+1:]
		}
		if i := strings.LastIndex(raw, "```"); i > 0 {
			raw = raw[:i]
		}
		raw = strings.TrimSpace(raw)
	}
	// Trim anything before the first { and after the last }
	if i := strings.Index(raw, "{"); i > 0 {
		raw = raw[i:]
	}
	if i := strings.LastIndex(raw, "}"); i > 0 && i < len(raw)-1 {
		raw = raw[:i+1]
	}
	var probe interface{}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return "", fmt.Errorf("LLM returned non-JSON: %w (got: %.200s)", err, raw)
	}
	pretty, perr := json.MarshalIndent(probe, "", "  ")
	if perr != nil {
		return raw, nil
	}
	return string(pretty), nil
}

// detectMostRecentTPMNContract scans {projectDir}/uploaded_files/ for .md files
// matching the 5-block TPMN format and returns the most recent match by mtime.
// Returns a structured error if no uploaded_files dir, no .md files, or no
// valid TPMN contract is found — the caller surfaces this to the UI.
func detectMostRecentTPMNContract(projectDir string) (string, error) {
	uploadDir := filepath.Join(projectDir, "uploaded_files")
	entries, err := os.ReadDir(uploadDir)
	if err != nil {
		return "", fmt.Errorf("no uploaded_files/ directory in active project (%s)", projectDir)
	}
	type cand struct {
		path  string
		mtime time.Time
	}
	var candidates []cand
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		candidates = append(candidates, cand{path: filepath.Join(uploadDir, e.Name()), mtime: info.ModTime()})
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no .md files found in uploaded_files/")
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].mtime.After(candidates[j].mtime) })
	for _, c := range candidates {
		data, rerr := os.ReadFile(c.path)
		if rerr != nil {
			continue
		}
		// Scan the FULL file — the 5 headers can span past any fixed prefix
		// (e.g., workbenchiq-05 has ## P_post at offset 4676). isTPMNContract
		// is cheap (5 substring searches) so no need to cap.
		if isTPMNContract(string(data)) {
			return c.path, nil
		}
	}
	return "", fmt.Errorf("found %d .md file(s) in uploaded_files/ but none match the 5-block TPMN format", len(candidates))
}

// ── CE Generator ──────────────────────────────────────────────────
//
// Implements the `create-ce` skill (see .gem-squared/gem2-core-skills/create-ce/SKILL.md).
// Parses a TPMN contract markdown file, validates it, writes the CESpec to the
// registry, and returns the resulting endpoint URL. NO LLM calls — config only.

const ceProductionHost = "https://ai-olympic.gemsquared.ai"

// ceCollisionError is returned when a CE already exists at the target slug.
type ceCollisionError struct {
	Slug      string
	CreatedAt string
}

func (e *ceCollisionError) Error() string {
	return fmt.Sprintf("ce_collision: %s already exists (created %s) — pass force=true to overwrite", e.Slug, e.CreatedAt)
}

// CEGenPreview is the human-readable summary returned to the user/UI.
type CEGenPreview struct {
	ContractTitle string `json:"contract_title"`
	ASummary      string `json:"a_summary"`
	BSummary      string `json:"b_summary"`
	PPreCount     int    `json:"p_pre_count"`
	PPostCount    int    `json:"p_post_count"`
	TrustGateL0   int    `json:"trust_gate_l0"`
	TrustGateL1   int    `json:"trust_gate_l1"`
	TrustGateL2   int    `json:"trust_gate_l2"`
	TrustGateL3   int    `json:"trust_gate_l3"`
	VultrModel    string `json:"vultr_model"`
}

// executeCreateCE handles the create-ce skill invocation.
// Wired in executor.go:ExecuteSkill switch as `case "create-ce"`.
func executeCreateCE(args map[string]any, state *CrafterState, sess *SessionData, start time.Time) (*SkillExecResult, error) {
	filePath, _ := args["file_path"].(string)
	if filePath == "" {
		// allow synonyms commonly emitted by the orchestrator
		filePath, _ = args["path"].(string)
	}
	if filePath == "" {
		filePath, _ = args["contract"].(string)
	}
	if filePath == "" {
		// WP-AO-30 hot-fix Unit 8: auto-detect the most recent TPMN markdown
		// in the active project's uploaded_files/ dir. Lets task_chain
		// [create-project, create-ce] run end-to-end without the orchestrator
		// guessing a file path.
		if state == nil || state.ProjectDir == "" {
			return nil, fmt.Errorf("create-ce needs an active project. Create or switch to a project first, then upload a TPMN contract markdown (5-block format) to uploaded_files/")
		}
		detected, derr := detectMostRecentTPMNContract(state.ProjectDir)
		if derr != nil {
			return nil, fmt.Errorf("create-ce: %w. Upload a TPMN contract markdown (5-block format with ## A:, ## P_pre:, ## F:, ## B:, ## P_post: headers) to uploaded_files/ then retry", derr)
		}
		filePath = detected
		log.Printf("[CREATE-CE] auto-detected contract: %s", filePath)
	}

	workflowOverride, _ := args["workflow_slug"].(string)
	stageOverride, _ := args["stage_slug"].(string)
	modelOverride, _ := args["vultr_model"].(string)
	force, _ := args["force"].(bool)

	// Resolve path: absolute kept; relative resolves under active project's uploaded_files/ or workspace root.
	resolvedPath := resolveContractPath(filePath, state, sess)
	if _, err := os.Stat(resolvedPath); err != nil {
		return nil, fmt.Errorf("contract file not found: %s (resolved from %q)", resolvedPath, filePath)
	}

	log.Printf("[CREATE-CE] parsing contract: %s", resolvedPath)

	spec, parseErr := parseContractFile(resolvedPath)
	if parseErr != nil {
		var ccErr *conflatedContractError
		if errors.As(parseErr, &ccErr) {
			return &SkillExecResult{
				Skill: "create-ce",
				OutputB: fmt.Sprintf(
					"## Contract Format Invalid\n\n**File:** `%s`\n\n**Issue:** %s\n\nYour contract appears to use the old A/F/B/P conflated format. Phase-2 runtime needs the split format (A · P_pre · F · B · P_post).\n\n**See:** [Docs/contract-authoring-guide.md §2](../Docs/contract-authoring-guide.md)\n\n**Example refactor:** `Docs/workbenchiq-05-payout-calculation-refactored.md`",
					filePath, ccErr.Detail),
				Duration: time.Since(start).Milliseconds(),
			}, nil
		}
		return nil, fmt.Errorf("parse contract: %w", parseErr)
	}

	// Apply overrides
	if workflowOverride != "" {
		spec.WorkflowSlug = workflowOverride
	}
	if stageOverride != "" {
		spec.StageSlug = stageOverride
	}
	if modelOverride != "" {
		spec.VultrModel = modelOverride
	}
	// WP-AO-36: when the contract's Circus Executor block did NOT specify a
	// vultr_model AND the caller did NOT pass an explicit vultr_model
	// override, fall back to the session's AgentModel selection (the UI's
	// "Agent LLM" dropdown). Contract-specified models still win — preserves
	// authoring intent. Only used when the contract is silent on the model.
	if spec.VultrModel == "" && sess != nil && sess.AgentModel != "" && isVultrModel(sess.AgentModel) {
		spec.VultrModel = sess.AgentModel
		log.Printf("[CREATE-CE] agent_model fallback: contract silent on vultr_model, using session AGENT LLM %q", sess.AgentModel)
	}

	// Validate
	if err := validateCESpec(spec); err != nil {
		return nil, fmt.Errorf("validate ce spec: %w", err)
	}

	// Generate a sample input via Vultr LLM so the HTML test page is
	// click-and-submit ready. Soft-fail: if generation errors, the HTML
	// falls back to "{}" and the user fills the textarea themselves.
	if sample, gerr := generateSampleInput(spec); gerr == nil {
		spec.SampleI = sample
		// WP-AO-67 follow-up — freeze the original at creation so judges can
		// see the 'STANDARD CORRECTION DATA' baseline even after edits.
		spec.OriginalSampleI = sample
		log.Printf("[CREATE-CE] sample input generated (%d bytes)", len(sample))
	} else {
		log.Printf("[CREATE-CE] sample input generation failed (continuing with empty): %v", gerr)
	}

	// Collision check
	existing, err := loadCESpec(spec.WorkflowSlug, spec.StageSlug)
	if err == nil && existing != nil {
		if !force {
			// WP-AO-33: emit the same viewer button + sample prefill the
			// success path produces, so the EXISTING CE is one-click testable
			// even on collision. Built from `existing` (not `spec`) since
			// that's what the live registry holds. Legacy CEs created
			// pre-WP-AO-30 will have empty SampleI — viewer falls back to {}.
			existingAPIPath := fmt.Sprintf("/ce/%s/%s/", existing.WorkflowSlug, existing.StageSlug)
			existingViewerURL := buildCEViewerURL(ceProductionHost, existing, existingAPIPath)
			var ob strings.Builder
			ob.WriteString("## CE already exists\n\n")
			ob.WriteString(fmt.Sprintf("**Contract:** %s\n", existing.ContractTitle))
			ob.WriteString(fmt.Sprintf("**CE slug:** `%s/%s`\n", existing.WorkflowSlug, existing.StageSlug))
			ob.WriteString(fmt.Sprintf("**Vultr model:** `%s`\n", existing.resolvedVultrModel()))
			ob.WriteString(fmt.Sprintf("**Created:** %s\n", existing.CreatedAt))
			ob.WriteString("\nThe existing CE is live and callable. Open the viewer to test it now, or re-run with `force=true` to overwrite with your new contract.\n")
			ob.WriteString(fmt.Sprintf("\n### API endpoint (curl/programs)\n`%s/ce/%s/%s/`\n",
				ceProductionHost, existing.WorkflowSlug, existing.StageSlug))
			ob.WriteString(fmt.Sprintf("\n[[CE_VIEWER_BUTTON|%s]]\n", existingViewerURL))
			return &SkillExecResult{
				Skill:    "create-ce",
				OutputB:  ob.String(),
				Duration: time.Since(start).Milliseconds(),
			}, nil
		}
		// preserve created_at on force-overwrite
		spec.CreatedAt = existing.CreatedAt
		log.Printf("[CREATE-CE] force-overwriting existing CE %s/%s", spec.WorkflowSlug, spec.StageSlug)
	}

	// Persist registry entry
	if err := saveCESpec(spec); err != nil {
		return nil, fmt.Errorf("save ce spec: %w", err)
	}

	// WP-AO-31 — replace per-CE HTML bundles with one generic /ce-viewer page,
	// parameterized via URL query params. AI-Pilot surfaces a clickable link
	// in chat; judges click → form pre-filled → one click on Run.
	apiEndpointPath := fmt.Sprintf("/ce/%s/%s/", spec.WorkflowSlug, spec.StageSlug)
	viewerURL := buildCEViewerURL(ceProductionHost, spec, apiEndpointPath)

	// Build preview
	preview := CEGenPreview{
		ContractTitle: spec.ContractTitle,
		ASummary:      summarize(spec.A, 200),
		BSummary:      summarize(spec.B, 200),
		PPreCount:     len(spec.PPre),
		PPostCount:    len(spec.PPost),
		TrustGateL0:   spec.TrustGateL0,
		TrustGateL1:   spec.TrustGateL1,
		TrustGateL2:   spec.TrustGateL2,
		TrustGateL3:   spec.TrustGateL3,
		VultrModel:    spec.resolvedVultrModel(),
	}
	registryPath := cePath(spec.WorkflowSlug, spec.StageSlug)
	apiEndpointURL := fmt.Sprintf("%s/ce/%s/%s/", ceProductionHost, spec.WorkflowSlug, spec.StageSlug)

	log.Printf("[CREATE-CE] saved %s → %s (viewer: %s)", spec.WorkflowSlug+"/"+spec.StageSlug, registryPath, viewerURL)

	// Format output as markdown (consumed by orchestrator chat)
	output := formatCEGenOutput(spec, registryPath, apiEndpointURL, viewerURL, preview)

	// Also return the structured payload as JSON in the StateChange field so
	// programmatic callers can parse it.
	structured, _ := json.Marshal(map[string]any{
		"ce_slug":              spec.WorkflowSlug + "/" + spec.StageSlug,
		"registry_path":        registryPath,
		"api_endpoint_url":     apiEndpointURL,
		"viewer_url":           viewerURL,
		"preview":              preview,
		"immediately_runnable": true,
	})

	return &SkillExecResult{
		Skill:       "create-ce",
		OutputB:     output,
		StateChange: string(structured),
		Duration:    time.Since(start).Milliseconds(),
	}, nil
}

// resolveContractPath maps a user-provided file_path to an absolute path on disk.
// Resolution order:
//   1. absolute path → use as-is
//   2. path starts with "uploaded_files/" → under {projectDir}/uploaded_files/
//   3. path relative → under {projectDir}/{path}
//   4. fall back to {projectDir}/uploaded_files/{basename}
func resolveContractPath(filePath string, state *CrafterState, sess *SessionData) string {
	if filepath.IsAbs(filePath) {
		return filePath
	}

	// Determine project dir
	var projectDir string
	if state != nil && state.ProjectDir != "" {
		projectDir = state.ProjectDir
	} else if sess != nil && sess.ActiveProject != "" {
		projectDir = filepath.Join(baseDir, ".gem-squared", "workspace", sess.ActiveProject)
	} else {
		return filePath // last resort — caller will see "not found"
	}

	// Try as relative-to-project
	cand := filepath.Join(projectDir, filePath)
	if _, err := os.Stat(cand); err == nil {
		return cand
	}

	// Try under uploaded_files/
	cand = filepath.Join(projectDir, "uploaded_files", filepath.Base(filePath))
	if _, err := os.Stat(cand); err == nil {
		return cand
	}

	// Try uploaded_files/ with the supplied subpath
	if !strings.HasPrefix(filePath, "uploaded_files/") {
		cand = filepath.Join(projectDir, "uploaded_files", filePath)
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}

	// Return the relative-to-project guess; stat() will fail with a clear message
	return filepath.Join(projectDir, filePath)
}

func summarize(v interface{}, max int) string {
	if v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		b, _ := json.Marshal(v)
		s = string(b)
	}
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

func formatCEGenOutput(spec *CESpec, registryPath, apiEndpointURL, viewerURL string, p CEGenPreview) string {
	var b strings.Builder
	b.WriteString("## CE Created — immediately runnable\n\n")
	b.WriteString(fmt.Sprintf("**Contract:** %s\n", p.ContractTitle))
	b.WriteString(fmt.Sprintf("**CE slug:** `%s/%s`\n", spec.WorkflowSlug, spec.StageSlug))
	b.WriteString(fmt.Sprintf("**Vultr model:** `%s`\n", p.VultrModel))
	b.WriteString("\n### Contract preview\n")
	b.WriteString(fmt.Sprintf("- **A (input type):** %s\n", truncateOneLine(p.ASummary, 120)))
	b.WriteString(fmt.Sprintf("- **B (output type):** %s\n", truncateOneLine(p.BSummary, 120)))
	b.WriteString(fmt.Sprintf("- **P_pre rules:** %d\n", p.PPreCount))
	b.WriteString(fmt.Sprintf("- **P_post rules:** %d\n", p.PPostCount))
	b.WriteString(fmt.Sprintf("- **Trust gates:** L1=%d / L2=%d\n", p.TrustGateL1, p.TrustGateL2))
	b.WriteString("\n### API endpoint (curl/programs)\n")
	b.WriteString(fmt.Sprintf("`%s`\n", apiEndpointURL))
	b.WriteString(fmt.Sprintf("Registry: `%s`\n", registryPath))
	if viewerURL != "" {
		// Renders as a big animated amber button via the [[CE_VIEWER_BUTTON|url]]
		// token in crafter-app.js formatContent — see static/crafter.css for the
		// pulse-glow animation and .ce-viewer-cta styles.
		b.WriteString(fmt.Sprintf("\n[[CE_VIEWER_BUTTON|%s]]\n", viewerURL))
	}
	return b.String()
}

func truncateOneLine(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
