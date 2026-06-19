---
name: plan-work
description: >
  (What) Decompose work into ≤9 unit-works with CONTRACTs and clarity %.
  (When) New work request, or sub-decomposition of low-clarity units.
  (Why) CONTRACTs before execution. (How) Pattern search → decompose → write WP file.
argument-hint: "[work description or WP path for decomposition]"
metadata:
  author: David Seo of GEM².AI
  version: 11.0.0-draft
allowed-tools:
  - Read
  - Write
  - Bash(date *)
  - Bash(uuidgen *)
  - mcp__gem2-studio__gem2_task
  - mcp__gem2-studio__gem2_search
  - mcp__gem2-studio__gem2_knowledge
  - mcp__gem2-studio__gem2_skill
---

(* TPMN SKILL — plan-work *)

(* === Layers === *)
L0 ≜ "git + .gem-squared/ files — local ID generation, AI knowledge for patterns"
L1 ≜ "gem2-studio MCP — semantic pattern search, remote task registration, dual persistence"

(* === Input === *)
A ≜ [
  work: 𝕊,                            (* what needs to be planned *)
  project_slug: 𝕊,
  time_stamp: 𝕊,                      (* ISO8601 — when this planning was triggered *)
  layer: {L0, L1},                     (* inherited from /init-session *)
  parent_wp: Path?,                    (* ⊥ for top-level, path for sub-decomposition *)
  parent_unit_index: ℕ?               (* which unit in parent is being decomposed *)
]

(* === Output === *)
B ≜ [
  wp_path: Path,                       (* e.g., ".gem-squared/work-plan/WP-ST-57.md" *)
  wp_id: 𝕊,                           (* local identifier, e.g., "WP-ST-57" *)
  task_id: 𝕊,                         (* uuid8 — L0: local-generated, L1: gem2-studio-assigned *)
  created_at: 𝕊,                      (* ISO8601 — recorded in WP file header *)
  unit_count: ℕ,                       (* 1-9 *)
  avg_clarity: ℕ,                      (* 0-100, average across all units *)
  references_used: Seq(𝕊)?            (* patterns that informed the plan — gem2-studio or local *)
]

(* === Precondition === *)
P ≜ work ≠ ⊥
    ∧ project_slug ≠ ⊥
    ∧ ".gem-squared/work-plan/" exists

(* === CE-INTENT TEMPLATE (WP-AO-35) === *)
(* When the `work` argument matches a CE-creation intent (regex: *)
(*   /create.*CE|wrap.*contract|execute.*\/?create-ce/i              *)
(* emit a MINIMAL 1-unit WP rather than decomposing into many units. *)
(* The single unit's action is /create-ce, file_path="" so the       *)
(* handler auto-detects the most recent TPMN contract markdown in    *)
(* {ProjectDir}/uploaded_files/.                                     *)
(*                                                                   *)
(* Template:                                                         *)
(*   Unit 1: Execute /create-ce on the uploaded contract             *)
(*     A: TPMN contract markdown in uploaded_files/                  *)
(*     B: live CE registered at /ce/{wf}/{stage}/ + viewer URL       *)
(*     P: contract file present, parses as 5-block format            *)
(*     Clarity: 90%                                                  *)
(*     Action: /create-ce with file_path="" (auto-detect)            *)
(*     Tags: [executing-create-ce, registering-live-endpoint]        *)
(*                                                                   *)
(* This template lets proceed-work invoke /create-ce as the unit's   *)
(* action — the orchestrator routes CE work through the canonical    *)
(* pipeline plan-work → proceed-work → verify-work → deploy-work     *)
(* rather than calling /create-ce directly. See WP-AO-35.            *)

(* === Transform === *)
F ≜ <<
  1. Search for relevant patterns via /search-kg:
       Invoke /search-kg(query=work, project_slug, scope=all, limit=5).
         /search-kg L0: tag-based search on archive/ + work-plan/ files.
         /search-kg L1 (additionally): gem2_search(semantic, entity_type='contract').
       Also: Read project skill references/ for project-specific context.
       Record references_used from /search-kg B.results.
  2. Decompose into 1-9 unit-works (Miller's law: 7±2).
     For each unit-work define:
       CONTRACT: A → B | P,
       Clarity %: 0-100,
       Unclear: what is ambiguous if < 100%,
       Tags: 1-3 tags in {verb-ing}-{object} format derived from CONTRACT A/B.
         (* Tag format: /^[a-z]+-[a-z]+(-[a-z]+)*$/ *)
         (* Tags capture intent — what this unit does, searchable by /search-kg *)
  3. Derive WP number and task_id:
     IF parent_wp = ⊥ → next WP-ST-{N} from existing files in work-plan/.
     IF parent_wp ≠ ⊥ → WP-ST-{parent_N}-{child_M}.
     Generate task_id:
       L0: `uuidgen | cut -c1-8` → local uuid8.
       L1: gem2_task_create(title, project_slug, status=PENDING,
           metadata_json={"wp_id": "WP-ST-{id}"}) → returns server task_id.
     Write .gem-squared/work-plan/WP-ST-{id}.md:

       # WP-ST-{id}: {title}
       **STATUS:** PENDING | **STATE:** — | **task_id:** {task_id}
       **created_at:** {time_stamp} | **project_slug:** {project_slug}

       ## Unit-Works
       ### 1. {title} | STATUS: PENDING
       - A: {input state}
       - B: {output state}
       - P: {preconditions}
       - Clarity: {N}%
       - Unclear: {what is ambiguous}
       - Tags: [{verb-ing}-{object}, ...] (1-3 tags, searchable by /search-kg)
       - Result: (filled by /proceed-work)
       - State: (filled by /verify-work — SUCCESS or FAILURE)
       - Truth: (filled by /verify-by-gem2 — score% | Alignment | SPT | EEF)

       ### 2. {title} | STATUS: PENDING
       - A: / B: / P: / Clarity: / Unclear:
       - Result:
       - State:
       - Truth:

       (repeat for each unit-work, max 9)

       ## References
       - {patterns used}
     IF parent_wp ≠ ⊥ → update parent DECOMPOSITION section.
     Calculate avg_clarity. Output B.
>>

(* === Constraint === *)
CONSTRAINT ≜ [
  ⊢ NEVER execute work — planning and CONTRACT creation only,
  ⊢ NEVER exceed 9 sub-works per level — Miller's law ceiling,
  ⊢ PREFER gem2-studio patterns when available (L1) — fall back to AI knowledge (L0),
  ⊢ NEVER decide recursion depth — human decides when clarity % is sufficient,
  ⊢ gem2-studio MCP unavailability does NOT block planning — local uuid8 + AI knowledge is sufficient
]

(* === Invariant === *)
INV ≜ [
  ⊢ Every unit-work has a CONTRACT (A → B | P) — no uncontracted work,
  ⊢ Every unit-work has a clarity % — no unassessed scope,
  ⊢ Every unit-work has 1-3 Tags in {verb-ing}-{object} format — searchable by /search-kg,
  ⊢ Every unit-work has its own STATUS line (PENDING → IN_PROGRESS → COMPLETED/ABORTED),
  ⊢ Every unit-work has a Result field (empty until /proceed-work fills it),
  ⊢ WP-level STATUS derived from unit statuses: all COMPLETED → COMPLETED, any IN_PROGRESS → IN_PROGRESS,
  ⊢ WP file is ALWAYS written (L0 baseline) — it is the source of truth,
  ⊢ L1: dual persistence — WP file + gem2-studio task, bidirectional link (task_id ↔ wp_id),
  ⊢ L0: single persistence — WP file only, task_id is local uuid8,
  ⊢ Child WPs reference parent, parent tracks children — tree is navigable
]

(* === Pre-Execution Dialog === *)
Ask_Human ≜ <<
  [field: "work",
   prompt: "What work needs to be planned?",
   required: ⊤]
>>

(* === Post-Execution Routing === *)
Routing ≜ [
  avg_clarity ≥ 70    → /proceed-work (clear enough to execute),
  avg_clarity < 70    → suggest further decomposition or ask human for clarification,
  parent_wp ≠ ⊥       → return to parent context
]