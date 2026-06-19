package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// executePlannedChain runs a task chain planned by the LLM orchestrator.
// Each step is dispatched MECHANICALLY — no LLM calls between steps.
// LLM is only called on: verify FAILURE, step error, chain completion.
func executePlannedChain(chain []TaskChainStep, sess *SessionData, model string, sendSSE func(string, any)) {
	total := len(chain)
	log.Printf("[PLANNED-CHAIN] starting %d-step chain for session %s", total, sess.ID)

	sess.mu.Lock()
	ctx := sess.cancelCtx
	sess.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}

	sendSSE("chain_start", map[string]any{
		"segments":   total,
		"session_id": sess.ID,
	})

	chainStart := time.Now()
	var allFiles []string
	unitsCompleted := 0
	plansCreated := 0
	var plansInfo []string // e.g., "WP-ST-1 (6 units)"
	hadProceedWork := false

	for i, step := range chain {
		if ctx.Err() != nil {
			log.Printf("[PLANNED-CHAIN] cancelled before step %d/%d", i+1, total)
			sendSSE("chain_cancelled", map[string]any{
				"step":    i + 1,
				"total":   total,
				"message": fmt.Sprintf("Chain cancelled at step %d/%d", i+1, total),
			})
			sendSSE("done", map[string]string{"status": "cancelled"})
			return
		}

		stepNum := i + 1
		log.Printf("[PLANNED-CHAIN] step %d/%d: action=%q args=%v", stepNum, total, step.Action, step.Args)

		sendSSE("chain_step", map[string]any{
			"step":   stepNum,
			"total":  total,
			"raw":    step.Action,
			"status": fmt.Sprintf("Step %d/%d: %s", stepNum, total, step.Action),
		})

		var stepErr error
		switch step.Action {

		case "switch-project":
			query, _ := step.Args["query"].(string)
			cmdResult := ExecuteCommand("switch-project", step.Args, sess.State)
			if cmdResult.Success && len(cmdResult.Files) > 0 {
				slug := cmdResult.Files[0]
				sess.SetActiveProject(slug)
				sess.State.Refresh()
				cmdResult.Output += fmt.Sprintf("\n\nActive project is now **%s**.", slug)
			}
			sendSSE("state_updated", map[string]string{"change": "switch-project"})
			sendSSE("response", map[string]any{
				"content":    cmdResult.Output,
				"skill":      "switch-project",
				"session_id": sess.ID,
			})
			sess.appendAndSave("assistant", cmdResult.Output)
			if !cmdResult.Success {
				stepErr = fmt.Errorf("switch-project failed for %q: %s", query, cmdResult.Output)
			}

		case "proceed-work":
			hadProceedWork = true
			sendSSE("skill_selected", map[string]string{
				"skill":     "auto-proceed",
				"reasoning": "Executing all PENDING units with inline verification",
			})
			result, err := autoProceedLoopWithCrafter(sess, model, sendSSE)
			if err != nil {
				stepErr = err
			} else {
				unitsCompleted += countCompletedFromResult(result.OutputB)
				allFiles = append(allFiles, result.FilesModified...)
				sendSSE("response", map[string]any{
					"content":        result.OutputB,
					"skill":          result.Skill,
					"state_change":   result.StateChange,
					"duration_ms":    result.Duration,
					"files_modified": result.FilesModified,
					"session_id":     sess.ID,
				})
				sess.appendAndSave("assistant", result.OutputB)
				if strings.Contains(result.StateChange, "FAILURE") {
					// Verify failure already reported by autoProceedLoopWithCrafter — stop chain
					sendSSE("done", map[string]string{"status": "stopped"})
					return
				}
			}

		case "deploy-work":
			deployResult := cmdDeployWork(sess)
			sendSSE("state_updated", map[string]string{"change": "deploy-work"})
			sendSSE("response", map[string]any{
				"content":    deployResult.Output,
				"skill":      "deploy-work",
				"session_id": sess.ID,
			})
			sess.appendAndSave("assistant", deployResult.Output)

		case "create-project":
			skill := GetSkill("create-project")
			if skill == nil {
				stepErr = fmt.Errorf("create-project skill not found")
			} else {
				execResult, err := ExecuteSkill(skill, step.Args, sess.State, sess)
				if err != nil {
					stepErr = err
				} else {
					sendSSE("response", map[string]any{
						"content":        execResult.OutputB,
						"skill":          execResult.Skill,
						"state_change":   execResult.StateChange,
						"session_id":     sess.ID,
					})
					sess.appendAndSave("assistant", execResult.OutputB)
					if execResult.StateChange != "" {
						sendSSE("state_updated", map[string]string{"change": execResult.StateChange})
					}
				}
			}

		case "plan-work":
			skill := GetSkill("plan-work")
			if skill == nil {
				stepErr = fmt.Errorf("plan-work skill not found")
			} else {
				execResult, err := ExecuteSkill(skill, step.Args, sess.State, sess)
				if err != nil {
					stepErr = err
				} else if !workPlanHasUnitHeaders(execResult) {
					// WP-AO-42 P5 — defensive guard against malformed plan-work
					// output (the JSON-cheat failure mode from the 2026-05-17
					// retest). If the produced WP has zero "### N." unit
					// headers, fail loudly rather than silently chaining to a
					// doomed proceed-work that reports "0 units completed".
					stepErr = fmt.Errorf("plan-work produced an empty WP — no '### N.' unit headers found in the file. The LLM emitted metadata-only output instead of decomposing the work. Retry by typing 'proceed' or re-run the chain. WP files: %v", execResult.FilesModified)
				} else {
					plansCreated++
					allFiles = append(allFiles, execResult.FilesModified...)
					sess.State.Refresh()
					for _, wp := range sess.State.WorkPlans {
						if wp.Status == "PENDING" || wp.Status == "IN_PROGRESS" {
							plansInfo = append(plansInfo, fmt.Sprintf("%s (%d units)", wp.ID, wp.UnitCount))
							break
						}
					}
					sendSSE("response", map[string]any{
						"content":        execResult.OutputB,
						"skill":          execResult.Skill,
						"state_change":   execResult.StateChange,
						"session_id":     sess.ID,
					})
					sess.appendAndSave("assistant", execResult.OutputB)
					if execResult.StateChange != "" {
						sendSSE("state_updated", map[string]string{"change": execResult.StateChange})
					}
					if strings.HasPrefix(sess.Name, "Session ") {
						if work, ok := step.Args["work"].(string); ok && work != "" {
							name := work
							if len(name) > 40 {
								name = name[:40] + "..."
							}
							sess.Name = name
							sess.saveSessionMeta()
						}
					}
				}
			}

		default:
			// Generic skill or command dispatch
			if isCommand(step.Action) {
				cmdResult := ExecuteCommand(step.Action, step.Args, sess.State)
				sendSSE("response", map[string]any{
					"content":    cmdResult.Output,
					"skill":      step.Action,
					"session_id": sess.ID,
				})
				sess.appendAndSave("assistant", cmdResult.Output)
				if !cmdResult.Success {
					stepErr = fmt.Errorf("%s failed: %s", step.Action, cmdResult.Output)
				}
			} else {
				skill := GetSkill(step.Action)
				if skill == nil {
					stepErr = fmt.Errorf("skill '%s' not found", step.Action)
				} else {
					execResult, err := ExecuteSkill(skill, step.Args, sess.State, sess)
					if err != nil {
						stepErr = err
					} else {
						sendSSE("response", map[string]any{
							"content":        execResult.OutputB,
							"skill":          execResult.Skill,
							"state_change":   execResult.StateChange,
							"duration_ms":    execResult.Duration,
							"files_modified": execResult.FilesModified,
							"session_id":     sess.ID,
						})
						sess.appendAndSave("assistant", execResult.OutputB)
						allFiles = append(allFiles, execResult.FilesModified...)
					}
				}
			}
		}

		// On step error: call LLM to diagnose
		if stepErr != nil {
			log.Printf("[PLANNED-CHAIN] step %d error: %v", stepNum, stepErr)
			sess.State.Refresh()
			crafterMsg := callCrafterForSituation("step-error", map[string]string{
				"step_num":      fmt.Sprintf("%d/%d", stepNum, total),
				"action":        step.Action,
				"error":         stepErr.Error(),
				"state_summary": sess.State.Summary(),
				"fallback":      fmt.Sprintf("Step %d (%s) failed: %v", stepNum, step.Action, stepErr),
			}, model)
			sendSSE("response", map[string]any{
				"content":    crafterMsg,
				"skill":      "crafter-diagnosis",
				"session_id": sess.ID,
			})
			sess.appendAndSave("assistant", crafterMsg)
			sendSSE("done", map[string]string{"status": "error"})
			return
		}

		sess.State.Refresh()
	}

	// Chain complete — auto-append deploy if it wasn't the last step
	if hadProceedWork {
		lastAction := ""
		if len(chain) > 0 {
			lastAction = chain[len(chain)-1].Action
		}
		if lastAction != "deploy-work" {
			deployResult := cmdDeployWork(sess)
			sendSSE("response", map[string]any{
				"content":    deployResult.Output,
				"skill":      "deploy-work",
				"session_id": sess.ID,
			})
			sess.appendAndSave("assistant", deployResult.Output)
		}
	}

	slug := sess.ActiveProject
	if slug == "" {
		slug = filepath.Base(sess.State.ProjectDir)
	}
	summaryCtx := map[string]string{
		"project":         slug,
		"units_completed": fmt.Sprintf("%d", unitsCompleted),
		"plans_created":   fmt.Sprintf("%d", plansCreated),
		"plans_info":      strings.Join(plansInfo, ", "),
		"files_count":     fmt.Sprintf("%d", len(allFiles)),
		"duration_ms":     fmt.Sprintf("%d", time.Since(chainStart).Milliseconds()),
	}

	if hadProceedWork || unitsCompleted > 0 {
		deployURL := fmt.Sprintf("%s/p/%s/", strings.TrimRight(ceProductionHost, "/"), slug)
		summaryCtx["deploy_url"] = deployURL
		summaryCtx["fallback"] = fmt.Sprintf("Chain complete! %d units done. Check it out: %s", unitsCompleted, deployURL)
	} else if plansCreated > 0 {
		summaryCtx["deploy_url"] = ""
		summaryCtx["fallback"] = fmt.Sprintf("Work plan created: %s. Run /proceed-work to start execution.", strings.Join(plansInfo, ", "))
	} else {
		summaryCtx["deploy_url"] = ""
		summaryCtx["fallback"] = "Chain complete."
	}

	crafterMsg := callCrafterForSituation("chain-complete", summaryCtx, model)

	sendSSE("response", map[string]any{
		"content":    crafterMsg,
		"skill":      "crafter-summary",
		"session_id": sess.ID,
	})
	sess.appendAndSave("assistant", crafterMsg)

	sendSSE("done", map[string]string{"status": "complete"})
}

// autoProceedLoopWithCrafter is like autoProceedLoop but calls the LLM
// with a situation-specific prompt on verify FAILURE instead of returning
// a static message. On verify SUCCESS: silent, loop continues.
func autoProceedLoopWithCrafter(sess *SessionData, model string, sendSSE func(string, any)) (*SkillExecResult, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY not set")
	}

	sess.mu.Lock()
	ctx := sess.cancelCtx
	sess.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}

	proceedSkill := GetSkill("proceed-work")
	verifySkill := GetSkill("verify-work")
	if proceedSkill == nil {
		return nil, fmt.Errorf("proceed-work skill not found")
	}

	loopStart := time.Now()
	var totalFiles []string
	unitsCompleted := 0
	maxUnits := 9

	for iteration := 0; iteration < maxUnits; iteration++ {
		if ctx.Err() != nil {
			log.Printf("[AUTO-LOOP-CRAFTER] cancelled before unit %d", iteration+1)
			sendSSE("unit_cancelled", map[string]any{
				"units_completed": unitsCompleted,
				"message":         fmt.Sprintf("Auto-proceed cancelled after %d units", unitsCompleted),
			})
			return &SkillExecResult{
				Skill:         "auto-proceed",
				OutputB:       fmt.Sprintf("## Auto-Proceed Cancelled\n\n**Units completed:** %d\n\nCancelled by user.", unitsCompleted),
				StateChange:   fmt.Sprintf("Auto-proceed cancelled after %d units", unitsCompleted),
				Duration:      time.Since(loopStart).Milliseconds(),
				FilesModified: totalFiles,
			}, nil
		}
		sess.State.Refresh()

		// WP-AO-47 Unit 1: defensive WP selection — pick the NEWEST WP with
		// actual pending units (iterate in REVERSE so highest WP-ST-N wins
		// ties). Drops the `Status == "PENDING"` fallback which caused the
		// 2026-05-18 regression where freshly-created WP-ST-2 was ignored in
		// favor of the older WP-ST-1 whose units were already COMPLETED.
		var targetWP *WorkPlanSummary
		for i := len(sess.State.WorkPlans) - 1; i >= 0; i-- {
			wp := &sess.State.WorkPlans[i]
			if wp.Pending > 0 {
				targetWP = wp
				break
			}
		}
		if targetWP == nil {
			break
		}

		unitTotal := targetWP.UnitCount
		unitDone := targetWP.Completed

		sendSSE("unit_progress", map[string]any{
			"status":  "executing",
			"wp_id":   targetWP.ID,
			"unit":    unitDone + 1,
			"total":   unitTotal,
			"message": fmt.Sprintf("Executing unit %d/%d of %s...", unitDone+1, unitTotal, targetWP.ID),
		})

		execStart := time.Now()
		args := map[string]any{"wp_id": targetWP.ID}
		idleW := startIdleWatchdog(ctx, fmt.Sprintf("proceed-work for %s unit %d/%d", targetWP.ID, unitDone+1, unitTotal), model, sendSSE)
		execResult, err := ExecuteSkill(proceedSkill, args, sess.State, sess)
		idleW.Stop()
		if err != nil {
			sendSSE("unit_progress", map[string]any{
				"status":  "error",
				"wp_id":   targetWP.ID,
				"unit":    unitDone + 1,
				"total":   unitTotal,
				"message": fmt.Sprintf("Unit %d failed: %v", unitDone+1, err),
			})
			return nil, fmt.Errorf("unit %d execution failed: %w", unitDone+1, err)
		}

		unitsCompleted++
		totalFiles = append(totalFiles, execResult.FilesModified...)
		sessLogExec(sess, "proceed-work", args, execResult.Duration, true, truncate(execResult.OutputB, 100))
		log.Printf("[AUTO-LOOP-CRAFTER] unit %d/%d completed in %v", unitDone+1, unitTotal, time.Since(execStart))

		// Verify unit inline
		verifyState := "SUCCESS"
		verifyDetail := ""
		if verifySkill != nil {
			sess.State.Refresh()
			verifyArgs := map[string]any{
				"wp_id":      targetWP.ID,
				"unit_index": float64(unitDone + 1),
			}
			verifyW := startIdleWatchdog(ctx, fmt.Sprintf("verify-work for %s unit %d/%d", targetWP.ID, unitDone+1, unitTotal), model, sendSSE)
			verifyResult, verifyErr := ExecuteSkill(verifySkill, verifyArgs, sess.State, sess)
			verifyW.Stop()
			if verifyErr != nil {
				verifyState = "FAILURE"
				verifyDetail = verifyErr.Error()
			} else {
				// WP-AO-29 — scope verdict to THIS unit; avoid cross-unit
				// contamination from stale failures elsewhere in the WP.
				verifyState, verifyDetail = extractUnitVerdict(verifyResult.OutputB, unitDone+1)
			}
		}

		sendSSE("unit_progress", map[string]any{
			"status":       "completed",
			"wp_id":        targetWP.ID,
			"unit":         unitDone + 1,
			"total":        unitTotal,
			"verify_state": verifyState,
			"duration_ms":  execResult.Duration,
			"message":      fmt.Sprintf("Unit %d/%d completed — %s", unitDone+1, unitTotal, verifyState),
		})

		// verify SUCCESS → continue to next unit
		// verify FAILURE → AI-pilot analyzes + retries (up to 2 retries)
		if verifyState == "FAILURE" {
			maxRetries := 2
			retrySuccess := false

			for retry := 1; retry <= maxRetries; retry++ {
				unitContract := extractUnitContract(targetWP.ID, unitDone+1, sess.State)
				wsFiles := listWorkspaceFiles(sess.State)

				// Fat failure context — uses dynamic per-model budget instead of
				// the prior 500-char truncation. See WP-AO-23 Unit 2.
				fc := composeFailureContext(targetWP.ID, unitDone+1, sess.State, model)
				fatContext := renderFailureContext(fc)
				// If verifyDetail was passed in but our log lookup missed, fall back to it.
				if fc.VerifyDetail == "" && verifyDetail != "" {
					fatContext += "\n\n## Live verify_detail (no log file found)\n" + verifyDetail
				}
				log.Printf("[AI-PILOT] fat-context for %s unit %d: %d/%d bytes, %d files",
					targetWP.ID, unitDone+1, fc.UsedBytes, fc.BudgetBytes, len(fc.FileContents))

				pilotAnalysis := callCrafterForSituation("verify-failure", map[string]string{
					"wp_id":           targetWP.ID,
					"unit_num":        fmt.Sprintf("%d", unitDone+1),
					"unit_total":      fmt.Sprintf("%d", unitTotal),
					"unit_title":      unitContract["title"],
					"contract_a":      unitContract["a"],
					"contract_b":      unitContract["b"],
					"result":          unitContract["result"],
					"verify_detail":   fatContext, // FAT — was truncate(verifyDetail, 500)
					"workspace_files": wsFiles,
					"fallback":        fmt.Sprintf("Unit %d/%d FAILED. Retrying (%d/%d).", unitDone+1, unitTotal, retry, maxRetries),
				}, model)

				sendSSE("unit_progress", map[string]any{
					"status":  "retrying",
					"wp_id":   targetWP.ID,
					"unit":    unitDone + 1,
					"total":   unitTotal,
					"retry":   retry,
					"message": fmt.Sprintf("AI-Pilot retry %d/%d for unit %d: %s", retry, maxRetries, unitDone+1, truncate(pilotAnalysis, 100)),
				})
				log.Printf("[AI-PILOT] retry %d/%d for %s unit %d — analysis: %s", retry, maxRetries, targetWP.ID, unitDone+1, truncate(pilotAnalysis, 200))

				// Reset unit status to PENDING so proceed-work can re-execute it
				resetUnitStatus(targetWP, unitDone+1, sess.State)
				sess.State.Refresh()

				retryArgs := map[string]any{
					"wp_id":          targetWP.ID,
					"retry_context":  fmt.Sprintf("PREVIOUS ATTEMPT FAILED. Verify detail: %s\nAI-Pilot analysis: %s", truncate(verifyDetail, 300), truncate(pilotAnalysis, 300)),
				}
				retryW := startIdleWatchdog(ctx, fmt.Sprintf("AI-Pilot retry %d/2 for %s unit %d", retry, targetWP.ID, unitDone+1), model, sendSSE)
				retryResult, retryErr := ExecuteSkill(proceedSkill, retryArgs, sess.State, sess)
				retryW.Stop()
				if retryErr != nil {
					log.Printf("[AI-PILOT] retry %d execution failed: %v", retry, retryErr)
					continue
				}
				totalFiles = append(totalFiles, retryResult.FilesModified...)

				// Re-verify
				sess.State.Refresh()
				retryVerifyState := "SUCCESS"
				if verifySkill != nil {
					retryVerifyArgs := map[string]any{
						"wp_id":      targetWP.ID,
						"unit_index": float64(unitDone + 1),
					}
					retryVerifyResult, retryVerifyErr := ExecuteSkill(verifySkill, retryVerifyArgs, sess.State, sess)
					if retryVerifyErr != nil {
						retryVerifyState = "FAILURE"
						verifyDetail = retryVerifyErr.Error()
					} else if strings.Contains(retryVerifyResult.OutputB, "FAIL") {
						retryVerifyState = "FAILURE"
						verifyDetail = retryVerifyResult.OutputB
					}
				}

				sendSSE("unit_progress", map[string]any{
					"status":       "completed",
					"wp_id":        targetWP.ID,
					"unit":         unitDone + 1,
					"total":        unitTotal,
					"verify_state": retryVerifyState,
					"retry":        retry,
					"message":      fmt.Sprintf("Retry %d/%d — unit %d: %s", retry, maxRetries, unitDone+1, retryVerifyState),
				})

				if retryVerifyState == "SUCCESS" {
					retrySuccess = true
					break
				}
			}

			if !retrySuccess {
				crafterMsg := fmt.Sprintf("## AI-Pilot Exhausted Retries\n\nUnit %d/%d of %s FAILED after %d retries.\nManual intervention needed: `/proceed-work %s`",
					unitDone+1, unitTotal, targetWP.ID, maxRetries, targetWP.ID)
				sendSSE("unit_failed", map[string]any{
					"wp_id":   targetWP.ID,
					"unit":    unitDone + 1,
					"total":   unitTotal,
					"message": crafterMsg,
				})
				return &SkillExecResult{
					Skill:         "auto-proceed",
					OutputB:       crafterMsg,
					StateChange:   fmt.Sprintf("Auto-proceed stopped at unit %d/%d of %s — FAILURE after %d retries", unitDone+1, unitTotal, targetWP.ID, maxRetries),
					Duration:      time.Since(loopStart).Milliseconds(),
					FilesModified: totalFiles,
				}, nil
			}
		}
	}

	// All units done
	sess.State.Refresh()
	var wpID string
	for _, wp := range sess.State.WorkPlans {
		if wp.Completed > 0 {
			wpID = wp.ID
		}
	}

	summary := fmt.Sprintf("## Auto-Proceed Complete\n\n"+
		"**WP:** %s | **Units completed:** %d | **Files:** %d | **Duration:** %dms",
		wpID, unitsCompleted, len(totalFiles), time.Since(loopStart).Milliseconds())

	// WP-AO-46 Unit 2: propagate any [[CE_VIEWER_BUTTON|url]] tokens from
	// the completed units' Result fields up to the summary OutputB. The
	// frontend's sentinel-rendering code (crafter-app.js, WP-AO-30) turns
	// the token into the cream-pulsing amber "▶ Open CE Viewer" button.
	// Without this propagation the canonical-pipeline chain strips the
	// token in the auto-proceed summary (regression from when /create-ce
	// was a top-level skill emitting the button directly to chat).
	if wpID != "" {
		if content, err := sess.State.ReadWorkPlan(wpID); err == nil {
			tokenRe := regexp.MustCompile(`\[\[CE_VIEWER_BUTTON\|[^\]]+\]\]`)
			if tokens := tokenRe.FindAllString(content, -1); len(tokens) > 0 {
				summary += "\n\n" + tokens[len(tokens)-1] + "\n"
			}
		}
	}

	return &SkillExecResult{
		Skill:         "auto-proceed",
		OutputB:       summary,
		StateChange:   fmt.Sprintf("Auto-proceed completed %d units of %s", unitsCompleted, wpID),
		Duration:      time.Since(loopStart).Milliseconds(),
		FilesModified: totalFiles,
	}, nil
}

// extractUnitContract reads the WP file and extracts CONTRACT fields for a specific unit.
func extractUnitContract(wpID string, unitIndex int, state *CrafterState) map[string]string {
	result := map[string]string{"title": "", "a": "", "b": "", "result": ""}
	content, err := state.ReadWorkPlan(wpID)
	if err != nil {
		return result
	}

	unitHeader := fmt.Sprintf("### %d.", unitIndex)
	idx := strings.Index(content, unitHeader)
	if idx < 0 {
		return result
	}
	block := content[idx:]
	if nextIdx := strings.Index(block[4:], "\n### "); nextIdx > 0 {
		block = block[:nextIdx+4]
	}

	titleRe := regexp.MustCompile(`### \d+\.\s*(.+?)(?:\s*\|)`)
	if m := titleRe.FindStringSubmatch(block); len(m) > 1 {
		result["title"] = strings.TrimSpace(m[1])
	}
	if v := extractContractField(block, "- A:"); v != "" {
		result["a"] = v
	}
	if v := extractContractField(block, "- B:"); v != "" {
		result["b"] = v
	}
	if v := extractContractField(block, "- Result:"); v != "" {
		result["result"] = v
	}
	return result
}

func extractContractField(block, prefix string) string {
	idx := strings.Index(block, prefix)
	if idx < 0 {
		return ""
	}
	rest := block[idx+len(prefix):]
	if nlIdx := strings.Index(rest, "\n- "); nlIdx > 0 {
		rest = rest[:nlIdx]
	} else if nlIdx := strings.Index(rest, "\n###"); nlIdx > 0 {
		rest = rest[:nlIdx]
	}
	return strings.TrimSpace(rest)
}

func listWorkspaceFiles(state *CrafterState) string {
	dir := filepath.Join(state.ProjectDir, "output", filepath.Base(state.ProjectDir))
	var files []string
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		files = append(files, rel)
		if len(files) >= 15 {
			return filepath.SkipAll
		}
		return nil
	})
	return strings.Join(files, ", ")
}

func resetUnitStatus(wp *WorkPlanSummary, unitIndex int, state *CrafterState) {
	content, err := state.ReadWorkPlan(wp.ID)
	if err != nil {
		return
	}
	// Reset COMPLETED/FAILURE back to PENDING for the specific unit
	unitHeader := fmt.Sprintf("### %d.", unitIndex)
	idx := strings.Index(content, unitHeader)
	if idx < 0 {
		return
	}
	block := content[idx:]
	nextUnit := strings.Index(block[1:], "\n### ")
	if nextUnit > 0 {
		block = block[:nextUnit+1]
	}

	updated := block
	updated = strings.Replace(updated, "STATUS: COMPLETED", "STATUS: PENDING", 1)
	updated = strings.Replace(updated, "STATUS: IN_PROGRESS", "STATUS: PENDING", 1)

	newContent := strings.Replace(content, block, updated, 1)
	os.WriteFile(wp.FilePath, []byte(newContent), 0644)
	state.Refresh()
}

// extractUnitVerdict parses the verify-work OutputB and returns the verdict
// (SUCCESS or FAILURE) + detail for the specific unit. Replaces the prior
// `strings.Contains(OutputB, "FAIL")` substring check which leaked failures
// from other units onto the current unit. WP-AO-29 Unit 2.
//
// OutputB format produced by executeVerifyWork:
//
//	## Verification: WP-ST-1 — **FAILURE**
//	(summary line)
//	### Unit 3: title — FAIL
//	(detail block)
//	### Unit 4: title — PASS
//	(detail block)
//	**Next:** ...
//
// We isolate to the "### Unit N: ... — (PASS|FAIL)" line and the detail span
// that follows it (up to next "### " or "**Next:**").
func extractUnitVerdict(outputB string, unitIdx int) (state, detail string) {
	header := regexp.MustCompile(fmt.Sprintf(`(?m)^### Unit %d: .+? — (PASS|FAIL)\b`, unitIdx))
	loc := header.FindStringIndex(outputB)
	if loc == nil {
		// Fallback to legacy bare-substring behavior — preserves prior semantics
		// for any caller that hasn't migrated to per-unit verdict format.
		if strings.Contains(outputB, "FAIL") {
			return "FAILURE", outputB
		}
		return "SUCCESS", ""
	}
	headerLine := outputB[loc[0]:loc[1]]
	verdict := "SUCCESS"
	if strings.HasSuffix(headerLine, "FAIL") {
		verdict = "FAILURE"
	}
	// Capture detail from after the header to next "### " or "**Next:**" or EOF
	tail := outputB[loc[1]:]
	endRe := regexp.MustCompile(`(?m)^(### |\*\*Next:\*\*)`)
	if endLoc := endRe.FindStringIndex(tail); endLoc != nil {
		detail = strings.TrimSpace(tail[:endLoc[0]])
	} else {
		detail = strings.TrimSpace(tail)
	}
	return verdict, detail
}

// idleWatchdog fires LLM idle-check pings during long-running blocking calls
// (e.g. ExecuteSkill for proceed-work / verify-work). Implements WP-AO-11 Unit
// 3.d which was spec'd but never wired. Stops when the parent operation returns.
type idleWatchdog struct {
	ctx     context.Context
	cancel  context.CancelFunc
	model   string
	sendSSE func(string, any)
	label   string // human-readable step name for the idle-check prompt
}

// startIdleWatchdog launches a watchdog goroutine that fires a "this is taking
// long, here's what's happening" SSE every 60 seconds, up to 3 times. Call
// Stop() after the long operation returns.
func startIdleWatchdog(parent context.Context, label, model string, sendSSE func(string, any)) *idleWatchdog {
	ctx, cancel := context.WithCancel(parent)
	w := &idleWatchdog{ctx: ctx, cancel: cancel, model: model, sendSSE: sendSSE, label: label}
	go w.run()
	return w
}

func (w *idleWatchdog) Stop() {
	w.cancel()
}

func (w *idleWatchdog) run() {
	start := time.Now()
	checksFired := 0
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-w.ctx.Done():
			return
		case t := <-ticker.C:
			if checksFired >= 3 {
				log.Printf("[IDLE-CHECK] %s — suppressed (3 checks already fired)", w.label)
				return
			}
			elapsed := int(t.Sub(start).Seconds())
			checksFired++
			log.Printf("[IDLE-CHECK] %s — firing check %d/3 (elapsed %ds)", w.label, checksFired, elapsed)
			analysis := callCrafterForSituation("idle-check", map[string]string{
				"current_step":  w.label,
				"elapsed_secs":  fmt.Sprintf("%d", elapsed),
				"last_progress": "(no SSE event in last 60s — work is still running)",
			}, w.model)
			if w.sendSSE != nil {
				w.sendSSE("response", map[string]any{
					"status":      "idle-check",
					"message":     analysis,
					"elapsed_s":   elapsed,
					"check_index": checksFired,
				})
			}
		}
	}
}

// FailureFileSnippet — a single file's content gathered into a failure context.
type FailureFileSnippet struct {
	Path    string
	Content string
	Size    int // pre-truncation length in bytes
}

// FailureContext bundles everything an LLM needs to diagnose a verify-FAILURE.
// All fields are budget-aware — fields are filled in priority order until the
// model's input budget is exhausted.
type FailureContext struct {
	UnitTitle    string
	UnitBlock    string               // full markdown block for this unit (no 2000-char trim)
	VerifyDetail string               // per-unit verify-work log section (no 500-char trim)
	ResultFull   string               // full Result field text
	FileContents []FailureFileSnippet // contents of files referenced by Result
	Truncated    []string             // names of fields that were dropped/cut to fit budget
	BudgetBytes  int
	UsedBytes    int
}

// composeFailureContext gathers fat failure evidence for the AI-Pilot retry analysis.
// Budget = InputBudgetBytes(model) * 0.6, leaving ~40% headroom for the system prompt,
// chat history, and response tokens.
func composeFailureContext(wpID string, unitIndex int, state *CrafterState, model string) FailureContext {
	budget := int(float64(InputBudgetBytes(model)) * 0.6)
	fc := FailureContext{BudgetBytes: budget}

	// 1. Read full WP content and slice the unit block
	wpContent, err := state.ReadWorkPlan(wpID)
	if err != nil {
		fc.Truncated = append(fc.Truncated, "wp_file:"+err.Error())
		return fc
	}
	unitHeader := fmt.Sprintf("### %d.", unitIndex)
	if idx := strings.Index(wpContent, unitHeader); idx >= 0 {
		block := wpContent[idx:]
		if nextIdx := strings.Index(block[4:], "\n### "); nextIdx > 0 {
			block = block[:nextIdx+4]
		}
		fc.UnitBlock = block
		// Extract title from "### N. Title | STATUS: ..." line
		if firstLine := strings.SplitN(block, "\n", 2)[0]; firstLine != "" {
			if titleStart := strings.Index(firstLine, ". "); titleStart > 0 {
				rest := firstLine[titleStart+2:]
				if pipeIdx := strings.Index(rest, "|"); pipeIdx > 0 {
					fc.UnitTitle = strings.TrimSpace(rest[:pipeIdx])
				} else {
					fc.UnitTitle = strings.TrimSpace(rest)
				}
			}
		}
		fc.ResultFull = extractContractField(block, "- Result:")
	}

	// 2. Read verify-work log for this WP and slice the unit section
	verifyLogPath := filepath.Join(state.ProjectDir, "verify-work-logs", wpID+".md")
	if data, err := os.ReadFile(verifyLogPath); err == nil {
		header := fmt.Sprintf("### Unit %d:", unitIndex)
		log := string(data)
		if idx := strings.Index(log, header); idx >= 0 {
			section := log[idx:]
			if nextIdx := strings.Index(section[len(header):], "\n### Unit "); nextIdx > 0 {
				section = section[:len(header)+nextIdx]
			}
			fc.VerifyDetail = section
		} else {
			// fall back: the whole log file (it's per-WP, all units inside)
			fc.VerifyDetail = log
		}
	}

	// 3. Account for budget consumed so far (priority fields)
	used := len(fc.UnitBlock) + len(fc.VerifyDetail)

	// 4. Extract file paths from Result + read their contents
	if fc.ResultFull != "" {
		paths := extractFilePathsFromResult(fc.ResultFull, state)
		const perFileCap = 30 * 1024 // 30 KB per file max
		for _, p := range paths {
			if used >= budget {
				fc.Truncated = append(fc.Truncated, "file:"+p+" (budget exhausted)")
				continue
			}
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			content := string(data)
			origSize := len(content)
			if len(content) > perFileCap {
				content = content[:perFileCap] + "\n... [truncated at 30 KB per-file cap]"
			}
			remain := budget - used
			if len(content) > remain {
				content = content[:remain] + "\n... [budget cut]"
			}
			fc.FileContents = append(fc.FileContents, FailureFileSnippet{
				Path: p, Content: content, Size: origSize,
			})
			used += len(content)
		}
	}

	fc.UsedBytes = used
	return fc
}

// extractFilePathsFromResult finds file paths mentioned in a Result text and
// returns those that exist on disk. Resolves relative paths against common
// project directories (workspace output, console/static, etc.).
func extractFilePathsFromResult(result string, state *CrafterState) []string {
	// Two patterns: (1) directory-prefixed paths under known roots; (2) bare
	// filenames with code extensions.
	re := regexp.MustCompile(`(?:[\w\-./]*(?:output|workspace|console|public|src|assets|static|tests?)/[\w\-./]+\.\w{1,6})|(?:[\w\-]+\.(?:html|js|css|go|ts|tsx|jsx|json|md|yaml|yml|py|sh|toml))`)
	matches := re.FindAllString(result, -1)

	projOutput := filepath.Join(state.ProjectDir, "output", filepath.Base(state.ProjectDir))
	roots := []string{
		"",
		state.ProjectDir,
		projOutput,
		filepath.Join(state.ProjectDir, "workspace"),
	}

	seen := map[string]bool{}
	var paths []string
	for _, m := range matches {
		for _, root := range roots {
			candidate := m
			if root != "" {
				candidate = filepath.Join(root, m)
			}
			if seen[candidate] {
				continue
			}
			seen[candidate] = true
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				paths = append(paths, candidate)
				break
			}
		}
		if len(paths) >= 10 {
			break
		}
	}
	return paths
}

// renderFailureContext formats a FailureContext for inclusion in an LLM prompt.
// Used by callers that take a single string for "verify_detail" / context fields.
func renderFailureContext(fc FailureContext) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("[FAILURE CONTEXT — %d/%d bytes used]\n\n", fc.UsedBytes, fc.BudgetBytes))
	if fc.UnitBlock != "" {
		b.WriteString("## Unit CONTRACT (full)\n")
		b.WriteString(fc.UnitBlock)
		b.WriteString("\n\n")
	}
	if fc.VerifyDetail != "" {
		b.WriteString("## Verify-work output (full)\n")
		b.WriteString(fc.VerifyDetail)
		b.WriteString("\n\n")
	}
	if len(fc.FileContents) > 0 {
		b.WriteString("## Files referenced in Result\n\n")
		for _, f := range fc.FileContents {
			b.WriteString(fmt.Sprintf("### %s (%d bytes original)\n```\n%s\n```\n\n", f.Path, f.Size, f.Content))
		}
	}
	if len(fc.Truncated) > 0 {
		b.WriteString("## Truncated (budget exhausted)\n")
		for _, t := range fc.Truncated {
			b.WriteString("- " + t + "\n")
		}
	}
	return b.String()
}

func isCommand(action string) bool {
	commands := map[string]bool{
		"clean-workplans": true, "clean-workspace": true, "delete-workplan": true,
		"list-workspace": true, "reset-session": true, "switch-project": true,
		"check-all-sessions": true, "deploy-work": true,
	}
	return commands[action]
}

func countCompletedFromResult(output string) int {
	re := regexp.MustCompile(`Units completed:\s*(\d+)`)
	if m := re.FindStringSubmatch(output); len(m) > 1 {
		var n int
		fmt.Sscanf(m[1], "%d", &n)
		return n
	}
	return 0
}

// workPlanHasUnitHeaders is the WP-AO-42 P5 guard: returns true iff the
// produced plan-work WP contains at least one "### N." unit header.
// Catches the JSON-cheat failure mode where the LLM emits metadata-only
// output without decomposing the work. Reads the WP file from disk (more
// reliable than parsing OutputB which may include markdown around the
// actual WP content). Falls back to scanning OutputB if no FilesModified.
func workPlanHasUnitHeaders(execResult *SkillExecResult) bool {
	unitRe := regexp.MustCompile(`(?m)^### \d+\.`)
	for _, path := range execResult.FilesModified {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if unitRe.MatchString(string(data)) {
			return true
		}
	}
	// Fallback: inspect OutputB directly (covers cases where the file write
	// failed or the result wasn't a file artifact).
	if execResult.OutputB != "" && unitRe.MatchString(execResult.OutputB) {
		return true
	}
	return false
}

func autoProceedLoop(sess *SessionData, sendSSE func(string, any)) (*SkillExecResult, error) {
	return autoProceedLoopWithCrafter(sess, "", sendSSE)
}
