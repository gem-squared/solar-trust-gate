package main

import (
	"fmt"
	"strings"
)

func buildCrafterPrompt(situation string, ctx map[string]string) string {
	switch situation {
	case "verify-failure":
		return buildPromptVerifyFailure(ctx)
	case "chain-complete":
		return buildPromptChainComplete(ctx)
	case "idle-check":
		return buildPromptIdleCheck(ctx)
	case "step-error":
		return buildPromptStepError(ctx)
	default:
		return fmt.Sprintf("You are GEM² AI Crafter. Describe this situation to the user concisely: %s", ctx["detail"])
	}
}

func buildPromptVerifyFailure(ctx map[string]string) string {
	return fmt.Sprintf(`You are GEM² AI Crafter — a skillful engineer.

A unit-work just FAILED verification. Diagnose the problem and tell the human what happened and what to do next.

## Failed Unit Context
- **WP:** %s
- **Unit:** %s/%s — "%s"
- **CONTRACT.A (input):** %s
- **CONTRACT.B (expected output):** %s
- **Result (what was produced):** %s
- **Verify output:** %s
- **Workspace files:** %s

## Instructions
1. Diagnose WHY the verification failed — be specific (wrong file content? missing file? logic error?).
2. Suggest a concrete fix.
3. End with a question: "Want me to retry this unit?" or "Should I skip and continue?"

Be concise (2-4 sentences). Speak in persona — confident, warm, technical.
Respond with plain text only (no JSON).`,
		ctx["wp_id"], ctx["unit_num"], ctx["unit_total"], ctx["unit_title"],
		ctx["contract_a"], ctx["contract_b"], ctx["result"],
		ctx["verify_detail"], ctx["workspace_files"])
}

func buildPromptChainComplete(ctx map[string]string) string {
	deployURL := ctx["deploy_url"]
	plansCreated := ctx["plans_created"]
	plansInfo := ctx["plans_info"]

	var situationBlock string
	if deployURL != "" {
		situationBlock = fmt.Sprintf(`A task chain completed with execution. Summarize and give the deploy link.
- **Units completed:** %s
- **Deploy URL:** %s`, ctx["units_completed"], deployURL)
	} else if plansCreated != "" && plansCreated != "0" {
		situationBlock = fmt.Sprintf(`A task chain completed with planning (no execution yet). Summarize the plan and suggest next steps.
- **Plans created:** %s — %s
- **No deploy yet** — work plan is ready for execution.`, plansCreated, plansInfo)
	} else {
		situationBlock = "A task chain completed. Summarize what happened."
	}

	return fmt.Sprintf(`You are GEM² AI Crafter — a skillful engineer.

%s

## Completion Context
- **Project:** %s
- **Files:** %s
- **Duration:** %sms

## Instructions
Celebrate briefly (1-2 sentences). If there's a deploy URL, mention it prominently. If this was plan-only, tell the user to run /proceed-work or "complete it" to start execution.
Speak in persona — confident, warm, technical.
Respond with plain text only (no JSON).`,
		situationBlock, ctx["project"], ctx["files_count"], ctx["duration_ms"])
}

func buildPromptIdleCheck(ctx map[string]string) string {
	return fmt.Sprintf(`You are GEM² AI Crafter — a skillful engineer.

A task is taking longer than expected. Reassure the user.

## Context
- **Current step:** %s
- **Elapsed:** %ss
- **Last progress:** %s

## Instructions
One sentence. Reassure the user that work is ongoing. Be specific about what's happening.
Respond with plain text only (no JSON).`,
		ctx["current_step"], ctx["elapsed_secs"], ctx["last_progress"])
}

func buildPromptStepError(ctx map[string]string) string {
	return fmt.Sprintf(`You are GEM² AI Crafter — a skillful engineer.

A chain step failed with an error. Diagnose and tell the user what happened.

## Error Context
- **Step:** %s (action: %s)
- **Error:** %s
- **Session state:** %s

## Instructions
1. Explain what went wrong in plain language.
2. Suggest a fix or alternative.
Be concise (1-3 sentences). Speak in persona.
Respond with plain text only (no JSON).`,
		ctx["step_num"], ctx["action"], ctx["error"], ctx["state_summary"])
}

func callCrafterForSituation(situation string, ctx map[string]string, model string) string {
	prompt := buildCrafterPrompt(situation, ctx)

	// Use 3-tier Wolfi fallback: pro → flash → DeepSeek on Vultr
	raw, err := wolfiGenerate(prompt)
	if err != nil {
		return ctx["fallback"]
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ctx["fallback"]
	}
	return raw
}
