---
name: proceed-work
description: >
  (What) Execute ONE unit-work — fulfill CONTRACT, record Result.
  (When) After /plan-work, or resuming from /check-session. One unit per invocation.
  (Why) Produce results for /verify-work. (How) Find PENDING unit → ask human → execute → record.
argument-hint: "[WP path or work title]"
metadata:
  author: David Seo of GEM².AI
  version: 13.0.0-draft
allowed-tools:
  - Read
  - Write
  - Edit
  - Glob
  - Grep
  - Bash
  - mcp__gem2-studio__gem2_task
  - mcp__gem2-studio__gem2_search
  - mcp__gem2-studio__gem2_knowledge
  - mcp__gem2-studio__gem2_session
---

(* TPMN SKILL — proceed-work *)

(* === Layers === *)
L0 ≜ "WP file is the sole state store — read, update STATUS, write Result"
L1 ≜ "gem2-studio MCP — additionally syncs task STATUS to remote"

(* === Input === *)
A ≜ [
  project_slug: 𝕊,
  wp_path: Path?,                      (* direct path to WP file, or ⊥ *)
  work_title: 𝕊?,                     (* search term if wp_path not given *)
  unit_index: ℕ?,                     (* specific unit to execute, or ⊥ = first PENDING *)
  layer: {L0, L1}                      (* inherited from /init-session *)
]

(* === Output === *)
B ≜ [
  project_slug: 𝕊,
  wp_path: Path,
  wp_id: 𝕊,
  unit_index: ℕ,                      (* which unit was executed, 1-based *)
  unit_title: 𝕊,
  unit_status: {COMPLETED, ABORTED},  (* this unit's result *)
  units_done: ℕ,                      (* total completed units in this WP *)
  units_total: ℕ,
  wp_status: {IN_PROGRESS, COMPLETED, ABORTED},  (* derived from all unit statuses *)
  updated_at: 𝕊,                      (* ISO8601 *)
  output_summary: 𝕊                   (* what was accomplished in this unit *)
]

(* === Precondition === *)
P ≜ project_slug ≠ ⊥
    ∧ (wp_path ≠ ⊥ ∨ work_title ≠ ⊥)
    ∧ ".gem-squared/work-plan/" exists

(* === Transform === *)
F ≜ <<
  1. Record started_at timestamp.
     Identify WP:
       IF wp_path provided → read that WP.
       IF only work_title → search work-plan/ for matching WP.
       IF neither → present active/pending works from alarm.md.
  2. Find target unit-work:
       IF unit_index provided → use that unit.
       ELSE → find first unit with STATUS: PENDING in WP file.
       IF no PENDING units remain → wp_status=COMPLETED, output B, STOP.
  3. Ask human permission:
       Show: WP title, target unit title, its CONTRACT (A→B|P), clarity %.
       Show: progress so far (units_done / units_total).
       IF denied → mark unit STATUS: ABORTED, output B, STOP.
  4. Execute the single unit-work:
       L0: Update unit STATUS: PENDING → IN_PROGRESS in WP file.
           Update WP-level STATUS → IN_PROGRESS in WP header.
       L1 (additionally): gem2_task_update(task_id, status=IN_PROGRESS).
       Read unit's CONTRACT (A, B, P) → fulfill B given A under P.
       Execution strategy is executor's choice (blackbox).
       On completion:
         L0: Update unit STATUS → COMPLETED, write Result in WP file.
             Refine Tags if implementation diverged from plan:
               add tags for discovered concerns, keep existing valid tags.
               (* Tag format: /^[a-z]+-[a-z]+(-[a-z]+)*$/ — {verb-ing}-{object} *)
         L1 (additionally): gem2_task_update if wp_status changes.
       Record updated_at timestamp. Output B.
>>

(* === Constraint === *)
CONSTRAINT ≜ [
  ⊢ ONE unit-work per invocation — never batch multiple units,
  ⊢ NEVER proceed without human permission — always ask first,
  ⊢ NEVER verify results — that is /verify-work mandate,
  ⊢ NEVER archive — that is /archive-work mandate,
  ⊢ NEVER plan or decompose — that is /plan-work mandate,
  ⊢ NEVER modify CONTRACTs — work against what was planned,
  ⊢ Execution strategy is executor's blackbox — skill does not dictate how,
  ⊢ NEVER write State field — that is /verify-work exclusive mandate,
  ⊢ gem2-studio sync failure does NOT block execution — WP file is the source of truth
]

(* === Invariant === *)
INV ≜ [
  ⊢ Per-unit STATUS in WP file is the source of truth for unit progress,
  ⊢ Unit STATUS transitions: PENDING → IN_PROGRESS → COMPLETED or ABORTED,
  ⊢ WP-level STATUS derived: any IN_PROGRESS → IN_PROGRESS, all COMPLETED → COMPLETED,
  ⊢ Result field filled in WP file after each unit completes,
  ⊢ Tags may be refined on completion — add implementation-discovered tags, keep valid plan-time tags,
  ⊢ Human permission required before execution — no silent work,
  ⊢ CONTRACTs are read-only during execution,
  ⊢ WP file is ALWAYS updated (L0) — gem2-studio sync is best-effort (L1)
]

(* === Pre-Execution Dialog === *)
Ask_Human ≜ <<
  [field: "confirm",
   prompt: "Proceed with unit-work {N}: {title}? (shows CONTRACT + progress)",
   required: ⊤]
>>

(* === Post-Execution Routing === *)
Routing ≜ [
  wp_status = IN_PROGRESS  → /proceed-work (next PENDING unit),
  wp_status = COMPLETED    → /verify-work (verify all results against CONTRACTs),
  wp_status = ABORTED      → /archive-work
]