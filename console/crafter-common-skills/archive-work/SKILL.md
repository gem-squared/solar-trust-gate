---
name: archive-work
description: >
  (What) Finalize WP — write STATUS/STATE to header, move to archive/, git commit.
  (When) After /verify-work SUCCESS, or anytime human explicitly demands.
  (Why) Atomic lifecycle closure. (How) Derive state → move file → commit → alarm → broadcast.
argument-hint: "[WP path or work title]"
metadata:
  author: David Seo of GEM².AI
  version: 11.0.0-draft
allowed-tools:
  - Read
  - Edit
  - Bash(git *)
  - Bash(mv *)
  - Bash(mkdir *)
  - Bash(date *)
  - mcp__gem2-studio__gem2_task
  - mcp__gem2-studio__gem2_msg
  - mcp__gem2-studio__gem2_knowledge
---

(* TPMN SKILL — archive-work *)

(* === Layers === *)
L0 ≜ "WP file + git + alarm.md — fully local archiving, git commit is the handoff"
L1 ≜ "gem2-studio MCP — additionally syncs task status, stores proven contracts, broadcasts"

(* === Input === *)
A ≜ [
  project_slug: 𝕊,
  wp_path: Path?,                      (* direct path to WP file, or ⊥ *)
  work_title: 𝕊?,                     (* search term if wp_path not given *)
  force: 𝔹?,                          (* ⊥ = false. true = case 2, human explicitly demands *)
  layer: {L0, L1}                      (* inherited from /init-session *)
]

(* === Output === *)
B ≜ [
  project_slug: 𝕊,
  wp_path: Path,                       (* original path *)
  archive_path: Path,                  (* new path in .gem-squared/archive/ *)
  wp_id: 𝕊,
  wp_status: {COMPLETED, ABORTED},    (* WP-level STATUS — written to header *)
  wp_state: {SUCCESS, FAILURE, —},    (* WP-level STATE — written to header *)
  trigger: {CASE_1, CASE_2},          (* which trigger path was used *)
  git_commit: 𝕊,                     (* commit hash *)
  archived_at: 𝕊                     (* ISO8601 *)
]

(* === Precondition === *)
P ≜ project_slug ≠ ⊥
    ∧ (wp_path ≠ ⊥ ∨ work_title ≠ ⊥)
    ∧ ".gem-squared/alarm.md" exists
    ∧ (force = ⊤                                          (* case 2: no precondition on unit status *)
       ∨ all unit STATUS ∈ {COMPLETED, ABORTED})          (* case 1: all units terminal *)

(* === Trigger Cases === *)
CASE_1 ≜ [
  condition: wp.STATUS = COMPLETED ∧ wp.STATE = SUCCESS,
  source: "/verify-work routing (recommended path)",
  human_permission: required (confirm only)
]

CASE_2 ≜ [
  condition: human explicitly demands archive (any STATUS, any STATE),
  source: "human override — priorities shifted, work abandoned, or FAILURE accepted",
  human_permission: required (confirm + acknowledge non-SUCCESS state)
]

(* === Transform === *)
F ≜ <<
  1. Record archived_at timestamp.
     Identify WP:
       IF wp_path provided → read that WP.
       IF only work_title → search work-plan/ for matching WP.
     Determine trigger case:
       IF all units COMPLETED/ABORTED AND all State = SUCCESS → CASE_1.
       ELSE → CASE_2 (requires force=⊤ or explicit human demand).
  2. IF CASE_2 — mark unfinished units:
       FOR each unit with STATUS ∈ {PENDING, IN_PROGRESS}:
         Mark STATUS → ABORTED in WP file.
         Write `- Result: Aborted by human override (archived before completion).`
         Write `- State:` (leave empty — never verified).
  3. Derive WP-level STATUS and STATE from per-unit fields:
       WP STATUS:
         IF all units STATUS = COMPLETED → wp_status = COMPLETED.
         IF any unit STATUS = ABORTED → wp_status = ABORTED.
       WP STATE:
         IF no per-unit State fields filled → wp_state = — (verification was not run).
         IF all filled State = SUCCESS → wp_state = SUCCESS.
         IF any filled State = FAILURE → wp_state = FAILURE.
     Write to WP header:
       `**STATUS:** {wp_status} | **STATE:** {wp_state} | **task_id:** {task_id}`
  4. Ask human permission:
       CASE_1: Show: "Archive COMPLETED|SUCCESS WP: {title}? (move to archive/)"
       CASE_2: Show: "Force-archive WP: {title}? STATUS={wp_status}, STATE={wp_state}.
               {N} units aborted. This is irreversible — confirm?"
       IF denied → STOP without archiving.
  5. Move WP file to archive:
       mkdir -p .gem-squared/archive/
       mv .gem-squared/work-plan/{WP-file}.md → .gem-squared/archive/{WP-file}.md
       Record archive_path.
  6. Git commit:
       git add .gem-squared/archive/{WP-file}.md .gem-squared/work-plan/ .gem-squared/alarm.md
       git commit with message:
         "Archive {wp_status}|{wp_state}: {WP title}

         Trigger: {CASE_1 or CASE_2}
         STATUS: {wp_status} | STATE: {wp_state}
         Archived: .gem-squared/archive/{WP-file}.md

         Date: {date}
         Author: David Seo of GEM².AI"
       Capture git_commit hash.
  7. Update alarm.md:
       Decrement IN_PROGRESS (or PENDING) counter.
       IF wp_status = COMPLETED → increment COMPLETED counter.
       IF wp_status = ABORTED → increment ABORTED counter.
       Move from Active Tasks to Recently COMPLETED table:
         | {wp_id} | {date} | {title} | {wp_state} |
       Update timestamp.
  8. IF layer = L1 — sync to gem2-studio:
       gem2_task_update(task_id,
         status = COMPLETED or CANCELLED,
         result_summary = "{wp_state}: {summary of unit results}").
       IF wp_state = SUCCESS:
         gem2_knowledge_create(entity_type='contract',
           title='Proven: {WP title}',
           content = CONTRACTs + results summary,
           project_slug, tags=['proven']).
       gem2_msg_create(
         from_role='ARCHITECT', to_role='BROADCAST',
         project_slug,
         message='Archived {wp_status}|{wp_state}: {WP title} [{CASE_1/CASE_2}]').
     Output B.
>>

(* === Constraint === *)
CONSTRAINT ≜ [
  ⊢ NEVER execute work — that is /proceed-work mandate,
  ⊢ NEVER verify — that is /verify-work and /verify-by-gem2 mandate,
  ⊢ NEVER plan — that is /plan-work mandate,
  ⊢ NEVER modify work output or per-unit Result fields — archive what exists as-is,
  ⊢ NEVER archive without human permission — always ask first,
  ⊢ CASE_2 ONLY when human explicitly demands — never auto-triggered by routing,
  ⊢ Git commit happens BEFORE alarm.md and gem2-studio updates — code is committed first,
  ⊢ File move is atomic: WP exists in EITHER work-plan/ OR archive/, never both,
  ⊢ gem2-studio sync failure does NOT block archiving — git commit + alarm.md is the baseline
]

(* === Invariant === *)
INV ≜ [
  ⊢ This is the ONLY skill that writes WP-level STATUS and STATE to the WP header,
  ⊢ This is the ONLY skill that moves WPs from work-plan/ to archive/,
  ⊢ WP-level STATUS derived from per-unit STATUS — not independently assigned,
  ⊢ WP-level STATE derived from per-unit State — not independently assigned,
  ⊢ wp_state = — is valid (verification was skipped),
  ⊢ CASE_2 marks all unfinished units ABORTED before deriving WP-level fields,
  ⊢ After archiving: work-plan/ contains only active/pending WPs,
  ⊢ After archiving: archive/ contains only terminal WPs (no further modification),
  ⊢ All side effects in one skill: WP header, file move, git, alarm.md, gem2-studio task, gem2-studio knowledge, broadcast,
  ⊢ Proven contracts (SUCCESS) stored as knowledge — feeds future /plan-work pattern retrieval,
  ⊢ Same archived_at timestamp on all writes — enables recency comparison,
  ⊢ L0 baseline (steps 1-7): WP file + git + alarm.md — fully functional without gem2-studio MCP,
  ⊢ L1 enhancement (step 8): gem2-studio task update + proven contract store + broadcast — best-effort
]

(* === Pre-Execution Dialog === *)
Ask_Human ≜ <<
  [field: "confirm",
   prompt: "Archive WP? (shows trigger case, STATUS, STATE, unit summary)",
   required: ⊤]
>>

(* === Post-Execution Routing === *)
Routing ≜ [
  archived = ⊤  → /check-session (cycle complete, check what's next),
  archived = ⊥  → report failure to human
]