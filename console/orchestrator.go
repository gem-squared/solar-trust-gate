package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// suggestProjectSlug returns a kebab-case word pair like "swift-falcon" or
// "bright-otter" used as the placeholder project name in the orchestrator
// prompt when no active project is bound. Re-rolled every Orchestrate call;
// Gemini is expected to read the user's confirmation in chat history to
// determine which slug actually got used. WP-AO-35.
var (
	slugAdjectives = []string{
		"swift", "bright", "calm", "bold", "lucid", "sharp", "quiet", "amber",
		"crimson", "azure", "silver", "golden", "agile", "wise", "keen", "deft",
	}
	slugNouns = []string{
		"falcon", "otter", "kestrel", "panther", "heron", "fox", "owl", "hawk",
		"raven", "lynx", "wren", "marten", "merlin", "swan", "elk", "shrike",
	}
	slugRand = rand.New(rand.NewSource(time.Now().UnixNano()))
)

func suggestProjectSlug() string {
	return slugAdjectives[slugRand.Intn(len(slugAdjectives))] + "-" + slugNouns[slugRand.Intn(len(slugNouns))]
}

type Turn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type TaskChainStep struct {
	Action string         `json:"action"`
	Args   map[string]any `json:"args,omitempty"`
}

type OrchestrateResult struct {
	SelectedSkill string          `json:"selected_skill,omitempty"`
	Command       string          `json:"command,omitempty"`
	Args          map[string]any  `json:"args,omitempty"`
	Reasoning     string          `json:"reasoning"`
	DirectReply   string          `json:"direct_reply,omitempty"`
	AutoLoop      bool            `json:"auto_loop,omitempty"`
	TaskChain     []TaskChainStep `json:"task_chain,omitempty"`
	IsTerminal    bool            `json:"is_terminal"`
}

func Orchestrate(userMsg string, catalog string, state *CrafterState, history []Turn, model string) (*OrchestrateResult, error) {
	return OrchestrateWithCallback(userMsg, catalog, state, history, model, nil)
}

func OrchestrateWithCallback(userMsg string, catalog string, state *CrafterState, history []Turn, model string, onChunk func(string)) (*OrchestrateResult, error) {
	if model == "" {
		if env := os.Getenv("GEM2_DEFAULT_LLM"); env != "" {
			model = env
		} else {
			// WP-AO-48: default Crafter LLM = gemini-2.5-pro
			model = "gemini-2.5-pro"
		}
	}

	sessionCtx := buildSessionContextFat(state, userMsg, history, model)
	systemPrompt := buildOrchestratorPrompt(catalog, state, history, sessionCtx)
	// WP-AO-35: substitute {{suggested_project_name}} placeholder with a fresh
	// random kebab-case slug for the no-project-active path.
	systemPrompt = strings.ReplaceAll(systemPrompt, "{{suggested_project_name}}", suggestProjectSlug())
	fullPrompt := systemPrompt + "\n\nUser message: " + userMsg + "\n\nRespond with JSON only."

	var raw string
	var err error

	if isVultrModel(model) {
		vultrKey := os.Getenv("VULTR_INFERENCE_API_KEY")
		if vultrKey == "" {
			return nil, fmt.Errorf("VULTR_INFERENCE_API_KEY not set for model %s", model)
		}
		raw, err = vultrSheepCall(vultrKey, vultrModelName(model),
			"You are an AI orchestrator. Parse user intent and respond with JSON only.", fullPrompt)
	} else {
		apiKey := os.Getenv("GEMINI_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("GEMINI_API_KEY not set")
		}
		raw, err = geminiGenerateStream(apiKey, model, fullPrompt, onChunk)
	}
	if err != nil {
		return nil, fmt.Errorf("orchestrator call: %w", err)
	}

	result, err := parseOrchestrateResult(raw)
	if err != nil {
		return &OrchestrateResult{
			DirectReply: raw,
			IsTerminal:  true,
			Reasoning:   "Failed to parse structured response, returning raw",
		}, nil
	}
	return result, nil
}

// failureIntentRegex — matches user messages asking about a failure / debug / fix.
var failureIntentRegex = regexp.MustCompile(`(?i)\b(investigate|debug|diagnose|why.*fail|exhaust|retri|stuck|broken|error|failure|fix.*it|what.*went|root.?cause)\b`)

// historyFailureRegex — matches assistant turns that announced a failure.
var historyFailureRegex = regexp.MustCompile(`(?i)(exhausted retries|unit_failed|verify.*fail|ai.?pilot.*fail)`)

// buildSessionContextFat returns the base session summary, augmented with fat
// failure evidence when failure-mode triggers fire. Triggers:
//
//	(a) userMsg matches failure-intent keywords (investigate, debug, why failed...)
//	(b) any unit in any active WP has STATE: FAILURE inline in its WP file
//	(c) recent assistant history mentions "Exhausted Retries" / "unit_failed"
//
// Cap: InputBudgetBytes(model) * 0.7 — leaves 30% headroom for system prompt,
// catalog, and the orchestrator's JSON response.
func buildSessionContextFat(state *CrafterState, userMsg string, history []Turn, model string) string {
	base := buildSessionContext(state)

	if state == nil {
		return base
	}

	// Cheap triggers first
	fatMode := false
	if failureIntentRegex.MatchString(userMsg) {
		fatMode = true
	}
	if !fatMode {
		for i := len(history) - 1; i >= 0 && i >= len(history)-5; i-- {
			if historyFailureRegex.MatchString(history[i].Content) {
				fatMode = true
				break
			}
		}
	}

	// State-based trigger — find any WP whose body contains "State: FAILURE"
	var failingWP *WorkPlanSummary
	var failingUnitIdx int
	if !fatMode {
		for i := range state.WorkPlans {
			wp := &state.WorkPlans[i]
			content, err := state.ReadWorkPlan(wp.ID)
			if err != nil {
				continue
			}
			if idx := strings.Index(content, "- State: FAILURE"); idx >= 0 {
				fatMode = true
				failingWP = wp
				failingUnitIdx = findUnitIndexAtOffset(content, idx)
				break
			}
		}
	} else if failingWP == nil {
		// Already in fat mode from cheap trigger — still find a target unit
		for i := range state.WorkPlans {
			wp := &state.WorkPlans[i]
			content, err := state.ReadWorkPlan(wp.ID)
			if err != nil {
				continue
			}
			if idx := strings.Index(content, "- State: FAILURE"); idx >= 0 {
				failingWP = wp
				failingUnitIdx = findUnitIndexAtOffset(content, idx)
				break
			}
			// fall back to the most recent IN_PROGRESS WP if no explicit FAILURE
			if failingWP == nil && wp.Status == "IN_PROGRESS" {
				failingWP = wp
				failingUnitIdx = wp.Completed + 1
			}
		}
	}

	if !fatMode {
		return base
	}

	// Compose fat evidence
	budget := int(float64(InputBudgetBytes(model)) * 0.7)
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\n──────────────────────────────────────\n")
	b.WriteString("**FAT-MODE ACTIVE** — failure evidence loaded below.\n")
	b.WriteString("──────────────────────────────────────\n\n")

	if failingWP != nil && failingUnitIdx > 0 {
		fc := composeFailureContext(failingWP.ID, failingUnitIdx, state, model)
		rendered := renderFailureContext(fc)
		if len(b.String())+len(rendered) > budget {
			room := budget - len(b.String()) - 200 // leave margin for fence text
			if room > 0 && len(rendered) > room {
				rendered = rendered[:room] + "\n... [budget cut at " + fmt.Sprintf("%d", budget) + " bytes]"
			}
		}
		b.WriteString(rendered)
		b.WriteString("\n")
		log.Printf("[ORCHESTRATOR_FAT] %s unit %d — %d/%d bytes, %d files",
			failingWP.ID, failingUnitIdx, fc.UsedBytes, fc.BudgetBytes, len(fc.FileContents))
	} else {
		b.WriteString("(No specific failing unit identified — fat-mode activated by keyword only.)\n")
	}

	return b.String()
}

// findUnitIndexAtOffset walks BACKWARDS from a byte offset in a WP file to find
// the unit header (### N. ...) that the offset belongs to. Returns 0 if no header.
func findUnitIndexAtOffset(content string, offset int) int {
	if offset >= len(content) {
		return 0
	}
	prefix := content[:offset]
	// Find the last "### N." marker before offset
	re := regexp.MustCompile(`### (\d+)\.`)
	matches := re.FindAllStringSubmatchIndex(prefix, -1)
	if len(matches) == 0 {
		return 0
	}
	last := matches[len(matches)-1]
	if last[2] < 0 || last[3] < 0 {
		return 0
	}
	numStr := prefix[last[2]:last[3]]
	var n int
	fmt.Sscanf(numStr, "%d", &n)
	return n
}

func buildSessionContext(state *CrafterState) string {
	if state == nil {
		return "No active session state.\n"
	}

	var b strings.Builder

	projBase := filepath.Base(state.ProjectDir)
	isSession := strings.HasPrefix(projBase, "Session") || len(projBase) == 8
	if isSession {
		b.WriteString("**Active project:** (none — no project bound to this session)\n")
	} else {
		b.WriteString(fmt.Sprintf("**Active project:** %s\n", projBase))
	}
	b.WriteString(fmt.Sprintf("**Counters:** PENDING:%d | IN_PROGRESS:%d | COMPLETED:%d | ABORTED:%d\n",
		state.Alarm.Pending, state.Alarm.InProgress, state.Alarm.Completed, state.Alarm.Aborted))

	if len(state.WorkPlans) == 0 {
		b.WriteString("**Work Plans:** none\n")
	} else {
		b.WriteString(fmt.Sprintf("**Work Plans:** %d total\n", len(state.WorkPlans)))
		for _, wp := range state.WorkPlans {
			nextUnit := ""
			if wp.Pending > 0 {
				nextUnit = fmt.Sprintf(", next: Unit %d", wp.Completed+wp.InProgress+1)
			}
			b.WriteString(fmt.Sprintf("  - %s: %s — %d/%d done [%s]%s\n",
				wp.ID, wp.Title, wp.Completed, wp.UnitCount, wp.Status, nextUnit))
		}
	}

	wsDir := filepath.Join(baseDir, ".gem-squared", "workspace")
	entries, err := os.ReadDir(wsDir)
	if err == nil && len(entries) > 0 {
		b.WriteString("**All projects in workspace:**\n")
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			slug := e.Name()
			projDir := filepath.Join(wsDir, slug)
			projState := NewCrafterState(projDir)
			projState.Refresh()
			wpCount := len(projState.WorkPlans)
			totalUnits := 0
			doneUnits := 0
			for _, wp := range projState.WorkPlans {
				totalUnits += wp.UnitCount
				doneUnits += wp.Completed
			}
			b.WriteString(fmt.Sprintf("  - **%s**: %d WPs, %d/%d units done\n", slug, wpCount, doneUnits, totalUnits))
		}
	}

	workspaceDir := filepath.Join(state.ProjectDir, "output", filepath.Base(state.ProjectDir))
	var wsFiles []string
	filepath.Walk(workspaceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(workspaceDir, path)
		wsFiles = append(wsFiles, rel)
		if len(wsFiles) >= 10 {
			return filepath.SkipAll
		}
		return nil
	})
	if len(wsFiles) > 0 {
		b.WriteString("**Recent workspace files:**\n")
		for _, f := range wsFiles {
			b.WriteString(fmt.Sprintf("  - %s\n", f))
		}
	}

	// TPMN contract auto-detect — scan uploaded_files/ for any markdown that
	// matches the WP-AO-24 5-block signature. If found, hint the orchestrator
	// to offer CE creation. WP-AO-26 Unit 5.
	uploadDir := filepath.Join(state.ProjectDir, "uploaded_files")
	if uploadEntries, err := os.ReadDir(uploadDir); err == nil {
		for _, e := range uploadEntries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(uploadDir, e.Name()))
			if err != nil {
				continue
			}
			head := string(data)
			if len(head) > 4096 {
				head = head[:4096]
			}
			if isTPMNContract(head) {
				wfHint, stageHint := guessSlugsFromContract(head)
				b.WriteString(fmt.Sprintf(
					"**Detected TPMN contract upload:** `uploaded_files/%s` (suggests CE slug `%s/%s`). Say `create CE` to deploy it as `/ce/%s/%s/`.\n",
					e.Name(), wfHint, stageHint, wfHint, stageHint))
				break // one detect-line is enough
			}
		}
	}

	return b.String()
}

// isTPMNContract returns true if the markdown head looks like a contract per
// the WP-AO-24 split format (presence of the 5 mandatory section headers).
func isTPMNContract(head string) bool {
	required := []string{"## A:", "## P_pre:", "## F:", "## B:", "## P_post:"}
	for _, h := range required {
		if !strings.Contains(head, h) {
			return false
		}
	}
	return true
}

// guessSlugsFromContract derives workflow / stage slug from the contract head
// for the auto-detect hint. Returns fallback "uploaded" / "contract" on failure.
func guessSlugsFromContract(head string) (string, string) {
	wf := "uploaded"
	stage := "contract"
	if m := regexp.MustCompile(`(?i)\*\*Workflow:\*\*\s*([^\n]+)`).FindStringSubmatch(head); len(m) > 1 {
		if s := simpleKebab(m[1]); s != "" {
			wf = s
		}
	}
	if m := regexp.MustCompile("`([a-z0-9_-]+):").FindStringSubmatch(head); len(m) > 1 {
		if s := simpleKebab(m[1]); s != "" {
			stage = s
		}
	}
	return wf, stage
}

func simpleKebab(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	out := []rune{}
	prevHyphen := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			out = append(out, r)
			prevHyphen = false
		} else if !prevHyphen && len(out) > 0 {
			out = append(out, '-')
			prevHyphen = true
		}
	}
	res := strings.Trim(string(out), "-")
	if len(res) > 64 {
		res = strings.Trim(res[:64], "-")
	}
	return res
}

func buildOrchestratorPrompt(catalog string, state *CrafterState, history []Turn, sessionCtx string) string {
	var b strings.Builder
	b.WriteString(`You are **GEM² AI Crafter** — a skillful engineer and agentic workflow orchestrator.

## Persona
You are "GEM² AI Crafter. Skillful Engineer." You speak with confidence, warmth, and technical precision.
When chatting freely, respond in this persona voice — be helpful, concise, and engineering-minded.

## Your Role
You are the SINGLE FRONT-DOOR for ALL user input. Every message comes through you.
Your job: understand what the user wants and return a structured JSON response that routes to the correct action.

## Available TPMN Skills
`)
	b.WriteString(catalog)
	b.WriteString("\n## Session Context\n")
	b.WriteString(sessionCtx)

	if len(history) > 0 {
		b.WriteString("\n## Recent Conversation (last 6 turns)\n")
		start := 0
		if len(history) > 6 {
			start = len(history) - 6
		}
		for _, t := range history[start:] {
			b.WriteString(fmt.Sprintf("[%s]: %s\n", t.Role, truncate(t.Content, 200)))
		}
	}

	b.WriteString(`
## Routing Rules

Analyze the user's message and return ONE of these action types.

### 0. CANONICAL PIPELINE for WORK intents (READ THIS FIRST)

WORK intents are ANY user request that asks to CREATE, BUILD, GENERATE, MAKE, WRAP, SET UP,
DEPLOY, PLAN, COMPLETE, FINISH, RUN, EXECUTE, IMPLEMENT, or otherwise produce a persistent
artifact. CE creation, project work, code generation — all WORK.

For EVERY WORK intent, you MUST emit a task_chain that runs through the pipeline:
  plan-work → proceed-work → verify-work → deploy-work

DO NOT route WORK intents to a direct selected_skill (single-step). DO NOT skip plan-work
and call create-ce / proceed-work directly. The pipeline is the I→P→E→V→D governance trace —
skipping it defeats the purpose of GEM² and produces flows without unit-work decomposition.

The ONLY exceptions to the pipeline:
  (a) META intents (see below) — no pipeline.
  (b) The user explicitly invokes "/create-ce", "/plan-work", etc. as a slash command —
      power-user direct path; honor it.
  (c) The user explicitly says "skip planning" or "just run create-ce" — honor literal intent.

For CE intents specifically: pass to plan-work the work arg:
  "Execute /create-ce on the uploaded contract to produce a live Contract-Executor"
plan-work will emit a 1-unit WP that proceed-work runs internally — proceed-work calls
/create-ce as the unit's action. NEVER include "create-ce" directly in the task_chain
unless rule (b) or (c) above fires.

═══════════════════════════════════════════════════════════════════════════════
SCAFFOLDING-vs-BUILD INTENT DISCRIMINATOR (WP-AO-43 — READ THIS BEFORE ROUTING)
═══════════════════════════════════════════════════════════════════════════════

NOT every "create project" message is a WORK intent. Apply this 2-step decision
tree to ANY message that mentions creating, making, setting up, or starting a
project, BEFORE applying the canonical-pipeline rule above:

STEP 1 — Did the user EXPLICITLY name the project in their message?
  YES: extract the name (slugify if needed) → skip the random-slug ask flow.
       Trigger patterns: "create X project", "make project X", "make X project",
       "new X project", "set up X project", "start X project", "create a project
       called X", "init X", where X looks like a project name (kebab-able noun
       phrase, sometimes with a hyphen or with multiple words).
  NO:  fall through to the existing random-slug ask flow (see Section 1
       direct_reply example with {{suggested_project_name}}).

STEP 2 — Does the message contain a BUILD-VERB follow-on AFTER the project intent?
  Build-verbs: "and build", "and complete", "and start", "and finish", "and code
  it", "and run", "and proceed", "and develop", "and implement", "and ship",
  "and deploy", "and make a working <thing>". Also: a CE-intent keyword (CE,
  create-ce, contract-executor, wrap as CE) anywhere in the message.
  YES: emit the 4-step canonical chain (with create-project prepended using the
       Step-1 name).
  NO:  emit selected_skill "create-project" ALONE — NO chain, NO ask, just
       create the scaffolding. The user has not asked you to build anything.

WHY THIS MATTERS: bare "create X project" is a scaffolding intent — the user
wants the workspace dir + alarm.md + work-plan/ subdir + project.json, and
nothing else. The plan-work skill, when fed a generic project name without
a CE intent or explicit build verb, will fabricate a 7-unit Express/Sequelize/
SQLite implementation plan that proceed-work then tries to execute and fails.
The 2026-05-18 retest produced exactly this cascade. DO NOT trigger the chain
on scaffolding-only intents.

PROJECT PREREQUISITE (refined): if Session Context shows "Active project:
(none)" AND the user has a WORK intent (build verb or CE intent) AND has NOT
explicitly named a project, emit a direct_reply asking with {{suggested_project_name}}.
If the user EXPLICITLY named the project, skip the ask and proceed directly.
Once a project is active, plan/proceed/verify/deploy steps run against it.

META intents (do NOT trigger the pipeline):
  - Greetings, free chat, questions
  - "/check-session", "/search-kg", "/init-session", "/end-session", "/list", "/help"
  - Status questions ("what's my session?", "where is the WP?")
  - Pure information queries

### 1. Task Chain → "task_chain" (for WORK intents and COMPOUND multi-step intents)

The chain executor runs each step MECHANICALLY — no LLM between steps. So plan the full sequence.

WORK pipeline (default for WORK intents on a project-active session):
  task_chain: [
    {"action":"plan-work","args":{"work":"<user intent summary>"}},
    {"action":"proceed-work","args":{"auto_loop":true}},
    {"action":"verify-work","args":{}},
    {"action":"deploy-work","args":{}}
  ]

Examples:
- "complete flappy-bird" (project not active) → task_chain: [
    {"action":"switch-project","args":{"query":"flappy-bird"}},
    {"action":"proceed-work","args":{"auto_loop":true}},
    {"action":"deploy-work","args":{}}
  ]
- "complete flappy-bird" (project already active) → task_chain: [
    {"action":"proceed-work","args":{"auto_loop":true}},
    {"action":"deploy-work","args":{}}
  ]
- "plan and start a snake game" → task_chain: [
    {"action":"create-project","args":{"project_name":"snake-game","stack":"html/js/css"}},
    {"action":"plan-work","args":{"work":"snake game"}},
    {"action":"proceed-work","args":{"auto_loop":true}},
    {"action":"deploy-work","args":{}}
  ]
- "create project for a flappy bird game -> complete autonomously" → task_chain: [
    {"action":"create-project","args":{"project_name":"flappy-bird","stack":"html/js/css"}},
    {"action":"plan-work","args":{"work":"flappy bird game"}},
    {"action":"proceed-work","args":{"auto_loop":true}},
    {"action":"deploy-work","args":{}}
  ]
- "create a CE with the contract attached" (Active project: a-ce-prj) → task_chain: [
    {"action":"plan-work","args":{"work":"Execute /create-ce on the uploaded contract to produce a live Contract-Executor"}},
    {"action":"proceed-work","args":{"auto_loop":true}},
    {"action":"verify-work","args":{}},
    {"action":"deploy-work","args":{}}
  ]
  ★ CE WORK → canonical 4-step pipeline. plan-work generates a single-unit WP whose action is /create-ce. proceed-work invokes /create-ce internally. NEVER emit create-ce directly in the chain — let plan-work + proceed-work decompose and execute it. This is the WP-AO-35 canonical-pipeline rule.

- "create CE for this contract" (Active project: my-claims-poc) → task_chain: [
    {"action":"plan-work","args":{"work":"Execute /create-ce on the uploaded contract"}},
    {"action":"proceed-work","args":{"auto_loop":true}},
    {"action":"verify-work","args":{}},
    {"action":"deploy-work","args":{}}
  ]

- "create a CE" (Active project: (none)) → direct_reply: "Let's set up a project for this work. Suggested name: {{suggested_project_name}} — confirm with 'ok' or type a different name."
  ★ NO PROJECT → ASK USER FIRST. Do NOT emit a task_chain on this turn. Wait for user reply, then on the NEXT turn emit:
    task_chain: [
      {"action":"create-project","args":{"project_name":"<user-confirmed-or-suggested>","stack":"tpmn"}},
      {"action":"plan-work","args":{"work":"Execute /create-ce on the uploaded contract"}},
      {"action":"proceed-work","args":{"auto_loop":true}},
      {"action":"verify-work","args":{}},
      {"action":"deploy-work","args":{}}
    ]

- "/create-ce" (literal slash command, power-user direct path, Active project: any) → selected_skill: "create-ce"
  ★ EXCEPTION (b) — explicit slash command bypasses the pipeline. Skill runs atomically.

═══ WP-AO-43 scaffolding-vs-build examples (apply BEFORE the WORK chain) ═══

- "create health-insurance-claim project" → selected_skill: "create-project"
    args: {"project_name": "health-insurance-claim", "stack": "tpmn"}
  ★ SCAFFOLDING-ONLY — explicit name present, NO build verb, NO CE intent.
  Emit a single skill. NO chain. NO random-slug ask. The user already typed
  the name. Just create the project scaffolding and stop.

- "make project flappy-bird" → selected_skill: "create-project"
    args: {"project_name": "flappy-bird", "stack": "html/js/css"}
  ★ SCAFFOLDING-ONLY — same rule. Stack inferred from project name hint.

- "create flappy-bird project and build it" → task_chain: [
    {"action":"create-project","args":{"project_name":"flappy-bird","stack":"html/js/css"}},
    {"action":"plan-work","args":{"work":"build flappy-bird game"}},
    {"action":"proceed-work","args":{"auto_loop":true}},
    {"action":"verify-work","args":{}},
    {"action":"deploy-work","args":{}}
  ]
  ★ BUILD INTENT — explicit name + "and build it" follow-on → full 4-step chain
  with create-project prepended.

- "set up a new project for me" → direct_reply: "Suggested name: {{suggested_project_name}} — confirm with 'ok' or type a different name."
  ★ NO EXPLICIT NAME — fall through to the random-slug ask flow. User has
  WORK intent ("set up a project") but no name given. Wait for reply.

═══════════════════════════════════════════════════════════════════════════

NOTE: "->" or "→" in user messages means sequential steps — treat as a compound intent and return a task_chain.
"complete autonomously" means: plan-work + proceed-work(auto_loop) + deploy-work.

RULES for task_chain:
- If the chain includes "proceed-work", ALWAYS append "deploy-work" as the LAST step.
- If the active project doesn't match the user's intent, prepend "switch-project".
- Each step has "action" (skill name or command name) and "args".
- proceed-work in a chain ALWAYS has args: {"auto_loop": true}.
- CANONICAL PIPELINE for WORK intents: plan-work → proceed-work → verify-work → deploy-work.
  Never emit "create-ce", "create-project" (alone), or other atomic-work skills directly in the chain
  for WORK intents — they must be units invoked BY proceed-work. The pipeline rule supersedes the
  prior task_chain: [create-project, create-ce] pattern.
- For WORK intent + Active project: (none) + NO explicit name in user message — emit direct_reply
  asking for project-name confirmation. The {{suggested_project_name}} placeholder is filled by the
  server with a random slug. Wait for user reply on next turn before emitting the chain.
- For WORK intent + Active project: <name> — emit the 4-step pipeline directly (no project step).
- WP-AO-43 SCAFFOLDING RULE: bare "create X project" / "make X project" / "new X project" — when
  the user explicitly names the project AND does NOT include a build-verb follow-on AND it is NOT
  a CE intent → emit selected_skill "create-project" ALONE. NO chain. NO random-slug ask. The user
  asked for scaffolding only — they will follow up with their own work intent later (upload a
  contract, type "build it", etc.). Do NOT decompose the project name into an implementation plan.

### 2. Single Skill → "selected_skill" (for simple intents)
- Creating a project → "create-project" (args: project_name, stack, description)
- Planning work → "plan-work" (args: work — description)
- Proceeding/executing/keep going → "proceed-work" (args: wp_id if specified)
- Checking status/progress → "check-session"
- Verifying results → "verify-work" (args: wp_id)
- Archiving → "archive-work" (args: wp_id)
- Updating plan → "update-work-plan" (args: wp_id, operation)
- Initializing → "init-session"
- Ending session → "end-session"
- Searching knowledge → "search-kg"
- **/create-ce as direct skill — POWER-USER ONLY**: only route to "create-ce" as a selected_skill
  when the user TYPES the slash command literally ("/create-ce" with the slash). All other CE
  intents (natural-language "create a CE", "wrap this contract", etc.) MUST go through the
  canonical pipeline above. The /create-ce skill itself is fine — proceed-work calls it as
  the unit's action.

For proceed-work: ALWAYS set "auto_loop": true UNLESS user says "step-by-step" or "sbs".
IMPORTANT: If user mentions a project name NOT currently active, use "switch-project" command or put it as first step in a task_chain.

### 3. System Command → "command"
- "clean work-plan" → command: "clean-workplans"
- "clean workspace" → command: "clean-workspace"
- "delete WP-XX" → command: "delete-workplan", args: {wp_id: "WP-XX"}
- "list files" → command: "list-workspace"
- "reset session" → command: "reset-session"
- "switch to {project}" → command: "switch-project", args: {query: "..."}
- "check all sessions" / "list projects" → command: "check-all-sessions"
- "deploy" / "publish" → command: "deploy-work", args: {slug: "..."}

### 4. New Session Greeting → "command": "__greeting__"
When message is "__init__" or conversation has NO prior turns and NO active work:
Return command: "__greeting__" with direct_reply greeting in persona.
If projects exist, mention them. If not, just greet.

### 5. Free Chat → "direct_reply"
Questions, discussion, explanations — respond in persona.

## Response Format
Return ONLY valid JSON:
{
  "task_chain": [{"action":"...", "args":{...}}, ...],
  "selected_skill": "skill-name",
  "command": "command-name",
  "args": {"key": "value"},
  "reasoning": "one-line explanation",
  "direct_reply": "response text",
  "auto_loop": true/false,
  "is_terminal": false
}

PRIORITY: task_chain > selected_skill > command > direct_reply.
If task_chain is present and non-empty, selected_skill and command are IGNORED.
Use task_chain for compound intents (2+ steps). Use selected_skill/command for single-step intents.
`)
	return b.String()
}

func parseOrchestrateResult(raw string) (*OrchestrateResult, error) {
	cleaned := raw
	if idx := strings.Index(cleaned, "```json"); idx >= 0 {
		cleaned = cleaned[idx+7:]
	} else if idx := strings.Index(cleaned, "```"); idx >= 0 {
		cleaned = cleaned[idx+3:]
	}
	if idx := strings.LastIndex(cleaned, "```"); idx >= 0 {
		cleaned = cleaned[:idx]
	}
	cleaned = strings.TrimSpace(cleaned)

	if start := strings.Index(cleaned, "{"); start > 0 {
		cleaned = cleaned[start:]
	}

	var result OrchestrateResult
	if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
		return nil, fmt.Errorf("parse orchestrate JSON: %w (raw: %.200s)", err, cleaned)
	}
	return &result, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
