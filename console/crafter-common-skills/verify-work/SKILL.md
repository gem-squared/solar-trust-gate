---
name: verify-work
description: >
  (What) Verify results against CONTRACTs — determine STATE (SUCCESS|FAILURE) per unit.
  (When) After all units complete (default verification path).
  (Why) Acceptance gate before /archive-work. (How) Result vs CONTRACT.B → State + log file.
argument-hint: "[WP path or work title]"
metadata:
  author: David Seo of GEM².AI
  version: 12.0.0-draft
allowed-tools:
  - Read
  - Write
  - Edit
  - Bash(date *)
  - Bash(mkdir *)
  - mcp__gem2-studio__gem2_task
---

(* TPMN SKILL — verify-work *)

(* === Layers === *)
L0 ≜ "Fully local — reads WP file, compares Result vs CONTRACT.B, writes State to WP file"
L1 ≜ "Additionally syncs state to gem2-studio task (state field)"

(* === Input === *)
A ≜ [
  project_slug: 𝕊,
  wp_path: Path?,                      (* direct path to WP file, or ⊥ *)
  work_title: 𝕊?,                     (* search term if wp_path not given *)
  layer: {L0, L1}                      (* inherited from /init-session *)
]

(* === Output === *)
B ≜ [
  project_slug: 𝕊,
  wp_path: Path,
  wp_id: 𝕊,
  overall_state: {SUCCESS, FAILURE},
  unit_results: Seq([
    unit_index: ℕ,
    unit_title: 𝕊,
    state: {SUCCESS, FAILURE},
    detail: 𝕊                         (* what passed or what specifically failed *)
  ]),
  failures: Seq(𝕊)?,                  (* failed unit titles + reasons, ⊥ if all SUCCESS *)
  log_path: Path,                      (* .gem-squared/verify-work-logs/{wp_id}.md *)
  verified_at: 𝕊                      (* ISO8601 *)
]

(* === Precondition === *)
P ≜ project_slug ≠ ⊥
    ∧ (wp_path ≠ ⊥ ∨ work_title ≠ ⊥)
    ∧ wp.STATUS = COMPLETED
    ∧ all unit STATUS ∈ {COMPLETED, ABORTED}

(* === Transform === *)
F ≜ <<
  1. Record verified_at timestamp.
     Identify WP:
       IF wp_path provided → read that WP.
       IF only work_title → search work-plan/ for matching WP.
     Verify precondition: WP STATUS = COMPLETED (all units done).
       IF not COMPLETED → STOP, report: "WP not ready for verification."
  2. Parse all unit-works from WP:
       Extract each unit's CONTRACT (A, B, P).
       Extract each unit's Result (recorded by /proceed-work).
       Skip ABORTED units (they have no result to verify).
  3. FOR each COMPLETED unit-work, evaluate STATE by formal verification:
       Let b = actual Result (recorded by /proceed-work).
       Let B = CONTRACT.B (expected output specification).
       Let P = CONTRACT.P (preconditions and constraints).

       (* Verification predicate — M layer *)
       unit.state = SUCCESS ⟺
         (∀ field ∈ B: b[field] ≠ ⊥ ∧ type(b[field]) = B[field].type)
         ∧ P(a, b) holds

       In practice:
         - Field coverage: ∀ field ∈ B → b[field] ≠ ⊥ (every contracted output field exists in result)
         - Type conformance: type(b[field]) = B[field].type (result types match contract types)
         - Constraint satisfaction: P(a, b) (preconditions and invariants hold against actual input/output)
       IF any predicate fails → unit.state = FAILURE. Record which predicate failed and why.
  4. Determine overall STATE:
       IF all verified units SUCCESS → overall_state = SUCCESS.
       IF any unit FAILURE → overall_state = FAILURE.
     Edit WP file:
       Write `- State: SUCCESS` or `- State: FAILURE` per unit-work.
       Do NOT write WP-level STATE in header — that is /archive-work mandate.
       Update WP updated_at timestamp.
     IF layer = L1: gem2_task_update(task_id, state=overall_state).
  5. Write detailed verification log:
       mkdir -p .gem-squared/verify-work-logs/
       Write .gem-squared/verify-work-logs/{wp_id}.md:

         # Verification Log: {wp_id}
         **WP:** {wp_title} | **Verified:** {verified_at}
         **Overall:** {overall_state} | **Units verified:** {count} | **Skipped (ABORTED):** {count}

         ## Unit 1: {title} — {STATE}
         ### CONTRACT.B (expected)
         {full CONTRACT.B text}
         ### Result (actual)
         {full Result text}
         ### Judgment
         {detailed comparison — what matched, what didn't, why STATE was determined}

         ## Unit 2: {title} — {STATE}
         (repeat for each verified unit)

         ## Summary
         {overall assessment — which units passed, which failed, failure reasons}

       IF log already exists (re-verification) → overwrite with fresh results.
     Output B.
>>

(* === Constraint === *)
CONSTRAINT ≜ [
  ⊢ NEVER fix failures — report only,
  ⊢ NEVER re-execute work — that is /proceed-work mandate,
  ⊢ NEVER modify work output or Result fields — verify against what exists,
  ⊢ NEVER archive — that is /archive-work mandate,
  ⊢ NEVER apply subjective judgment — verify against CONTRACTs only,
  ⊢ Verification is binary per unit: SUCCESS or FAILURE — no partial credit,
  ⊢ gem2-studio sync failure does NOT block verification — WP file is the source of truth
]

(* === Invariant === *)
INV ≜ [
  ⊢ STATE is written to WP file alongside STATUS — separate concerns (STATUS = did it run, STATE = did it pass),
  ⊢ overall_state = FAILURE if ANY unit fails — no partial success,
  ⊢ Failures include specific reasons — not just pass/fail,
  ⊢ ABORTED units are skipped (no result to verify) — they do not affect overall STATE,
  ⊢ WP file stores the verdict (State field) — routing signal,
  ⊢ Log file stores the full reasoning (CONTRACT.B vs Result comparison) — decision support,
  ⊢ One log file per WP, structured with per-unit sections,
  ⊢ Re-verification overwrites the log — latest run is the current truth,
  ⊢ WP file is the source of truth for STATE — L0 baseline,
  ⊢ L1 syncs state to gem2-studio task — best-effort, non-blocking
]

(* === Post-Execution Routing === *)
Routing ≜ [
  overall_state = SUCCESS  → /archive-work (persist the win),
  overall_state = FAILURE  → present failures to human:
    → human may /plan-work to re-decompose failed units,
    → human may /proceed-work to retry specific unit,
    → human may /archive-work with FAILURE state accepted
]