---
name: init-session
description: >
  (What) Bootstrap session — ensure 7 mandatory files exist, detect L0/L1 layer.
  (When) Every session start. Must be first skill triggered.
  (Why) Infrastructure gate. (How) Check files → create missing → /check-session.
metadata:
  author: David Seo of GEM².AI
  version: 15.0.0
allowed-tools:
  - Read
  - Write
  - Bash(mkdir *)
  - Bash(date *)
  - Bash(git *)
  - Bash(uuidgen *)
  - mcp__gem2-studio__gem2_knowledge
  - mcp__gem2-studio__gem2_skill
  - mcp__gem2-studio__gem2_session
  - mcp__gem2-studio__gem2_status
---

(* TPMN SKILL — init-session *)

(* === Layers === *)
L0 ≜ "git + .gem-squared/ files — REQUIRED, works without gem2-studio MCP"
L1 ≜ "gem2-studio MCP — OPTIONAL, enhances with semantic search, latest templates, session recovery"

(* === Input === *)
A ≜ [
  project_dir: Path,
  7_Mandatory_Files: [
    ".claude/skills/{slug}/SKILL.md"       — project skill,
    ".claude/TPMN-SKILL-STANDARD.md"       — skill authoring standard,
    ".gem-squared/alarm.md"                — mutable state,
    "CLAUDE.md"                            — behavioral rules,
    ".mcp.json"                            — MCP server config,
    ".gitignore"                           — git hygiene,
    ".gem-squared/work-plan/"              — directory
  ]
]

(* === Output === *)
B ≜ [
  project_slug: 𝕊,
  session_started_at: 𝕊,              (* ISO8601 timestamp *)
  mandatory_files: 𝕊,                 (* e.g., "All 7 present" or "Created: alarm.md, .gitignore" *)
  layer: {L0, L1}                      (* which layer is active this session *)
]

(* === Precondition === *)
P ≜ project_dir ≠ ⊥

(* === Transform === *)
F ≜ <<
  1. Detect available layer:
       Probe gem2-studio MCP: attempt gem2_status(project_slug) or gem_health().
       IF reachable → layer = L1.
       IF unreachable or not configured → layer = L0.
  2. Retrieve project_slug:
       L0: Read .claude/skills/*/SKILL.md → extract `name:` field from frontmatter.
           IF no SKILL.md exists → derive from directory name (basename of project_dir).
           Generate local session_id: `uuidgen | cut -c1-8`.
       L1 (additionally): gem2_session_context(project_slug) to confirm project exists in gem2-studio.
       IF project_slug not found by either method → STOP, report human to run /create-project.
  3. Ensure all 3 canonical directory trees exist (mkdir -p equivalent):
       Tree 1 — .gem-squared/ (15 dirs):
         work-plan, verify-work-logs, truth-logs, archive, evidences,
         gem2-core-skills/_preface,
         gem2-core-skills/{archive-work,check-session,end-session,init-session,
                          plan-work,proceed-work,verify-by-gem2,verify-work}
       Tree 2 — .claude/skills/ (5 dirs):
         agents, {slug}/references, {slug}/assets, {slug}/eval-viewer, {slug}/scripts
       Tree 3 — ~/.claude/skills/ (8 dirs):
         archive-work, check-session, end-session, init-session,
         plan-work, proceed-work, verify-by-gem2, verify-work
       Allowed tool: Bash(mkdir -p {path}) for each missing directory.
  4. FOR each file in 7_Mandatory_Files: check exists.
       IF missing → create with defaults:
         SKILL.md:
           L0: minimal project identity skeleton (bundled template).
           L1: fetch latest from gem2-studio: gem2_skill(action="search", user_id="gem2-studio", query="SKILL.md Template", skill_type="prompt").
         TPMN-SKILL-STANDARD.md:
           L0: bundled v3.0 fallback.
           L1: fetch from gem2-studio: gem2_skill(action="search", user_id="gem2-studio", query="TPMN-SKILL-STANDARD.md Template", skill_type="prompt").
         alarm.md:
           L0: empty counters (PENDING:0|IN_PROGRESS:0|COMPLETED:0|DECOMPOSED:0|ABORTED:0).
           L1: fetch from gem2-studio: gem2_skill(action="search", user_id="gem2-studio", query="alarm.md Template", skill_type="prompt").
         CLAUDE.md:
           L0: bundled template.
           L1: fetch from gem2-studio: gem2_skill(action="search", user_id="gem2-studio", query="CLAUDE.md Template", skill_type="prompt").
         .mcp.json:
           L0: minimal gem2-studio MCP config.
           L1: fetch from gem2-studio: gem2_skill(action="search", user_id="gem2-studio", query=".mcp.json Template", skill_type="prompt").
         .gitignore:
           L0: standard gem2 ignores.
           L1: fetch from gem2-studio: gem2_skill(action="search", user_id="gem2-studio", query=".gitignore Template", skill_type="prompt").
         work-plan/:
           L0: mkdir (already covered by step 3).
       IF exists AND updatable (CLAUDE.md, SKILL.md, TPMN-SKILL-STANDARD.md):
         L1: fetch latest from gem2-studio (same gem2_skill search as above).
             Extract version from local file (first 15 lines: "**Version:** vX.Y.Z" or "version: X.Y.Z").
             Extract version from fetched content.
             IF remote version > local version → overwrite with fetched content.
             Apply template vars: replace {{.Slug}} with project_slug, {{.Date}} with today.
         L0: no update (no remote source to compare against).
       IF exists AND NOT updatable (alarm.md, .mcp.json, .gitignore):
         NEVER overwrite — these contain mutable state or user customizations.
  5. Check 9 core skill SKILL.md versions (L1 only):
       FOR each core skill in {archive-work, check-session, end-session, init-session,
                                plan-work, proceed-work, update-work-plan, verify-by-gem2, verify-work}:
         L1: gem2_skill(action="search", user_id="gem2-studio", query="{name} SKILL.md", skill_type="prompt").
             (* NOTE: user_id="gem2-studio" is the MCP default — explicit is preferred for clarity. *)
             IF found → VALIDATE fetched content before any write:
               CONTENT VALIDATION (mandatory):
                 (a) Count lines in fetched content. IF < 30 lines → REJECT (frontmatter-only).
                 (b) Check for TPMN body markers: "(* ===" OR "CONSTRAINT" OR "F ≜".
                     IF none found → REJECT (truncated or malformed).
                 (c) IF REJECTED → log warning: "Skipping {name}: fetched content appears truncated
                     ({N} lines, no TPMN body markers). Keeping local file."
                     CONTINUE to next skill — do NOT overwrite.
               IF VALIDATED → extract version from fetched content.
             Check both locations:
               ~/.claude/skills/{name}/SKILL.md
               .gem-squared/gem2-core-skills/{name}/SKILL.md
             IF local file missing OR remote version > local version → overwrite with fetched content.
         L0: no remote source — skip version checking (local files used as-is).
  6. Record session_started_at timestamp in alarm.md ("Last checked" line).
  7. L1 (IF available): gem2_session_context(role="ARCHITECT", project_slug) → warm cache
       for /check-session. Store nothing — just pre-fetch for next skill.
>>

(* === Constraint === *)
CONSTRAINT ≜ [
  ⊢ project_slug must be resolved before step 3,
  ⊢ NEVER start work — infrastructure readiness only,
  ⊢ NEVER analyze session state — that is /check-session mandate,
  ⊢ NEVER modify alarm.md beyond initial creation + timestamp,
  ⊢ NEVER modify CLAUDE.md beyond version upgrade from gem2-studio,
  ⊢ NEVER skip file checks — all 7 checked every time,
  ⊢ gem2-studio MCP failure is NOT a blocking error — fall back to L0 silently
]

(* === Invariant === *)
INV ≜ [
  ⊢ All 3 canonical directory trees exist after this skill completes (28 dirs total),
  ⊢ All 7 mandatory files exist after this skill completes,
  ⊢ Updatable files (CLAUDE.md, SKILL.md, TPMN-SKILL-STANDARD.md) are at latest known version (L1) or unchanged (L0),
  ⊢ 9 core skill SKILL.md files are at latest known version (L1) or unchanged (L0) in both locations,
  ⊢ Fetched core skill content is NEVER written without TPMN body validation (≥30 lines + markers),
  ⊢ Non-updatable files (alarm.md, .mcp.json, .gitignore) are NEVER overwritten if they exist,
  ⊢ alarm.md records session_started_at timestamp,
  ⊢ Skill works identically whether gem2-studio MCP is available or not — L1 only enhances,
  ⊢ Layer detection happens ONCE at session start — no re-probing mid-session
]

(* === Post-Execution Routing === *)
Routing ≜ [
  project_slug = ⊥   → STOP, report: "run /create-project",
  files_created ≠ ∅  → report created files + active layer, then /check-session,
  all_present = ⊤    → /check-session
]