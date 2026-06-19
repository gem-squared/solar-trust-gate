---
name: end-session
description: >
  (What) Commit session state for next-session recovery.
  (When) Before ending session, or "done for now" / "save session".
  (Why) Git commit = primary handoff. (How) Derive summary → update alarm.md → git commit.
argument-hint: "(no arguments — reads current state automatically)"
metadata:
  author: David Seo of GEM².AI
  version: 2.0.0-draft
allowed-tools:
  - Read
  - Edit
  - Bash(git *)
  - Bash(date *)
  - mcp__gem2-studio__gem2_msg
  - mcp__gem2-studio__gem2_task
  - mcp__gem2-studio__gem2_status
---

(* TPMN SKILL — end-session *)

(* === Input === *)
A ≜ [
  project_slug: 𝕊,
  alarm_path: ".gem-squared/alarm.md",
  work_plan_dir: ".gem-squared/work-plan/"
]

(* === Output === *)
B ≜ [
  project_slug: 𝕊,
  ended_at: 𝕊,                        (* ISO8601 — session end timestamp *)
  git_commit: 𝕊?,                     (* commit hash, ⊥ if working tree was clean *)
  msg_id: 𝕊?,                         (* gem2-studio message ID, ⊥ if gem2-studio unavailable *)
  summary: [
    accomplished: Seq(𝕊),             (* what was done this session *)
    active_wps: Seq(𝕊)?,             (* WPs still IN_PROGRESS *)
    pending_decisions: Seq(𝕊)?,      (* decisions needed before next action *)
    next_actions: Seq(𝕊)             (* recommended first steps for next session *)
  ]
]

(* === Precondition === *)
P ≜ project_slug ≠ ⊥
    ∧ alarm_path exists

(* === Transform === *)
F ≜ <<
  1. Record ended_at timestamp.
     Read alarm.md → extract current counters, active WPs.
     Scan work-plan/*.md → identify any IN_PROGRESS or recently modified WPs.
     Check git status → determine if working tree has uncommitted changes.
  2. Derive session summary:
       accomplished: what tasks were completed or progressed this session
         (from git log today + local WP Result fields written today).
       active_wps: WPs still IN_PROGRESS (from alarm.md).
       pending_decisions: any routing decisions left unresolved
         (e.g., "unit 6 FAILURE — implement, amend, or accept?").
       next_actions: recommended first steps for next session
         (derived from routing state of active WPs).
  3. Update alarm.md:
       Update "Last checked" timestamp.
       Update footer timestamp line with session summary.
       Do NOT modify counters or task lists — that is /archive-work mandate.
  4. Git commit (PRIMARY HANDOFF):
       IF working tree is dirty (including alarm.md update from step 3):
         git add .gem-squared/alarm.md (+ any other uncommitted session changes).
         git commit with message:
           "Session end: {1-2 sentence summary}

           Accomplished: {bullet list}
           Active WPs: {list or 'none'}
           Pending decisions: {list or 'none'}
           Next actions: {list}

           Date: {date}
           Author: David Seo of GEM².AI"
         Record git_commit hash.
       IF working tree is clean:
         git_commit = ⊥ (nothing to commit).
  5. gem2-studio broadcast (OPTIONAL — IF AVAILABLE):
       IF gem2-studio MCP tools are reachable:
         gem2_msg_create(
           from_role='ARCHITECT', to_role='BROADCAST',
           project_slug,
           message='Session end {ended_at}. {1-2 sentence summary}.',
           content=structured handoff (accomplished, active, pending, next)
         ).
         Record msg_id.
       ELSE:
         msg_id = ⊥. Skip silently — git commit is sufficient.
  6. Output B.
>>

(* === Constraint === *)
CONSTRAINT ≜ [
  ⊢ NEVER modify alarm.md counters or task lists — only timestamps and footer,
  ⊢ NEVER close or archive WPs — only report their current state,
  ⊢ NEVER move WP files — that is /archive-work mandate,
  ⊢ NEVER execute work — session is ending, not starting,
  ⊢ NEVER create gem2-studio tasks — only messages (read-only on task state),
  ⊢ Git commit is MANDATORY if working tree is dirty — primary state persistence,
  ⊢ gem2-studio MCP is OPTIONAL — graceful skip if unavailable,
  ⊢ Commit message must be self-contained — next session reads git log to recover
]

(* === Invariant === *)
INV ≜ [
  ⊢ After /end-session: git working tree is CLEAN (all changes committed),
  ⊢ Git commit message contains full handoff context (self-contained recovery),
  ⊢ alarm.md timestamp reflects session end time,
  ⊢ Symmetric with /init-session: init reads what end committed,
  ⊢ Works WITHOUT gem2-studio MCP — git is the required baseline, gem2-studio is enhancement,
  ⊢ Next session recovery path: git log → alarm.md → ready (no gem2-studio MCP needed)
]

(* === Post-Execution Routing === *)
Routing ≜ [
  session_ended = ⊤  → STOP (session is over, no further skills)
]