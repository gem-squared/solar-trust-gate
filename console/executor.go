package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type SkillExecResult struct {
	Skill         string   `json:"skill"`
	OutputB       string   `json:"output_b"`
	StateChange   string   `json:"state_change,omitempty"`
	Duration      int64    `json:"duration_ms"`
	FilesModified []string `json:"files_modified,omitempty"`
}

// resolveCrafterModel returns the model used for AI-Pilot skill execution
// (plan-work, proceed-work, verify-work, deploy-work, create-project, etc.).
// This is the CRAFTER LLM, NOT the Agent LLM. The Agent LLM (sess.AgentModel)
// is reserved for the Contract-Executor runtime at /ce/{wf}/{stage}/.
// See memory: project_crafter_vs_agent_llm.md for the strict role split.
// WP-AO-44 — corrected from the WP-AO-36 naming trap.
func resolveCrafterModel(sess *SessionData) string {
	if sess != nil && sess.Model != "" {
		return sess.Model
	}
	if env := os.Getenv("GEM2_DEFAULT_LLM"); env != "" {
		return env
	}
	// Default: Solar Pro 3 when UPSTAGE_API_KEY is set; fallback to Gemini.
	if solarAPIKey() != "" {
		return envOr("UPSTAGE_MODEL", "solar-pro3-260323")
	}
	return "gemini-2.5-pro"
}

func isVultrModel(model string) bool {
	return strings.HasPrefix(model, "vultr/")
}

func vultrModelName(model string) string {
	return strings.TrimPrefix(model, "vultr/")
}

func isSolarModel(model string) bool {
	return strings.HasPrefix(model, "solar/") || model == "solar-pro3" || model == "solar-pro" || model == "upstage/"
}

// agentGenerate routes to Solar, Gemini, or Vultr depending on the model.
// Solar is the default when UPSTAGE_API_KEY is set and no model prefix given.
func agentGenerate(agentModel, prompt string) (string, error) {
	if isVultrModel(agentModel) {
		vultrKey := os.Getenv("VULTR_INFERENCE_API_KEY")
		if vultrKey == "" {
			return "", fmt.Errorf("VULTR_INFERENCE_API_KEY not set for model %s", agentModel)
		}
		return vultrFreeformCall(vultrKey, vultrModelName(agentModel),
			"You are an AI agent executing a TPMN skill. Follow instructions precisely. Output markdown when the prompt asks for markdown; output JSON only when the prompt explicitly asks for JSON.",
			prompt)
	}
	if isSolarModel(agentModel) || (agentModel == "" && solarAPIKey() != "") {
		model := agentModel
		if model == "" {
			model = envOr("UPSTAGE_MODEL", "solar-pro3")
		}
		model = strings.TrimPrefix(model, "solar/")
		return solarChat(model, prompt, 120*time.Second)
	}
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("GEMINI_API_KEY not set (and no UPSTAGE_API_KEY for solar)")
	}
	return geminiGenerate(apiKey, agentModel, prompt)
}

func ExecuteSkill(skill *SkillSpec, args map[string]any, state *CrafterState, sess *SessionData) (*SkillExecResult, error) {
	start := time.Now()

	if sess != nil && sess.IsCancelled() {
		return nil, fmt.Errorf("cancelled before executing /%s", skill.Name)
	}

	apiKey := os.Getenv("GEMINI_API_KEY")
	vultrKey := os.Getenv("VULTR_INFERENCE_API_KEY")
	if apiKey == "" && vultrKey == "" {
		return nil, fmt.Errorf("neither GEMINI_API_KEY nor VULTR_INFERENCE_API_KEY set")
	}

	// WP-AO-44: skill execution uses the CRAFTER LLM (sess.Model), NOT the
	// AGENT LLM (sess.AgentModel). The variable retains the historic name
	// `agentModel` for call-site stability — see resolveCrafterModel docs.
	agentModel := resolveCrafterModel(sess)

	switch skill.Name {
	case "check-session":
		return executeCheckSession(state, start)
	case "plan-work":
		return executePlanWork(skill, args, state, apiKey, agentModel, start)
	case "proceed-work":
		return executeProceedWork(skill, args, state, sess, apiKey, agentModel, start)
	case "verify-work":
		return executeVerifyWork(skill, args, state, apiKey, agentModel, start)
	case "create-project":
		return executeCreateProject(args, state, sess, start)
	case "create-ce":
		return executeCreateCE(args, state, sess, start)
	case "deploy-work":
		return executeDeployWork(args, state, sess, start)
	default:
		return executeGeneric(skill, args, state, apiKey, agentModel, start)
	}
}

func executeCheckSession(state *CrafterState, start time.Time) (*SkillExecResult, error) {
	state.Refresh()

	var b strings.Builder
	b.WriteString("## Session Status\n\n")
	b.WriteString(state.Summary())
	b.WriteString("\n### Work Plan Details\n")
	for _, wp := range state.WorkPlans {
		b.WriteString(fmt.Sprintf("- **%s**: %s\n  STATUS: %s | Units: %d/%d completed",
			wp.ID, wp.Title, wp.Status, wp.Completed, wp.UnitCount))
		if wp.InProgress > 0 {
			b.WriteString(fmt.Sprintf(" | %d in progress", wp.InProgress))
		}
		if wp.Pending > 0 {
			b.WriteString(fmt.Sprintf(" | %d pending", wp.Pending))
		}
		b.WriteString("\n")
	}

	return &SkillExecResult{
		Skill:    "check-session",
		OutputB:  b.String(),
		Duration: time.Since(start).Milliseconds(),
	}, nil
}

func executePlanWork(skill *SkillSpec, args map[string]any, state *CrafterState, apiKey, agentModel string, start time.Time) (*SkillExecResult, error) {
	work, _ := args["work"].(string)
	if work == "" {
		return nil, fmt.Errorf("plan-work requires 'work' argument")
	}

	state.Refresh()
	wpNum := state.NextWPNumber()
	wpID := fmt.Sprintf("WP-ST-%d", wpNum)
	now := time.Now().Format(time.RFC3339)

	prompt := buildPlanWorkPrompt(skill, work, wpID, now, state)
	if uploaded := readUploadedFilesContext(state.ProjectDir); uploaded != "" {
		prompt += uploaded
	}
	raw, err := agentGenerate(agentModel, prompt)
	if err != nil {
		return nil, fmt.Errorf("plan-work LLM call: %w", err)
	}

	wpContent := extractWorkPlanContent(raw, wpID, work, now, filepath.Base(state.ProjectDir))

	filename := fmt.Sprintf("WP-ST-%d.md", wpNum)
	wpDir := state.WorkPlanDir()
	os.MkdirAll(wpDir, 0755)
	wpPath := filepath.Join(wpDir, filename)
	if err := os.WriteFile(wpPath, []byte(wpContent), 0644); err != nil {
		return nil, fmt.Errorf("write work plan: %w", err)
	}

	state.UpdateAlarmCounters()

	return &SkillExecResult{
		Skill:         "plan-work",
		OutputB:       fmt.Sprintf("Created work plan **%s** with unit-works.\n\n%s", wpID, wpContent),
		StateChange:   fmt.Sprintf("Created %s", wpPath),
		Duration:      time.Since(start).Milliseconds(),
		FilesModified: []string{wpPath},
	}, nil
}

func executeProceedWork(skill *SkillSpec, args map[string]any, state *CrafterState, sess *SessionData, apiKey, agentModel string, start time.Time) (*SkillExecResult, error) {
	state.Refresh()

	wpID, _ := args["wp_id"].(string)
	workTitle, _ := args["work_title"].(string)

	var targetWP *WorkPlanSummary
	for i := range state.WorkPlans {
		wp := &state.WorkPlans[i]
		if wpID != "" && wp.ID == wpID {
			targetWP = wp
			break
		}
		if workTitle != "" && strings.Contains(strings.ToLower(wp.Title), strings.ToLower(workTitle)) {
			targetWP = wp
			break
		}
	}

	if targetWP == nil {
		for i := range state.WorkPlans {
			wp := &state.WorkPlans[i]
			if wp.Status == "IN_PROGRESS" || wp.Status == "PENDING" {
				targetWP = wp
				break
			}
		}
	}

	if targetWP == nil {
		return nil, fmt.Errorf("no active or pending work plan found")
	}

	wpContent, err := os.ReadFile(targetWP.FilePath)
	if err != nil {
		return nil, fmt.Errorf("read work plan: %w", err)
	}

	content := string(wpContent)
	unitIdx, unitTitle := findNextPendingUnit(content)
	if unitIdx == 0 {
		return &SkillExecResult{
			Skill:    "proceed-work",
			OutputB:  fmt.Sprintf("All units in %s are completed. Consider running /verify-work.", targetWP.ID),
			Duration: time.Since(start).Milliseconds(),
		}, nil
	}

	unitBlock := extractUnitBlock(content, unitIdx)

	// WP-AO-45: atomic-skill fast-path dispatcher.
	// When a unit's contract specifies an explicit slash-skill action like
	// /create-ce, dispatch DIRECTLY to the skill's executor — bypass the
	// Gemini tool-call dance. The LLM has no /create-ce function declared
	// as a tool, so without this fast-path it hallucinates file-write tool
	// calls (the 2026-05-18 retest produced fabricated YAML files instead
	// of an actual CE registry entry). The planning already happened in
	// plan-work — proceed-work just dispatches.
	// WP-AO-64: pass full WP content + recent user messages as fallback
	// haystacks so the user's "name X" directive is honored even when
	// Gemini's plan-work output paraphrases the user's literal slug.
	dispatchHaystacks := []string{unitBlock, content}
	if sess != nil {
		// Walk history in reverse — newest user messages first so the
		// most recent CE-rename intent wins on ambiguity.
		for i := len(sess.History) - 1; i >= 0; i-- {
			if sess.History[i].Role == "user" {
				dispatchHaystacks = append(dispatchHaystacks, sess.History[i].Content)
			}
		}
	}
	if skillResult, dispatched := dispatchAtomicSkillUnitMulti(dispatchHaystacks, state, sess); dispatched {
		log.Printf("[PROCEED] atomic-skill fast-path → /%s for unit %d of %s", skillResult.Skill, unitIdx, targetWP.ID)
		// Compose proceed-work result wrapping the dispatched skill's output.
		var rb strings.Builder
		rb.WriteString(fmt.Sprintf("Executed unit %d (%s) of %s via atomic-skill fast-path /%s.\n\n",
			unitIdx, unitTitle, targetWP.ID, skillResult.Skill))
		rb.WriteString(skillResult.OutputB)
		composed := rb.String()
		// Archive prior Result + mark unit COMPLETED in the WP file
		if prior := extractExistingResult(content, unitIdx); prior != "" {
			archiveResult(state, targetWP.ID, unitIdx, prior)
		}
		updatedContent := markUnitCompleted(content, unitIdx, composed)
		if err := os.WriteFile(targetWP.FilePath, []byte(updatedContent), 0644); err != nil {
			return nil, fmt.Errorf("update work plan: %w", err)
		}
		state.UpdateAlarmCounters()
		return &SkillExecResult{
			Skill:         "proceed-work",
			OutputB:       composed,
			StateChange:   fmt.Sprintf("Updated unit %d in %s → COMPLETED (via /%s fast-path)", unitIdx, targetWP.ID, skillResult.Skill),
			Duration:      time.Since(start).Milliseconds(),
			FilesModified: skillResult.FilesModified,
		}, nil
	}

	projectSlug := filepath.Base(state.ProjectDir)
	workspaceDir := filepath.Join(state.WorkspacePath(), projectSlug)
	os.MkdirAll(workspaceDir, 0755)

	log.Printf("[PROCEED] executing %s unit %d (model: %s, workspace: %s)", targetWP.ID, unitIdx, agentModel, workspaceDir)

	prompt := buildProceedToolPrompt(targetWP.ID, unitIdx, unitTitle, unitBlock, content)
	if retryCtx, ok := args["retry_context"].(string); ok && retryCtx != "" {
		prompt += "\n\n## RETRY — PREVIOUS FAILURE CONTEXT\n" + retryCtx + "\n\nYou MUST fix the issues described above. Do NOT repeat the same mistake.\n"
	}
	if uploaded := readUploadedFilesContext(state.ProjectDir); uploaded != "" {
		prompt += uploaded
	}

	var raw string
	var callLog []ToolCallLog

	if isVultrModel(agentModel) {
		var vultrErr error
		raw, vultrErr = agentGenerate(agentModel, prompt)
		if vultrErr != nil {
			return nil, fmt.Errorf("proceed-work vultr call: %w", vultrErr)
		}
	} else {
		tools := workspaceTools()
		executor := workspaceToolExecutorWithSlugStrip(workspaceDir, projectSlug)
		var toolErr error
		raw, callLog, toolErr = geminiToolCall(apiKey, agentModel, prompt, tools, executor)
		if toolErr != nil {
			return nil, fmt.Errorf("proceed-work tool-call: %w", toolErr)
		}
	}

	var filesCreated []string
	for _, call := range callLog {
		if (call.Name == "create_file" || call.Name == "write_file") && !strings.Contains(call.Result, "error") {
			if p, ok := call.Args["path"].(string); ok {
				filesCreated = append(filesCreated, p)
			}
		}
	}

	var resultText strings.Builder
	resultText.WriteString(fmt.Sprintf("Executed unit %d (%s) of %s via Gemini function calling.\n\n", unitIdx, unitTitle, targetWP.ID))
	if len(filesCreated) > 0 {
		resultText.WriteString("**Artifacts:**\n")
		for _, f := range filesCreated {
			absPath := filepath.Join(workspaceDir, f)
			info, statErr := os.Stat(absPath)
			if statErr == nil {
				resultText.WriteString(fmt.Sprintf("- `%s` (%s) → `%s`\n", f, formatBytes(info.Size()), absPath))
			} else {
				resultText.WriteString(fmt.Sprintf("- `%s` → `%s`\n", f, absPath))
			}
		}
		// WP-AO-28 Unit 1 — dynamic per-file budget, max 2 MB/file, 20 MB total.
		// Replaces prior hardcoded 300-char per-file truncate that starved
		// verify-work evidence. See WP-AO-28 forensic.
		perFileCap, totalCap := fileEvidenceBudget(agentModel, len(filesCreated))
		accumulated := 0
		resultText.WriteString("\n**File contents (evidence):**\n")
		for _, f := range filesCreated {
			absPath := filepath.Join(workspaceDir, f)
			fileContent, readErr := os.ReadFile(absPath)
			if readErr != nil {
				continue
			}
			if accumulated >= totalCap {
				resultText.WriteString(fmt.Sprintf("\n`%s`: [file omitted — total evidence cap (%s) reached]\n", f, formatBytes(int64(totalCap))))
				continue
			}
			snippet := string(fileContent)
			origLen := len(snippet)
			if origLen > perFileCap {
				snippet = snippet[:perFileCap] + fmt.Sprintf("\n...(truncated at %s per-file cap; original %s)", formatBytes(int64(perFileCap)), formatBytes(int64(origLen)))
			}
			remain := totalCap - accumulated
			if len(snippet) > remain {
				snippet = snippet[:remain] + "\n...(truncated — total evidence cap reached)"
			}
			resultText.WriteString(fmt.Sprintf("\n`%s`:\n```\n%s\n```\n", f, snippet))
			accumulated += len(snippet)
		}
	}
	resultText.WriteString(fmt.Sprintf("**Tool calls:** %d\n\n", len(callLog)))
	if raw != "" {
		resultText.WriteString("**Summary:**\n")
		// WP-AO-28 Unit 2 — dynamic summary cap by model budget (was 500-char hardcode).
		resultText.WriteString(truncate(raw, summaryCap(agentModel)))
	}

	result := &SkillExecResult{
		Skill:         "proceed-work",
		OutputB:       resultText.String(),
		StateChange:   fmt.Sprintf("Updated unit %d in %s → COMPLETED", unitIdx, targetWP.ID),
		Duration:      time.Since(start).Milliseconds(),
		FilesModified: filesCreated,
	}

	// WP-AO-28 Unit 3 — archive any prior Result content before overwriting.
	if prior := extractExistingResult(content, unitIdx); prior != "" {
		archiveResult(state, targetWP.ID, unitIdx, prior)
	}
	updatedContent := markUnitCompleted(content, unitIdx, resultText.String())
	if err := os.WriteFile(targetWP.FilePath, []byte(updatedContent), 0644); err != nil {
		return nil, fmt.Errorf("update work plan: %w", err)
	}
	state.UpdateAlarmCounters()
	return result, nil
}

func executeVerifyWork(skill *SkillSpec, args map[string]any, state *CrafterState, apiKey, agentModel string, start time.Time) (*SkillExecResult, error) {
	state.Refresh()

	wpID, _ := args["wp_id"].(string)
	workTitle, _ := args["work_title"].(string)
	// WP-AO-29 — honor unit_index for single-unit mode. When > 0, scope verify
	// to ONLY that unit (no Overall: line, no cross-unit contamination).
	// When 0/missing, BATCH mode preserved.
	requestedUnitIdx := 0
	if v, ok := args["unit_index"].(float64); ok {
		requestedUnitIdx = int(v)
	} else if v, ok := args["unit_index"].(int); ok {
		requestedUnitIdx = v
	}
	var targetWP *WorkPlanSummary
	for i := range state.WorkPlans {
		wp := &state.WorkPlans[i]
		if wpID != "" && wp.ID == wpID {
			targetWP = wp
			break
		}
		if workTitle != "" && strings.Contains(strings.ToLower(wp.Title), strings.ToLower(workTitle)) {
			targetWP = wp
			break
		}
	}
	if targetWP == nil {
		// WP-AO-47 Unit 2: defensive WP selection for verify-work — iterate
		// in REVERSE so the NEWEST eligible WP wins. Prefer one with
		// completed units; fall back to in-progress. Without this, verify
		// always targeted the oldest matching WP-ST-1, ignoring fresh ones.
		for i := len(state.WorkPlans) - 1; i >= 0; i-- {
			wp := &state.WorkPlans[i]
			if wp.Completed > 0 {
				targetWP = wp
				break
			}
		}
		if targetWP == nil {
			for i := len(state.WorkPlans) - 1; i >= 0; i-- {
				wp := &state.WorkPlans[i]
				if wp.Status == "IN_PROGRESS" {
					targetWP = wp
					break
				}
			}
		}
	}
	if targetWP == nil {
		return nil, fmt.Errorf("no work plan found to verify")
	}

	wpData, err := os.ReadFile(targetWP.FilePath)
	if err != nil {
		return nil, fmt.Errorf("read work plan: %w", err)
	}
	content := string(wpData)
	verifiedAt := time.Now().Format(time.RFC3339)

	units := parseUnitBlocks(content)
	if len(units) == 0 {
		return nil, fmt.Errorf("no units found in %s", targetWP.ID)
	}

	outputDir := filepath.Join(state.WorkspacePath(), filepath.Base(state.ProjectDir))
	var outputFiles []string
	filepath.Walk(outputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(outputDir, path)
		outputFiles = append(outputFiles, rel)
		return nil
	})
	outputInventory := strings.Join(outputFiles, ", ")

	var verifiedUnits []unitVerification
	successCount, failCount, skipCount := 0, 0, 0

	for _, u := range units {
		// WP-AO-29 single-unit scope — skip everything except the requested unit
		if requestedUnitIdx > 0 && u.index != requestedUnitIdx {
			continue
		}
		if u.status == "ABORTED" {
			skipCount++
			continue
		}
		if u.status != "COMPLETED" {
			skipCount++
			continue
		}

		// WP-AO-46 Unit 1: deterministic verify for atomic-skill units.
		// If proceed-work used the WP-AO-45 fast-path, the unit's Result
		// includes the marker "via atomic-skill fast-path /<skill>". For
		// known skills with deterministic side effects, check the side
		// effect on disk and PASS without invoking the LLM. This avoids
		// the LLM hallucinating "missing from disk" when the actual
		// artifact lives outside output/{slug}/ (e.g., the CE registry
		// at .gem-squared/ce-registry/{wf}/{stage}.json).
		atomicMarker := regexp.MustCompile(`via atomic-skill fast-path /(\S+)`)
		if m := atomicMarker.FindStringSubmatch(u.result); len(m) > 1 {
			skillName := m[1]
			if skillName == "create-ce" {
				ceSlugRe := regexp.MustCompile(`\*\*CE slug:\*\*\s*` + "`" + `([^/` + "`" + `]+)/([^` + "`" + `]+)` + "`")
				slugMatch := ceSlugRe.FindStringSubmatch(u.result)
				if len(slugMatch) == 3 {
					wf, stage := slugMatch[1], slugMatch[2]
					regPath := filepath.Join(baseDir, ".gem-squared", "ce-registry", wf, stage+".json")
					if _, err := os.Stat(regPath); err == nil {
						verifiedUnits = append(verifiedUnits, unitVerification{
							index: u.index, title: u.title, state: "SUCCESS",
							detail: fmt.Sprintf("Atomic-skill /create-ce executed; CE registered at %s/%s. Registry entry verified on disk: %s. Deterministic verify (no LLM).", wf, stage, regPath),
						})
						successCount++
						continue
					}
					verifiedUnits = append(verifiedUnits, unitVerification{
						index: u.index, title: u.title, state: "FAILURE",
						detail: fmt.Sprintf("Atomic-skill /create-ce claimed success but registry entry missing at %s. Deterministic verify failed.", regPath),
					})
					failCount++
					continue
				}
			}
			// Future atomic skills append cases above; unknown skill falls through to LLM.
		}

		prompt := fmt.Sprintf(`You are a TPMN verification agent. Verify ONE unit-work Result against its CONTRACT.

## Unit %d: %s

### CONTRACT.B (expected output)
%s

### CONTRACT.P (preconditions)
%s

### Clarity
%s

### Actual Result
%s

### Files in output/ directory
%s

## Verification Rules
Focus on whether the ACTUAL WORK was accomplished, not whether the Result text literally mirrors CONTRACT.B wording.

1. Field coverage: Were the deliverables described in CONTRACT.B actually produced? (e.g., if B says "a package.json file", and Result says "Files created: package.json" — that is a PASS)
2. Type conformance: Are the outputs the correct type/format? (files are files, configs are configs, etc.)
3. Constraint satisfaction: Were preconditions P reasonably met?
4. Completeness: Given the Clarity %%, is the work sufficiently complete?

IMPORTANT: If files were created and the work described in CONTRACT.B was done, mark SUCCESS even if the Result text uses different wording than CONTRACT.B. The Result is a summary of tool calls, not a copy of CONTRACT.B.

Respond with ONLY valid JSON:
{
  "state": "SUCCESS" or "FAILURE",
  "field_coverage": true/false,
  "type_conformance": true/false,
  "constraint_satisfaction": true/false,
  "completeness_vs_clarity": "assessment of result completeness relative to clarity %%",
  "detail": "specific explanation of what passed or failed"
}`, u.index, u.title, u.contractB, u.contractP, u.clarity, u.result, outputInventory)

		raw, err := agentGenerate(agentModel, prompt)
		if err != nil {
			verifiedUnits = append(verifiedUnits, unitVerification{
				index: u.index, title: u.title, state: "FAILURE",
				detail: fmt.Sprintf("Verification call failed: %v", err),
			})
			failCount++
			continue
		}

		v := parseVerificationResult(raw, u)
		verifiedUnits = append(verifiedUnits, v)
		if v.state == "SUCCESS" {
			successCount++
		} else {
			failCount++
		}
	}

	overallState := "SUCCESS"
	if failCount > 0 {
		overallState = "FAILURE"
	}

	// Write State per unit in WP file + enrich Result with artifact inventory on SUCCESS
	workspaceDir := filepath.Join(state.WorkspacePath(), filepath.Base(state.ProjectDir))
	updatedContent := content
	for _, v := range verifiedUnits {
		stateRe := regexp.MustCompile(fmt.Sprintf(`(?m)(### %d\..*\n(?:.*\n)*?- State:)(.*)`, v.index))
		updatedContent = stateRe.ReplaceAllString(updatedContent, "${1} "+v.state)

		if v.state == "SUCCESS" {
			inventory := buildArtifactInventory(updatedContent, v.index, workspaceDir)
			if inventory != "" {
				resultRe := regexp.MustCompile(fmt.Sprintf(`(?m)(### %d\..*\n(?:.*\n)*?- Result:)(.*)`, v.index))
				if m := resultRe.FindStringIndex(updatedContent); m != nil {
					existingResult := updatedContent[m[0]:m[1]]
					eolIdx := strings.Index(existingResult, "\n- State:")
					if eolIdx < 0 {
						eolIdx = len(existingResult)
					}
					insertPoint := m[0] + eolIdx
					updatedContent = updatedContent[:insertPoint] + "\n  **Verified artifacts:**\n" + inventory + updatedContent[insertPoint:]
				}
			}
		}
	}
	os.WriteFile(targetWP.FilePath, []byte(updatedContent), 0644)

	// Write verification log
	var log strings.Builder
	log.WriteString(fmt.Sprintf("# Verification Log: %s\n", targetWP.ID))
	log.WriteString(fmt.Sprintf("**WP:** %s | **Verified:** %s\n", targetWP.Title, verifiedAt))
	if requestedUnitIdx > 0 {
		// WP-AO-29 single-unit mode — no "Overall" line; just the one unit's verdict.
		log.WriteString(fmt.Sprintf("**Mode:** single-unit (unit %d) | **Result:** %s\n\n",
			requestedUnitIdx, overallState))
	} else {
		log.WriteString(fmt.Sprintf("**Overall:** %s | **Units verified:** %d | **Skipped:** %d\n\n",
			overallState, len(verifiedUnits), skipCount))
	}

	for _, v := range verifiedUnits {
		log.WriteString(fmt.Sprintf("## Unit %d: %s — %s\n", v.index, v.title, v.state))
		log.WriteString(fmt.Sprintf("**Detail:** %s\n\n", v.detail))
	}

	log.WriteString("## Summary\n")
	if requestedUnitIdx > 0 {
		log.WriteString(fmt.Sprintf("Unit %d %s.\n", requestedUnitIdx, overallState))
	} else if overallState == "SUCCESS" {
		log.WriteString(fmt.Sprintf("All %d verified units passed. Ready for /archive-work.\n", successCount))
	} else {
		log.WriteString(fmt.Sprintf("%d passed, %d failed. Review failures before archiving.\n", successCount, failCount))
	}

	verifyLogDir := filepath.Join(state.ProjectDir, "verify-work-logs")
	os.MkdirAll(verifyLogDir, 0755)
	logPath := filepath.Join(verifyLogDir, targetWP.ID+".md")
	os.WriteFile(logPath, []byte(log.String()), 0644)

	// Build output
	var output strings.Builder
	output.WriteString(fmt.Sprintf("## Verification: %s — **%s**\n\n", targetWP.ID, overallState))
	output.WriteString(fmt.Sprintf("Verified %d units | %d passed | %d failed | %d skipped\n\n",
		len(verifiedUnits), successCount, failCount, skipCount))
	for _, v := range verifiedUnits {
		icon := "PASS"
		if v.state == "FAILURE" {
			icon = "FAIL"
		}
		output.WriteString(fmt.Sprintf("### Unit %d: %s — %s\n%s\n\n", v.index, v.title, icon, v.detail))
	}
	if overallState == "SUCCESS" {
		output.WriteString("**Next:** Run /archive-work to finalize.")
	} else {
		output.WriteString("**Next:** Fix failed units with /proceed-work, then re-verify.")
	}

	return &SkillExecResult{
		Skill:         "verify-work",
		OutputB:       output.String(),
		StateChange:   fmt.Sprintf("Verified %s → %s", targetWP.ID, overallState),
		Duration:      time.Since(start).Milliseconds(),
		FilesModified: []string{targetWP.FilePath, ".gem-squared/" + logPath},
	}, nil
}

type unitBlock struct {
	index    int
	title    string
	status   string
	contractA string
	contractB string
	contractP string
	clarity  string
	result   string
}

type unitVerification struct {
	index  int
	title  string
	state  string
	detail string
}

func parseUnitBlocks(content string) []unitBlock {
	re := regexp.MustCompile(`### (\d+)\.\s+(.+?)\s*\|\s*STATUS:\s*(\w+)`)
	matches := re.FindAllStringSubmatchIndex(content, -1)
	var units []unitBlock

	for i, m := range matches {
		var idx int
		fmt.Sscanf(content[m[2]:m[3]], "%d", &idx)
		title := content[m[4]:m[5]]
		status := content[m[6]:m[7]]

		blockEnd := len(content)
		if i+1 < len(matches) {
			blockEnd = matches[i+1][0]
		} else {
			if sepIdx := strings.Index(content[m[0]:], "\n## "); sepIdx > 0 {
				blockEnd = m[0] + sepIdx
			}
		}
		block := content[m[0]:blockEnd]

		u := unitBlock{
			index:  idx,
			title:  title,
			status: status,
		}
		u.contractA = extractField(block, "A:")
		u.contractB = extractField(block, "B:")
		u.contractP = extractField(block, "P:")
		u.clarity = extractField(block, "Clarity:")
		u.result = extractField(block, "Result:")
		units = append(units, u)
	}
	return units
}

func extractField(block, prefix string) string {
	topLevelFields := []string{"- **A:", "- **B:", "- **P:", "- A:", "- B:", "- P:",
		"- Clarity:", "- Unclear:", "- Tags:", "- Result:", "- State:", "- Truth:"}
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- **"+prefix) || strings.HasPrefix(trimmed, "- "+prefix) {
			val := trimmed
			if idx := strings.Index(val, prefix); idx >= 0 {
				val = strings.TrimSpace(val[idx+len(prefix):])
			}
			for j := i + 1; j < len(lines); j++ {
				next := lines[j]
				if !strings.HasPrefix(next, "  ") && !strings.HasPrefix(next, "\t") {
					break
				}
				nextTrimmed := strings.TrimSpace(next)
				isTopLevel := false
				for _, f := range topLevelFields {
					if strings.HasPrefix(nextTrimmed, f) {
						isTopLevel = true
						break
					}
				}
				if isTopLevel {
					break
				}
				val += "\n" + strings.TrimRight(next, " \t")
			}
			return val
		}
	}
	return ""
}

func parseVerificationResult(raw string, u unitBlock) unitVerification {
	v := unitVerification{index: u.index, title: u.title}

	cleaned := raw
	if idx := strings.Index(cleaned, "{"); idx >= 0 {
		cleaned = cleaned[idx:]
	}
	if idx := strings.LastIndex(cleaned, "}"); idx >= 0 {
		cleaned = cleaned[:idx+1]
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		v.state = "FAILURE"
		v.detail = fmt.Sprintf("Could not parse verification response. Raw: %s", truncate(raw, 300))
		return v
	}

	if s, ok := parsed["state"].(string); ok {
		v.state = s
	} else {
		v.state = "FAILURE"
	}

	detail := ""
	if d, ok := parsed["detail"].(string); ok {
		detail = d
	}
	if c, ok := parsed["completeness_vs_clarity"].(string); ok && c != "" {
		detail += " | Clarity assessment: " + c
	}
	if fc, ok := parsed["field_coverage"].(bool); ok && !fc {
		detail += " | MISSING: field coverage failed"
	}
	if cs, ok := parsed["constraint_satisfaction"].(bool); ok && !cs {
		detail += " | MISSING: constraint satisfaction failed"
	}
	v.detail = detail
	return v
}

func executeGeneric(skill *SkillSpec, args map[string]any, state *CrafterState, apiKey, agentModel string, start time.Time) (*SkillExecResult, error) {
	prompt := fmt.Sprintf(`You are executing the TPMN skill "/%s".

## Skill Definition
%s

## Current State
%s

## Arguments
%s

Execute this skill according to its contract. Provide the output B as described in the skill definition.
`, skill.Name, skill.RawBody, state.Summary(), formatArgs(args))

	raw, err := agentGenerate(agentModel, prompt)
	if err != nil {
		return nil, fmt.Errorf("%s LLM call: %w", skill.Name, err)
	}

	return &SkillExecResult{
		Skill:    skill.Name,
		OutputB:  raw,
		Duration: time.Since(start).Milliseconds(),
	}, nil
}

func buildPlanWorkPrompt(skill *SkillSpec, work, wpID, timestamp string, state *CrafterState) string {
	// WP-AO-42 P3 — CE-intent fast path. Detect Contract-Executor creation
	// intent and emit a SPECIAL 1-unit prompt that overrides the generic
	// 3-7-units + FILE-CREATION-ONLY constraints below (which steer the
	// LLM into generic file-gen units instead of /create-ce). The plan-work
	// SKILL.md has the CE template guidance but its body is buried; this
	// outer-prompt override is what actually controls LLM behavior.
	lwork := strings.ToLower(work)
	if strings.Contains(lwork, "create-ce") || strings.Contains(lwork, "/create-ce") || strings.Contains(lwork, "create a ce") {
		return fmt.Sprintf(`You are a TPMN work planning agent producing a MINIMAL 1-unit work plan for a Contract-Executor (CE) creation request.

## Task
Plan the following work: %s

## Current Project State
%s

## CE-Intent Fast Path (WP-AO-42 P3)
This is a CE-creation intent. Output EXACTLY ONE unit-work whose action
invokes "/create-ce" on the uploaded contract. DO NOT decompose into
multiple file-creation units. The /create-ce skill itself handles the
contract parsing, registry write, sample-input generation, and viewer URL
emission as one atomic step — your planning artifact is just the wrapper
that proceed-work uses to invoke it.

## Output Format
Write the work plan in this EXACT markdown format. Every "###" header
MUST end with "| STATUS: PENDING" — the system uses this to track units.
Write ONLY the markdown content, no code fences, no JSON wrapper.

# %s: %s
**STATUS:** PENDING | **STATE:** — | **task_id:** AUTO
**created_at:** %s | **project_slug:** %s

## Objective
Wrap the uploaded TPMN contract as a live Contract-Executor by invoking /create-ce.
The handler auto-detects the most recent contract in uploaded_files/ when file_path
is empty. On success it emits a viewer URL and registers the CE under /ce/{wf}/{stage}/.

## Unit-Works

### 1. Execute /create-ce on the uploaded contract | STATUS: PENDING
- **A:** TPMN contract markdown present in {ProjectDir}/uploaded_files/ (5-block format: ## A:, ## P_pre:, ## F:, ## B:, ## P_post:)
- **B:** Live CE registered at /ce/{wf}/{stage}/, viewer URL emitted to chat, sample input auto-generated
- **P:** active project bound to session, at least one TPMN contract markdown in uploaded_files/
- Clarity: 92%%
- Unclear: None — atomic action with auto-detect path resolution
- Tags: [executing-create-ce, registering-live-endpoint, emitting-viewer-url]
- Result:
- State:
- Truth:
`, work, state.Summary(), wpID, work, timestamp, filepath.Base(state.ProjectDir))
	}

	return fmt.Sprintf(`You are a TPMN work planning agent. Your task is to decompose work into unit-works with formal contracts.

## Skill Contract
%s

## Task
Plan the following work: %s

## Requirements
- Create %s as the work plan ID
- Decompose into 3-7 unit-works (Miller's law: 7±2)
- Each unit-work needs: title, A (input), B (output), P (preconditions), Clarity %%, Tags
- Tags format: {verb-ing}-{object} (e.g., "building-api", "testing-integration")
- Be specific and actionable

## CRITICAL EXECUTOR CONSTRAINT
The executor can ONLY create and write files. It CANNOT:
- Run shell commands (no npm install, no pip install, no make, etc.)
- Start processes (no node server.js, no python app.py, etc.)
- Install packages or dependencies
- Run tests or verification commands

Plan units around FILE CREATION ONLY. For example:
- GOOD: "Create package.json with Express dependency listed"
- BAD: "Run npm install to install Express"
- GOOD: "Create server.js with complete Express hello world server code"
- BAD: "Run the server and verify it responds with Hello World"

Combine logical steps into fewer file-creation units. A "hello world Express server" should be 2-3 units (create package.json, create server.js), not 6-7 units with install/run/verify steps.

## Current Project State
%s

## Output Format
Write a complete work plan in this exact markdown format:

# %s: {title}
**STATUS:** PENDING | **STATE:** — | **task_id:** {8-char-uuid}
**created_at:** %s | **project_slug:** %s

## Unit-Works

### 1. {unit title} | STATUS: PENDING
- **A:** {input state description}
- **B:** {output state description}
- **P:** {preconditions}
- Clarity: {N}%%
- Unclear: {what is ambiguous}
- Tags: [{tag1}, {tag2}]
- Result:
- State:
- Truth:

(repeat for each unit, 3-7 total)

IMPORTANT: Every unit header MUST end with "| STATUS: PENDING". Example: "### 1. Create server file | STATUS: PENDING". This is MANDATORY — the system uses this to track unit status.

Write ONLY the markdown content, no code fences.
`, skill.RawBody, work, wpID, state.Summary(), wpID, timestamp, filepath.Base(state.ProjectDir))
}

func buildProceedPrompt(skill *SkillSpec, wpID string, unitIdx int, unitTitle, unitBlock, fullWP string) string {
	return fmt.Sprintf(`You are a TPMN work execution agent. Execute ONE unit-work and produce its Result.

## Skill Contract
%s

## Work Plan: %s

## Target Unit: %d. %s

## Unit Contract
%s

## Full Work Plan Context
%s

## Instructions
Execute unit %d according to its contract:
1. Read the input A (what you start with)
2. Produce output B (what must exist after)
3. Verify precondition P holds
4. Write a detailed Result describing what was accomplished

Respond with ONLY the Result text — what was done, what was produced, key decisions made.
Keep it concrete and specific, under 500 words.
`, skill.RawBody, wpID, unitIdx, unitTitle, unitBlock, truncate(fullWP, 2000), unitIdx)
}

func buildProceedToolPrompt(wpID string, unitIdx int, unitTitle, unitBlock, fullWP string) string {
	return fmt.Sprintf(`You are a code execution agent with file tools. Execute ONE unit-work by creating real files.

## Work Plan: %s

## Target Unit: %d. %s

## Unit Contract
%s

## Full Work Plan Context
%s

## Instructions
1. Read the CONTRACT carefully: A is the starting state, B is what must exist after, P is preconditions.
2. Use the create_file tool to create ALL files needed to fulfill CONTRACT.B.
3. Write complete, working code — not stubs or placeholders.
4. Use list_files to verify your work after creating files.
5. After all files are created, provide a brief summary of what was accomplished.

IMPORTANT: You MUST use the create_file tool to create actual files. Do NOT just describe what files would look like.
IMPORTANT: File paths are relative to the project output/ directory ROOT. Do NOT prefix paths with the project name.
Good: create_file(path="index.html", ...) or create_file(path="src/game.js", ...)
Bad: create_file(path="flappy-bird/index.html", ...) — this creates an unwanted subdirectory.
`, wpID, unitIdx, unitTitle, unitBlock, truncate(fullWP, 2000))
}

func buildVerifyPrompt(skill *SkillSpec, wpID, wpContent string) string {
	return fmt.Sprintf(`You are a TPMN verification agent. Verify completed unit-works against their contracts.

## Skill Contract
%s

## Work Plan: %s
%s

## Instructions
For each COMPLETED unit-work:
1. Check if Result satisfies output B described in the contract
2. Check if precondition P was met
3. Assign a State: SUCCESS or FAILURE
4. Provide brief justification

Format your response as:

### Unit {N}: {title}
**State:** SUCCESS/FAILURE
**Justification:** {why}

Provide an overall summary at the end.
`, skill.RawBody, wpID, wpContent)
}

func extractWorkPlanContent(raw, wpID, work, timestamp, projectSlug string) string {
	cleaned := strings.TrimSpace(raw)
	if strings.HasPrefix(cleaned, "```") {
		lines := strings.Split(cleaned, "\n")
		start, end := 1, len(lines)-1
		if end > start && strings.HasPrefix(lines[end], "```") {
			cleaned = strings.Join(lines[start:end], "\n")
		}
	}

	if !strings.HasPrefix(cleaned, "# "+wpID) && !strings.HasPrefix(cleaned, "# WP-") {
		// WP-AO-42: projectSlug threaded through instead of hardcoded "crafter".
		cleaned = fmt.Sprintf("# %s: %s\n**STATUS:** PENDING | **STATE:** — | **task_id:** AUTO\n**created_at:** %s | **project_slug:** %s\n\n%s",
			wpID, work, timestamp, projectSlug, cleaned)
	}

	cleaned = ensureUnitStatusMarkers(cleaned)
	return cleaned
}

func ensureUnitStatusMarkers(content string) string {
	unitHeaderRe := regexp.MustCompile(`(?m)^(### \d+\..+?)(\s*\|\s*STATUS:\s*\w+)?$`)
	return unitHeaderRe.ReplaceAllStringFunc(content, func(line string) string {
		if strings.Contains(line, "STATUS:") {
			return line
		}
		return strings.TrimRight(line, " \t") + " | STATUS: PENDING"
	})
}

// dispatchAtomicSkillUnit (WP-AO-45) scans a unit block for explicit
// slash-skill action references and, if found, invokes the matching
// skill's executor directly — bypassing the LLM tool-call dance.
// Returns the skill's SkillExecResult + dispatched=true when matched,
// else (nil, false) to signal the caller should fall through to the
// LLM path. Currently dispatches /create-ce; extensible by adding cases.
func dispatchAtomicSkillUnit(unitBlock string, state *CrafterState, sess *SessionData) (*SkillExecResult, bool) {
	return dispatchAtomicSkillUnitMulti([]string{unitBlock}, state, sess)
}

// dispatchAtomicSkillUnitMulti runs the same fast-path but scans multiple
// haystacks in priority order: unit body, full WP, recent user messages.
// First match wins per arg-key.
func dispatchAtomicSkillUnitMulti(haystacks []string, state *CrafterState, sess *SessionData) (*SkillExecResult, bool) {
	if len(haystacks) == 0 {
		return nil, false
	}
	unitBlock := haystacks[0]
	low := strings.ToLower(unitBlock)
	// /create-ce dispatch — the unit's contract explicitly names this skill
	if strings.Contains(low, "/create-ce") || strings.Contains(low, "create-ce ") {
		args := map[string]any{
			"file_path": "", // auto-detect from uploaded_files/
		}
		// Best-effort arg extraction: if the unit mentions an explicit
		// file_path="<path>", pull that. Otherwise auto-detect handles it.
		if m := regexp.MustCompile(`file_path\s*[:=]\s*["']?([^"'\s\)]+)["']?`).FindStringSubmatch(unitBlock); len(m) > 1 && m[1] != `""` && m[1] != "" {
			args["file_path"] = m[1]
		}
		// WP-AO-63/64: extract stage_slug + workflow_slug across multiple
		// phrasings. The fast-path otherwise silently falls back to the
		// contract-declared slug, ignoring the user's rename intent. We
		// scan unit body first, then WP content, then recent user chat
		// messages — first-match-wins, with strict (slug-shaped) capture.
		stagePatterns := []*regexp.Regexp{
			// Explicit assignments — highest precedence
			regexp.MustCompile(`stage_slug\s*[:=]\s*["']?([a-z0-9][a-z0-9-]{0,62}[a-z0-9])["']?`),
			regexp.MustCompile(`ce[_-]name\s*[:=]\s*["']?([a-z0-9][a-z0-9-]{0,62}[a-z0-9])["']?`),
			regexp.MustCompile(`--ce[_-]name\s+["']?([a-z0-9][a-z0-9-]{0,62}[a-z0-9])["']?`),
			regexp.MustCompile(`--stage[_-]slug\s+["']?([a-z0-9][a-z0-9-]{0,62}[a-z0-9])["']?`),
			// Quoted natural-language phrasings
			regexp.MustCompile(`naming\s+the\s+stage\s+['"]([a-z0-9][a-z0-9-]{0,62}[a-z0-9])['"]`),
			regexp.MustCompile(`stage\s*[:=]\s*['"]([a-z0-9][a-z0-9-]{0,62}[a-z0-9])['"]`),
			regexp.MustCompile(`named?\s+['"` + "`" + `]([a-z0-9][a-z0-9-]{0,62}[a-z0-9])['"` + "`" + `]`),
			regexp.MustCompile(`(?:as|to)\s+['"` + "`" + `]([a-z0-9][a-z0-9-]{0,62}[a-z0-9])['"` + "`" + `]`),
			regexp.MustCompile(`slug\s+['"` + "`" + `]([a-z0-9][a-z0-9-]{0,62}[a-z0-9])['"` + "`" + `]`),
			// Bare natural-language — must look slug-shaped (digit-led or
			// hyphenated to avoid catching common English words)
			regexp.MustCompile(`(?:name\s+(?:it\s+)?(?:as\s+)?)([0-9]+-[a-z][a-z0-9-]*)`),
			// "Name: 0001-intake" — bare label-style. Digit-led keeps it safe.
			regexp.MustCompile(`(?:^|[\s.])name\s*[:=]\s*["']?([0-9]+-[a-z][a-z0-9-]*)["']?`),
			regexp.MustCompile(`(?:named|call(?:ed)?|rename(?:d)?\s+to)\s+([0-9]+-[a-z][a-z0-9-]*)`),
			regexp.MustCompile(`(?:as|to)\s+([0-9]+-[a-z][a-z0-9-]*)\b`),
			// "Live CE registered at /ce/<workflow>/<stage>/"
			regexp.MustCompile(`/ce/[a-z0-9][a-z0-9-]*/([a-z0-9][a-z0-9-]{0,62}[a-z0-9])/`),
		}
		for _, hay := range haystacks {
			if _, found := args["stage_slug"]; found {
				break
			}
			hayLow := strings.ToLower(hay)
			for _, pat := range stagePatterns {
				if m := pat.FindStringSubmatch(hayLow); len(m) > 1 && m[1] != "" {
					args["stage_slug"] = m[1]
					log.Printf("[ATOMIC-DISPATCH] stage_slug=%q matched pat=%q", m[1], pat.String())
					break
				}
			}
		}
		workflowPatterns := []*regexp.Regexp{
			regexp.MustCompile(`workflow_slug\s*[:=]\s*["']?([a-z0-9][a-z0-9-]{0,62}[a-z0-9])["']?`),
			regexp.MustCompile(`workflow\s*[:=]\s*['"]([a-z0-9][a-z0-9-]{0,62}[a-z0-9])['"]`),
			regexp.MustCompile(`--workflow[_-]slug\s+["']?([a-z0-9][a-z0-9-]{0,62}[a-z0-9])["']?`),
			regexp.MustCompile(`/ce/([a-z0-9][a-z0-9-]{0,62}[a-z0-9])/[a-z0-9]`),
		}
		for _, hay := range haystacks {
			if _, found := args["workflow_slug"]; found {
				break
			}
			hayLow := strings.ToLower(hay)
			for _, pat := range workflowPatterns {
				if m := pat.FindStringSubmatch(hayLow); len(m) > 1 && m[1] != "" {
					args["workflow_slug"] = m[1]
					break
				}
			}
		}
		// User-intent default: when a custom stage_slug is supplied but no
		// workflow_slug is in any haystack, default workflow_slug to the
		// active project's basename. Without this, a custom CE goes under
		// the contract-declared workflow (e.g., "health-insurance-claim-
		// pipeline") even when the user is working in a different project
		// like "010-project" — leading to cross-project namespace collisions.
		if _, hasStage := args["stage_slug"]; hasStage {
			if _, hasWf := args["workflow_slug"]; !hasWf {
				if state != nil && state.ProjectDir != "" {
					projectBasename := filepath.Base(state.ProjectDir)
					if projectBasename != "" && projectBasename != "." && projectBasename != "/" {
						args["workflow_slug"] = projectBasename
					}
				}
			}
		}
		res, err := executeCreateCE(args, state, sess, time.Now())
		if err != nil {
			// WP-AO-63 fix: do NOT fall back to LLM on /create-ce error —
			// the LLM has no /create-ce tool, so it'll freestyle Python/JSON
			// files that aren't a CESpec. Surface the actual error to the
			// user so they can fix the contract (typically: old A/F/B/P
			// conflated format instead of 5-block) — or correct the path.
			log.Printf("[ATOMIC-DISPATCH] /create-ce failed: %v", err)
			return &SkillExecResult{
				Skill: "create-ce",
				OutputB: fmt.Sprintf(
					"## /create-ce failed\n\n**Error:** %s\n\n**Common causes:**\n- Contract file is in old A/F/B/P conflated format — needs 5-block split (`## A:`, `## P_pre:`, `## F:`, `## B:`, `## P_post:`). See `Docs/contract-authoring-guide.md` §2.\n- No `.md` file in `uploaded_files/` — re-upload via the Crafter chat.\n- `file_path` arg in the unit body points to a path that doesn't exist.\n\n**To recover:** fix the contract markdown, then re-run `/proceed-work` on this same unit. No LLM fallback is attempted because the LLM has no /create-ce tool — it would write freestyle files instead of registering a CESpec, which is worse than failing visibly.",
					err.Error()),
				StateChange: "create-ce dispatch failed — see error detail",
				Duration:    0,
			}, true
		}
		return res, true
	}
	// Future atomic-skill cases append here.
	return nil, false
}

func findNextPendingUnit(content string) (int, string) {
	re := regexp.MustCompile(`### (\d+)\.\s+(.+?)\s*\|\s*STATUS:\s*PENDING`)
	m := re.FindStringSubmatch(content)
	if len(m) < 3 {
		return 0, ""
	}
	var idx int
	fmt.Sscanf(m[1], "%d", &idx)
	return idx, strings.TrimSpace(m[2])
}

func extractUnitBlock(content string, unitIdx int) string {
	re := regexp.MustCompile(fmt.Sprintf(`(?s)(### %d\..+?)(?:### \d+\.|## |$)`, unitIdx))
	m := re.FindStringSubmatch(content)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// extractExistingResult pulls the current Result content (excluding the
// `- Result:` line itself) for the given unit. Returns "" if no Result body
// exists yet. Used by WP-AO-28 Unit 3 archive flow.
func extractExistingResult(content string, unitIdx int) string {
	// Find the unit header
	unitHeader := fmt.Sprintf("### %d.", unitIdx)
	idx := strings.Index(content, unitHeader)
	if idx < 0 {
		return ""
	}
	block := content[idx:]
	// Find the unit's end (next "### " or EOF)
	if nextUnit := strings.Index(block[len(unitHeader):], "\n### "); nextUnit > 0 {
		block = block[:len(unitHeader)+nextUnit]
	}
	// Find `- Result:` within this unit block
	resultMarker := "- Result:"
	resultIdx := strings.Index(block, resultMarker)
	if resultIdx < 0 {
		return ""
	}
	tail := block[resultIdx+len(resultMarker):]
	// Result body ends at the next `- ` field (typically `- State:`) at line start
	endRe := regexp.MustCompile(`(?m)^- `)
	endLoc := endRe.FindStringIndex(tail)
	var body string
	if endLoc != nil {
		body = tail[:endLoc[0]]
	} else {
		body = tail
	}
	return strings.TrimSpace(body)
}

// archiveResult writes the prior Result content to a timestamped archive
// file under {ProjectDir}/result-archives/. Returns the archive file path
// (empty if no archive was written). WP-AO-28 Unit 3.
func archiveResult(state *CrafterState, wpID string, unitIdx int, prior string) string {
	if state == nil || state.ProjectDir == "" {
		return ""
	}
	trimmed := strings.TrimSpace(prior)
	// Skip the empty-Result case ("- Result:\n" with no body) — nothing to archive
	if len(trimmed) < 5 {
		return ""
	}
	archiveDir := filepath.Join(state.ProjectDir, "result-archives")
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		log.Printf("[RESULT-ARCHIVE] mkdir failed: %v", err)
		return ""
	}
	// Safe-for-disk ISO8601 form: 20260516T185758Z
	ts := time.Now().UTC().Format("20060102T150405Z")
	filename := fmt.Sprintf("%s-unit-%d-%s.md.archive", wpID, unitIdx, ts)
	path := filepath.Join(archiveDir, filename)
	header := fmt.Sprintf("# Archived Result — %s unit %d\n**Archived:** %s\n\n---\n\n", wpID, unitIdx, time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(header+trimmed+"\n"), 0644); err != nil {
		log.Printf("[RESULT-ARCHIVE] write failed: %v", err)
		return ""
	}
	log.Printf("[RESULT-ARCHIVE] wpID=%s unit=%d archive=%s (%d bytes)", wpID, unitIdx, path, len(trimmed))
	return path
}

// markUnitCompleted updates the WP file in-place for a completed unit:
//   - flips STATUS: PENDING → COMPLETED on the unit's header line
//   - OVERWRITES the Result block with the new content (prior content should
//     have been archived by the caller via archiveResult before invocation —
//     this function does NOT archive itself)
//   - sets WP-level STATUS to IN_PROGRESS if it was PENDING
//
// WP-AO-28 Unit 3 — removed the prior 1000-char Result cap (was starving
// verify-work evidence); switched APPEND behavior to true OVERWRITE.
func markUnitCompleted(content string, unitIdx int, result string) string {
	statusRe := regexp.MustCompile(fmt.Sprintf(`(### %d\..+?\|)\s*STATUS:\s*PENDING`, unitIdx))
	content = statusRe.ReplaceAllString(content, "${1} STATUS: COMPLETED")

	// Indent continuation lines so they nest visually under `- Result:`.
	// No char-limit cap — David's spec (WP-AO-28) allows large Result.
	resultClean := strings.ReplaceAll(strings.TrimSpace(result), "\n", "\n  ")

	// Locate the unit block + the Result field within it; OVERWRITE the
	// body (everything between `- Result:` and the next `- ` field marker).
	unitHeader := fmt.Sprintf("### %d.", unitIdx)
	unitIdxStart := strings.Index(content, unitHeader)
	if unitIdxStart < 0 {
		return content // unit not found — caller error, return unchanged
	}
	// Find end of unit block (next "### " or EOF)
	tail := content[unitIdxStart+len(unitHeader):]
	unitEnd := unitIdxStart + len(content[unitIdxStart:])
	if nextHdr := strings.Index(tail, "\n### "); nextHdr > 0 {
		unitEnd = unitIdxStart + len(unitHeader) + nextHdr
	}
	unitBlock := content[unitIdxStart:unitEnd]
	rest := content[unitEnd:]

	// Find `- Result:` within the unit block + its body span
	resultMarker := "- Result:"
	rIdx := strings.Index(unitBlock, resultMarker)
	if rIdx < 0 {
		// No Result field — append one before the unit's end
		unitBlock = strings.TrimRight(unitBlock, "\n") + fmt.Sprintf("\n- Result: %s\n", resultClean)
	} else {
		// Find body end — next `- ` at line start within unitBlock past rIdx
		afterMarker := rIdx + len(resultMarker)
		searchSpan := unitBlock[afterMarker:]
		endRe := regexp.MustCompile(`(?m)^- `)
		var bodyEnd int
		if loc := endRe.FindStringIndex(searchSpan); loc != nil {
			bodyEnd = afterMarker + loc[0]
		} else {
			bodyEnd = len(unitBlock)
		}
		// Construct new block
		newResultLine := " " + resultClean + "\n"
		unitBlock = unitBlock[:afterMarker] + newResultLine + unitBlock[bodyEnd:]
	}

	content = content[:unitIdxStart] + unitBlock + rest

	// WP-level STATUS bump
	wpStatusRe := regexp.MustCompile(`\*\*STATUS:\*\*\s*PENDING`)
	content = wpStatusRe.ReplaceAllString(content, "**STATUS:** IN_PROGRESS")

	return content
}

func buildArtifactInventory(wpContent string, unitIdx int, workspaceDir string) string {
	block := extractUnitBlock(wpContent, unitIdx)
	if block == "" {
		return ""
	}
	pathRe := regexp.MustCompile("`([^`]+\\.\\w+)`")
	matches := pathRe.FindAllStringSubmatch(block, -1)
	seen := make(map[string]bool)
	var lines []string
	for _, m := range matches {
		candidate := m[1]
		if seen[candidate] {
			continue
		}
		if filepath.IsAbs(candidate) {
			continue
		}
		if strings.Contains(candidate, "/") || strings.Contains(candidate, ".") {
			ext := filepath.Ext(candidate)
			if ext == "" || len(ext) > 6 {
				continue
			}
			seen[candidate] = true
			absPath := filepath.Join(workspaceDir, candidate)
			info, err := os.Stat(absPath)
			if err == nil {
				lines = append(lines, fmt.Sprintf("  - `%s` (%s) → `%s`", candidate, formatBytes(info.Size()), absPath))
			} else {
				lines = append(lines, fmt.Sprintf("  - `%s` (missing from disk)", candidate))
			}
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

func formatBytes(b int64) string {
	if b < 1024 {
		return fmt.Sprintf("%d B", b)
	}
	if b < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(b)/(1024*1024))
}

func formatArgs(args map[string]any) string {
	if len(args) == 0 {
		return "none"
	}
	data, _ := json.MarshalIndent(args, "", "  ")
	return string(data)
}

func readUploadedFilesContext(projectDir string) string {
	uploadDir := filepath.Join(projectDir, "uploaded_files")
	entries, err := os.ReadDir(uploadDir)
	if err != nil || len(entries) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n## Uploaded Reference Files\n")
	b.WriteString("The user has uploaded the following files for context. Use them to inform your work.\n\n")

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		name := e.Name()
		size := info.Size()
		ext := strings.ToLower(filepath.Ext(name))

		isImage := ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif" || ext == ".webp" || ext == ".svg"
		if isImage {
			b.WriteString(fmt.Sprintf("### %s (image, %s)\n", name, formatBytes(size)))
			b.WriteString("*Image file — available in uploaded_files/ for reference.*\n\n")
			continue
		}

		if size > 50*1024 {
			b.WriteString(fmt.Sprintf("### %s (%s, truncated)\n", name, formatBytes(size)))
			data, err := os.ReadFile(filepath.Join(uploadDir, name))
			if err == nil {
				content := string(data)
				if len(content) > 2000 {
					content = content[:2000] + "\n... (truncated at 2000 chars)"
				}
				b.WriteString("```\n" + content + "\n```\n\n")
			}
			continue
		}

		data, err := os.ReadFile(filepath.Join(uploadDir, name))
		if err != nil {
			continue
		}
		b.WriteString(fmt.Sprintf("### %s (%s)\n", name, formatBytes(size)))
		b.WriteString("```\n" + string(data) + "\n```\n\n")
	}

	return b.String()
}
