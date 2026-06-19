---
name: check-session
description: >
  (What) Read-only session status — counters, active/pending WPs, divergence.
  (When) After /init-session, or anytime human asks for status.
  (Why) Project state visibility. (How) alarm.md + work-plan/ + git log → status report.
metadata:
  author: David Seo of GEM².AI
  version: 11.0.0-draft
allowed-tools:
  - Read
  - Bash(git *)
  - Bash(date *)
  - mcp__gem2-studio__gem2_session
  - mcp__gem2-studio__gem2_task
  - mcp__gem2-studio__gem2_status
---

(* TPMN SKILL — check-session *)

(* === Layers === *)
L0 ≜ "git + .gem-squared/ files — local state only"
L1 ≜ "gem2-studio MCP — adds remote state comparison and divergence detection"

(* === Input === *)
A ≜ [
  project_slug: 𝕊,
  session_started_at: 𝕊,              (* ISO8601 — from init-session's alarm.md timestamp *)
  alarm_path: ".gem-squared/alarm.md",
  work_plan_dir: ".gem-squared/work-plan/",
  layer: {L0, L1}                      (* inherited from /init-session *)
]

(* === Output === *)
B ≜ [
  project_slug: 𝕊,
  checked_at: 𝕊,                      (* ISO8601 — when this check was performed *)
  layer: {L0, L1},
  counters: 𝕊,                        (* e.g., "PENDING:2 | IN_PROGRESS:1 | COMPLETED:55 | DECOMPOSED:0 | ABORTED:0" *)
  active_works: Seq(𝕊)?,              (* WP titles currently IN_PROGRESS *)
  pending_works: Seq(𝕊)?,             (* WP titles waiting to start *)
  recent_commits: Seq(𝕊)?,            (* last 5 git commits — session context *)
  divergence: Seq(𝕊)?                 (* L1 only: mismatches between local files and gem2-studio *)
]

(* === Precondition === *)
P ≜ project_slug ≠ ⊥
    ∧ alarm_path exists

(* === Transform === *)
F ≜ <<
  1. L0 — Read local state:
       Read alarm.md → parse STATUS counter line.
       Scan work-plan/*.md → extract STATUS from each WP file header.
       Classify: active_works (IN_PROGRESS), pending_works (PENDING).
       git log --oneline -5 → recent_commits (session context for human).
  2. L1 — Query gem2-studio MCP (IF layer = L1):
       gem2_task_search(project_slug, status=IN_PROGRESS) → remote active_works.
       gem2_task_search(project_slug, status=PENDING) → remote pending_works.
       gem2_session_context(project_slug) → recent messages, decisions.
  3. L1 — Detect divergence (IF layer = L1):
       Compare alarm.md counters vs gem2-studio task counts.
       Compare WP file STATUS vs gem2-studio task STATUS.
       IF mismatch → record in divergence list.
  4. Output B as human-readable summary:
       Always show: counters, active_works, pending_works, recent_commits.
       L1 additionally: divergence (if any), recent messages/decisions from gem2-studio.
>>

(* === Constraint === *)
CONSTRAINT ≜ [
  ⊢ Strictly read-only — NEVER modifies any file,
  ⊢ NEVER modifies gem2-studio state — read-only queries only,
  ⊢ NEVER auto-fixes divergence — report only,
  ⊢ NEVER starts or resumes work — status reporting only,
  ⊢ gem2-studio MCP unavailability is NOT an error — L0 output is complete and valid
]

(* === Invariant === *)
INV ≜ [
  ⊢ Zero side effects — filesystem and gem2-studio unchanged after execution,
  ⊢ Divergence is flagged, never silently resolved (L1 only),
  ⊢ Output is current snapshot — no caching between invocations,
  ⊢ L0 output is self-sufficient — human can make routing decisions from local state alone,
  ⊢ git log provides session context even without gem2-studio MCP
]

(* === Post-Execution Routing === *)
Routing ≜ [
  active_works ≠ ∅     → /proceed-work (resume interrupted work),
  pending_works ≠ ∅    → ask human which to proceed,
  divergence ≠ ∅       → flag to human, suggest /archive-work to reconcile,
  all_clear = ⊤        → /plan-work or ask human for new work
]