(* TPMN SKILL — create-project *)
(* Proven pattern: WP-ST-125 units 1-2 (gem2-console), AI-agent-olympics retrofit *)

(* === Layers === *)
L0 ≜ "git + filesystem — fully local project bootstrap"

(* === Input === *)
A ≜ [
  project_slug: 𝕊,                    (* unique identifier, e.g., "gem2-console" — used in alarm.md, WP headers, gem2-studio MCP calls *)
  project_name: 𝕊,                    (* human-readable name, e.g., "GEM² Console" *)
  project_path: Path,                  (* absolute path to target directory *)
  time_stamp: 𝕊,                      (* ISO8601 — when this creation was triggered *)
  stack: {go, node, python, none},     (* language stack — determines module file *)
  license: {MIT, Apache-2.0, none}?,   (* ⊥ = MIT *)
  remote_url: 𝕊?,                     (* git remote origin URL — ⊥ = no remote *)
  module_path: 𝕊?,                    (* go.mod module / package.json name — ⊥ = derive from remote_url or project_name *)
  description: 𝕊?                     (* one-line project description — used in CLAUDE.md and alarm.md *)
]

(* === Output === *)
B ≜ [
  project_slug: 𝕊,
  project_path: Path,
  project_name: 𝕊,
  git_initialized: 𝔹,
  remote_configured: 𝔹,
  skills_installed: ℕ,                (* 12 core skills *)
  claude_md_version: 𝕊,              (* e.g., "v1.0.0" *)
  stack_file: 𝕊?,                    (* "go.mod" | "package.json" | "pyproject.toml" | ⊥ *)
  init_session_ready: 𝔹,             (* true iff /init-session can run at this path *)
  created_at: 𝕊                      (* ISO8601 *)
]

(* === Precondition === *)
P ≜ project_slug ≠ ⊥
    ∧ project_name ≠ ⊥
    ∧ project_path exists (directory)
    ∧ time_stamp ≠ ⊥
    ∧ stack ∈ {go, node, python, none}

(* === Transform === *)
F ≜ <<
  1. Scaffold project structure:
       Create directories (skip if exist):
         {project_path}/.gem-squared/
         {project_path}/.gem-squared/work-plan/
         {project_path}/.gem-squared/archive/
         {project_path}/.gem-squared/verify-work-logs/
         {project_path}/.gem-squared/reference/
         {project_path}/.claude/skills/

       IF stack = go:
         go mod init {module_path} (if go.mod missing).
       IF stack = node:
         Write package.json with name={module_path}, version="0.1.0" (if missing).
       IF stack = python:
         Write pyproject.toml with project.name={module_path} (if missing).

       IF license ≠ none:
         Write LICENSE file ({license} template, copyright GEM².AI {year}).

       Write .gitignore:
         Stack-specific ignores (Go: *.exe, vendor/; Node: node_modules/; Python: __pycache__, .venv/).
         Always: .env, .env.local, .DS_Store, .gem-squared/, .claude/.
         (* .gem-squared/ and .claude/ are dev env — not committed to submission repos *)

       IF git not initialized:
         git init.

       IF remote_url ≠ ⊥ ∧ no origin configured:
         git remote add origin {remote_url}.

  2. Install 12 core TPMN skills:
       Source: ~/.claude/skills/ (global installation).
       Target: {project_path}/.claude/skills/.
       Skills: archive-work, check-session, end-session, extract-skill,
               init-session, plan-work, proceed-work, search-kg,
               search-skill, skill-to-kg, update-work-plan, verify-work.
       Copy each skill directory (cp -R). Skip if already exists at target.
       Record skills_installed count.

  3. Write CLAUDE.md (project-specific):
       Template structure:
         # CLAUDE.md — {project_name}
         **Version:** v1.0.0 | **Updated:** {time_stamp}

         ---
         ## CANNONICAL DIRECTIVE, YOU MUST OBEY ##
         **No ad-hoc execution. NEVER jump into implementation without triggering the matching skill first.**

         ---

         ## Project Identity
         **project_slug:** {project_slug}
         {project_name} — {description}.
         **Module:** {module_path}
         **Stack:** {stack}

         ## Git Commit Convention
         (standard gem2 format with Date + Author)

         ## Session Protocol
         Trigger /init-session on every session start.

         ## Skill Trigger Pattern
         **No ad-hoc execution. NEVER jump into implementation without triggering the matching skill first.**
         (full 12-skill table)

         ## Mandatory Execution Rule
         ALL implementation work MUST flow through /proceed-work.

         ## Project Context
         .gem-squared/alarm.md, work-plan/, archive/, reference/

         ## Core Invariants
         - L0 mode (git + .gem-squared/)
         - Every work cycle produces a git commit

       IF CLAUDE.md already exists:
         Ask human: overwrite or skip.
         IF skip → preserve existing CLAUDE.md.

  4. Write alarm.md (initial state):
       IF .gem-squared/alarm.md missing:
         # {project_name} — Session Alarm
         **Project:** {project_name} | **project_slug:** {project_slug}
         **Updated:** {time_stamp}

         ## Status Counters
         PENDING: 0 | IN_PROGRESS: 0 | COMPLETED: 0 | DECOMPOSED: 0 | ABORTED: 0

         ## Active Tasks
         | WP | Date | Title | State |
         | — | — | — | — |

         ## Recently Completed
         | WP | Date | Title | State |

         ## Notes
         - L0 mode — git + .gem-squared/ files
       IF exists → skip (preserve existing session state).

  5. Copy TPMN references:
       Source: ~/.claude/skills/ parent or nearest gem2 project with references.
       Target: {project_path}/.gem-squared/reference/.
       Files: TPMN-LIFECYCLE-GUIDE.md, TPMN-SKILL-STANDARD.md.
       Skip if already exist.

  6. Verify /init-session readiness:
       Check: alarm.md exists ∧ work-plan/ exists ∧ ≥ 12 skills in .claude/skills/.
       Set init_session_ready = ⊤ iff all checks pass.
       Record created_at. Output B.
>>

(* === Constraint === *)
CONSTRAINT ≜ [
  ⊢ NEVER overwrite existing files without asking — skip or ask human,
  ⊢ NEVER delete anything — additive only,
  ⊢ NEVER execute project work — this creates the environment, not the deliverables,
  ⊢ NEVER create WPs — that is /plan-work mandate,
  ⊢ NEVER modify existing alarm.md — preserve session state from prior sessions,
  ⊢ NEVER touch ~/.claude/skills/ (global) — read-only source for copying,
  ⊢ .gem-squared/ and .claude/ are ALWAYS gitignored — dev env stays local,
  ⊢ Stack detection is explicit (input parameter) — never infer from existing files
]

(* === Invariant === *)
INV ≜ [
  ⊢ After execution: /init-session can run at {project_path} without errors,
  ⊢ After execution: .claude/skills/ contains exactly 12 core skills (+ any pre-existing project skills),
  ⊢ After execution: CLAUDE.md contains full skill trigger table and mandatory execution rule,
  ⊢ After execution: .gem-squared/ has alarm.md, work-plan/, archive/, reference/,
  ⊢ Existing project files are never overwritten without human permission,
  ⊢ Existing alarm.md is never modified — it may contain active session state,
  ⊢ git init is idempotent — safe to run on already-initialized repos,
  ⊢ This skill is idempotent — running twice on the same project produces the same result,
  ⊢ MANDATE BOUNDARY: environment creation only — between nothing and /init-session
]

(* === Pre-Execution Dialog === *)
Ask_Human ≜ <<
  [field: "project_name", prompt: "Project name?", required: ⊤],
  [field: "project_path", prompt: "Absolute path to project directory?", required: ⊤],
  [field: "stack", prompt: "Language stack? (go/node/python/none)", required: ⊤]
>>

(* === Post-Execution Routing === *)
Routing ≜ [
  init_session_ready = ⊤  → /init-session (project is bootable),
  init_session_ready = ⊥  → report missing components to human
]
