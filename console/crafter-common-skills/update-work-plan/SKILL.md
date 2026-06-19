---
name: update-work-plan
description: >
  (What) Mutate a live WP — add, modify, reorder, or abort PENDING unit-works.
  (When) Mid-execution discovery: scope changed, new unit needed, CONTRACT needs revision.
  (Why) Sanctioned plan mutation path — no ad-hoc WP editing.
  (How) Identify WP → select operation → mutate PENDING units only → update WP file.
argument-hint: "[WP path or title] [operation: add|modify|abort|reorder]"
metadata:
  author: David Seo of GEM².AI
  version: 1.0.0
allowed-tools:
  - Read
  - Write
  - Edit
  - Bash(date *)
  - mcp__gem2-studio__gem2_task
  - mcp__gem2-studio__gem2_search
  - mcp__gem2-studio__gem2_knowledge
---

(* TPMN SKILL — update-work-plan *)

(* === Layers === *)
L0 ≜ "WP file is the sole state store — read, mutate PENDING units, write back"
L1 ≜ "gem2-studio MCP — additionally syncs task description to remote"

(* === Input === *)
A ≜ [
  project_slug: 𝕊,
  wp_path: Path?,                      (* direct path to WP file, or ⊥ *)
  work_title: 𝕊?,                     (* search term if wp_path not given *)
  operation: {add, modify, abort, reorder},
  target_units: Seq(ℕ)?,              (* 1-based unit indices — ⊥ = ask human *)
  description: 𝕊?,                    (* what to change — ⊥ = ask human *)
  layer: {L0, L1}                      (* inherited from /init-session *)
]

(* === Output === *)
B ≜ [
  project_slug: 𝕊,
  wp_path: Path,
  wp_id: 𝕊,
  operation: {add, modify, abort, reorder},
  units_affected: Seq(ℕ),             (* which units were changed, 1-based *)
  units_total: ℕ,                      (* new total after mutation *)
  units_pending: ℕ,                    (* remaining PENDING units *)
  change_summary: 𝕊,                  (* human-readable description of what changed *)
  updated_at: 𝕊                       (* ISO8601 *)
]

(* === Precondition === *)
P ≜ project_slug ≠ ⊥
    ∧ (wp_path ≠ ⊥ ∨ work_title ≠ ⊥)
    ∧ ".gem-squared/work-plan/" exists
    ∧ WP.STATUS ∈ {PENDING, IN_PROGRESS}   (* not archived, not completed *)

(* === Transform === *)
F ≜ <<
  1. Identify WP and validate state:
       IF wp_path provided → read that WP.
       IF only work_title → search work-plan/ for matching WP.
       Parse all units: index, title, STATUS, CONTRACT.
       Validate: WP is not archived (must be in work-plan/, not archive/).
       Validate: at least one PENDING unit exists (for modify/abort/reorder).
       Show current WP state: unit list with STATUS indicators.

  2. Ask human permission and determine scope:
       Show: WP title, operation requested, current unit list.
       IF operation = add:
         Ask: what new unit-work(s) to add (title, A, B, P, Clarity).
         Validate: total units after add ≤ 9 (Miller's law).
       IF operation = modify:
         Show PENDING units only (COMPLETED/IN_PROGRESS are immutable).
         Ask: which unit(s) to modify, what changes to CONTRACT.
       IF operation = abort:
         Show PENDING units only.
         Ask: which unit(s) to abort and reason.
       IF operation = reorder:
         Show PENDING units only.
         Ask: new order for PENDING units.
       IF denied → STOP, output B with units_affected = [].

  3. Execute mutation on WP file:
       IF operation = add:
         Append new unit-work(s) to ## Unit-Works section.
         Each new unit gets STATUS: PENDING, empty Result/State/Truth.
         CONTRACT format matches /plan-work output (A, B, P, Clarity, Unclear, Tags).
       IF operation = modify:
         Update CONTRACT fields (A, B, P, Clarity, Unclear, Tags) for target units.
         Tags: add, remove, or replace tags on PENDING units.
           (* Tag format: /^[a-z]+-[a-z]+(-[a-z]+)*$/ — {verb-ing}-{object} *)
         Preserve STATUS: PENDING — do not change status.
         Do NOT touch Result/State/Truth fields (empty for PENDING).
       IF operation = abort:
         Mark target units STATUS: PENDING → ABORTED.
         Write `- Result: Aborted by /update-work-plan ({reason}).`
         Leave State and Truth empty.
       IF operation = reorder:
         Renumber PENDING units to new order.
         COMPLETED/IN_PROGRESS units retain their original numbers.
       L0: Write updated WP file.
       L1 (additionally): gem2_task_update(task_id, description=updated summary).
       Record updated_at timestamp. Output B.
>>

(* === Constraint === *)
CONSTRAINT ≜ [
  ⊢ NEVER execute work — mutation only, execution is /proceed-work mandate,
  ⊢ NEVER verify — that is /verify-work mandate,
  ⊢ NEVER archive — that is /archive-work mandate,
  ⊢ NEVER create new WPs — that is /plan-work mandate,
  ⊢ NEVER touch COMPLETED units — their Results are recorded facts,
  ⊢ NEVER touch IN_PROGRESS units — the executor owns them,
  ⊢ ONLY mutate PENDING units — the unexecuted plan is mutable,
  ⊢ NEVER exceed 9 units per WP — Miller's law ceiling,
  ⊢ NEVER mutate without human permission — always ask first,
  ⊢ gem2-studio sync failure does NOT block mutation — WP file is the source of truth
]

(* === Invariant === *)
INV ≜ [
  ⊢ PENDING units are mutable — they are plans, not facts,
  ⊢ COMPLETED units are immutable — their Result/State/Truth are recorded facts,
  ⊢ IN_PROGRESS units are immutable — the executor owns them,
  ⊢ WP-level STATUS unchanged by this skill — derived from unit statuses by other skills,
  ⊢ Every added unit has a CONTRACT (A → B | P) — no uncontracted work,
  ⊢ Every added unit has a Clarity % — no unassessed scope,
  ⊢ Every added unit has 1-3 Tags in {verb-ing}-{object} format — searchable by /search-kg,
  ⊢ Tags on PENDING units are mutable — tags on COMPLETED/IN_PROGRESS units are immutable,
  ⊢ Total units ≤ 9 after mutation — Miller's law ceiling,
  ⊢ WP file is ALWAYS updated (L0) — gem2-studio sync is best-effort (L1),
  ⊢ MANDATE BOUNDARY: plan mutation only — between /plan-work (creates) and /proceed-work (executes)
]

(* === Pre-Execution Dialog === *)
Ask_Human ≜ <<
  [field: "confirm",
   prompt: "Update WP {id}? Operation: {op}. Shows affected units and changes.",
   required: ⊤]
>>

(* === Post-Execution Routing === *)
Routing ≜ [
  units_pending > 0   → /proceed-work (continue execution with updated plan),
  units_pending = 0   → /verify-work (all units terminal),
  operation = abort
    ∧ units_pending = 0 → /archive-work (everything aborted)
]