---
name: verify-by-gem2
description: >
  (What) External epistemic verification via gem2_truth_filter — scores, alignment, SPT.
  (When) Only when user explicitly requests. Never auto-triggered.
  (Why) Independent second opinion beyond /verify-work. (How) gem2_truth_filter → Truth + log file.
  Requires gem2-epistemic-gateway MCP.
argument-hint: "[WP path and unit index]"
metadata:
  author: David Seo of GEM².AI
  version: 13.0.0
allowed-tools:
  - Read
  - Write
  - Edit
  - Bash(date *)
  - Bash(mkdir *)
  - mcp__gem2-studio__gem2_task
  - mcp__gem-epistemic__gem2_truth_filter
---

(* TPMN SKILL — verify-by-gem2 *)

(* === Layers === *)
DEFAULT ≜ "gem2-studio MCP — sync truth scores to remote task after verification"
FALLBACK ≜ "WP file reads/writes only — local state is always updated, when gem2-studio MCP unreachable"
EXT ≜ "gem2-epistemic-gateway — HARD REQUIREMENT, this skill cannot run without it"

(* NOTE: This entire skill is OPTIONAL in the lifecycle.
   /verify-work (local) is sufficient. This skill adds external epistemic verification.
   It requires gem2-epistemic-gateway — a separate service from gem2-studio.
   If epistemic-gateway is not configured → skill is unavailable, not an error. *)

(* === Input === *)
A ≜ [
  project_slug: 𝕊,
  wp_path: Path?,                      (* direct path to WP file, or ⊥ *)
  work_title: 𝕊?,                     (* search term if wp_path not given *)
  unit_index: ℕ?,                     (* specific unit to verify, or ⊥ = all COMPLETED units *)
  layer: {DEFAULT, FALLBACK}           (* inherited from /init-session — applies to gem2-studio sync *)
]

(* === Output === *)
B ≜ [
  project_slug: 𝕊,
  wp_path: Path,
  wp_id: 𝕊,
  unit_results: Seq([
    unit_index: ℕ,
    unit_title: 𝕊,
    truth_score: ℕ,                    (* 0-100% from gem2_truth_filter *)
    alignment_score: ℝ?,               (* 0.0-1.0, CONTRACT.B ↔ Result alignment *)
    alignment_issues: Seq(𝕊)?,        (* what misaligned, if any *)
    spt_violations: Seq(𝕊)?,          (* SPT issues: State→Trait, Local→Global, Incremental→Mass *)
    eef_extrapolation: 𝔹,             (* true if extrapolation detected *)
    explanation: 𝕊                     (* plain English summary from truth_filter *)
  ]),
  log_path: Path,                      (* .gem-squared/truth-logs/{wp_id}.md *)
  verified_at: 𝕊                      (* ISO8601 *)
]

(* === Precondition === *)
P ≜ project_slug ≠ ⊥
    ∧ (wp_path ≠ ⊥ ∨ work_title ≠ ⊥)
    ∧ target unit(s) STATUS = COMPLETED
    ∧ target unit(s) have Result field populated
    ∧ gem2-epistemic-gateway MCP reachable (configured in .mcp.json)   (* HARD — not optional *)

(* === Transform === *)
F ≜ <<
  1. Record verified_at timestamp.
     Verify gem2-epistemic-gateway is reachable.
       IF not reachable → STOP, report: "gem2-epistemic-gateway not configured. This skill is optional — /verify-work provides local verification."
     Identify WP:
       IF wp_path provided → read that WP.
       IF only work_title → search work-plan/ for matching WP.
  2. Identify target unit(s):
       IF unit_index provided → verify that single unit.
       ELSE → collect all COMPLETED units with non-empty Result.
       Skip ABORTED units and units without Result.
  3. FOR each target unit:
       Extract CONTRACT.B as prompt (the specification the output should align with).
       Extract Result as content (the actual deliverable to evaluate).
       Build session_context from WP header + unit's A and P fields.
       Call remote MCP tool:
         gem2_truth_filter(
           content         = Result,
           prompt          = CONTRACT.B,
           session_context = context string
         )
       Parse response: truth_score, alignment_score, alignment_issues,
                        spt_violations, eef_extrapolation, explanation.
  4. Record summary in WP file per unit:
       Edit each verified unit → write Truth field:
         - Truth: {truth_score}% | Alignment: {alignment_score}
         - SPT: {violations or "none"} | EEF: {extrapolation or "none"}
       Update WP updated_at timestamp.
       gem2_task_update(task_id,
         metadata_json={"truth_scores": unit_results}).
         FALLBACK (gem2-studio MCP unavailable): skip — WP file stores the scores.
  5. Write detailed truth log:
       mkdir -p .gem-squared/truth-logs/
       Write .gem-squared/truth-logs/{wp_id}.md:

         # Truth Log: {wp_id}
         **WP:** {wp_title} | **Verified:** {verified_at}
         **Units evaluated:** {count} | **Skipped (ABORTED):** {count}

         ## Unit 1: {title} — {truth_score}%
         ### Scores
         - Truth: {truth_score}%
         - Alignment: {alignment_score}
         - EEF extrapolation: {true/false}
         ### CONTRACT.B (expected)
         {full CONTRACT.B text}
         ### Result (actual)
         {full Result text}
         ### Alignment Issues
         {alignment_issues list, or "None"}
         ### SPT Violations
         {spt_violations list, or "None"}
         ### Explanation
         {full explanation from gem2_truth_filter — verbatim}

         ## Unit 2: {title} — {truth_score}%
         (repeat for each verified unit)

         ## Raw Responses
         (full gem2_truth_filter JSON responses per unit — for audit trail)

       IF log already exists (re-verification) → overwrite with fresh results.
     Output B.
>>

(* === Constraint === *)
CONSTRAINT ≜ [
  ⊢ NEVER fix or modify output — report scores only,
  ⊢ NEVER override /verify-work STATE — this is a complementary external signal,
  ⊢ NEVER re-execute work — that is /proceed-work mandate,
  ⊢ NEVER write STATE to WP file — that is /verify-work mandate,
  ⊢ One gem2_truth_filter call per unit-work — not per paragraph or per file,
  ⊢ Remote tool failure (network, credits exhausted) → report failure, do not retry silently,
  ⊢ gem2-studio MCP sync failure does NOT block truth recording — WP file is the source of truth
]

(* === Invariant === *)
INV ≜ [
  ⊢ Verification is performed by EXTERNAL service (gem2-epistemic-engine via gateway) — not self-judgment,
  ⊢ truth_score is gem2_truth_filter's judgment — this skill does not interpret or threshold it,
  ⊢ alignment_score measures CONTRACT.B ↔ Result alignment via external LLM evaluation,
  ⊢ SPT violations flagged verbatim from truth_filter — no filtering or suppression,
  ⊢ This skill produces advisory output — human decides whether scores warrant action,
  ⊢ /verify-work = self-check (binary, local), /verify-by-gem2 = external check (scored, requires EXT),
  ⊢ WP file stores the scores (Truth field) — routing signal,
  ⊢ Log file stores the full truth_filter response — decision support,
  ⊢ One log file per WP, structured with per-unit sections + raw responses,
  ⊢ Re-verification overwrites the log — latest run is the current truth,
  ⊢ gem2-studio sync is DEFAULT (best-effort on failure)
]

(* === Post-Execution Routing === *)
Routing ≜ [
  scores presented → human decides:
    → accept and /archive-work,
    → /proceed-work to redo specific unit,
    → /plan-work to re-decompose
]
