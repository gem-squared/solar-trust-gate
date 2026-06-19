---
name: skill-to-kg
description: >
  (Who) Claude Code or any user.
  (What) Sweep all non-core skills from {project_root}/.claude/skills/ to .gem-squared/external-skills/.
  (When) 1. At init-session (automatically), 2. After user creates/extracts a project-specific skill, 3. When user wants to restore a specific skill.
  (Where) {project_root}/.claude/skills/ → {project_root}/.gem-squared/external-skills/ (sweep). Reverse for restore.
  (Why) In TPMN mode, only the 12 core lifecycle skills and the project identity skill belong in .claude/skills/. Other skills become CONTRACTs — searchable via /search-kg, not active triggers.
argument-hint: "[sweep (default)|restore <skill-name>|list]"
metadata:
  author: David Seo of GEM².AI
  version: 2.1.0
allowed-tools:
  - Read
  - Glob
  - Bash(mv *)
  - Bash(mkdir *)
  - Bash(ls *)
  - Bash(date *)
  - mcp__gem2-studio__gem2_skill
  - mcp__gem2-studio__gem2_knowledge
---

(* TPMN SKILL — skill-to-kg *)

(* === Layers === *)
DEFAULT ≜ "gem2-studio MCP — registers sweep/restore operations in gem2-kg for cross-project visibility"
FALLBACK ≜ "git + .gem-squared/ files — filesystem move only, no remote registration"

(* === Protected — never swept === *)
CORE_SKILLS ≜ {
  "archive-work", "check-session", "end-session", "extract-skill",
  "init-session", "plan-work", "proceed-work", "search-kg",
  "search-skill", "skill-to-kg", "update-work-plan", "verify-work"
}  (* 12 official TPMN lifecycle skills *)

PROJECT_SKILL ≜ {project_slug}
(* The project identity skill bound by CLAUDE.md — always protected alongside CORE_SKILLS *)

PROTECTED ≜ CORE_SKILLS ∪ {PROJECT_SKILL}

(* === Input === *)
A ≜ [
  operation: {sweep, restore, list}?,  (* ⊥ = sweep (default). sweep = batch move ALL non-core out *)
  skill_name: 𝕊?,                     (* required for restore only. ⊥ for sweep/list *)
  project_slug: 𝕊
]

(* === Output === *)
B ≜ [
  project_slug: 𝕊,
  operation: {sweep, restore, list},
  swept_skills: Seq(𝕊)?,              (* names of skills/dirs moved out — sweep only *)
  swept_count: ℕ?,                     (* |swept_skills| — sweep only. 0 = nothing to sweep *)
  restored_skill: 𝕊?,                 (* name restored — restore only *)
  source_path: Path?,                  (* restore: moved FROM *)
  dest_path: Path?,                    (* restore: moved TO *)
  archived_skills: Seq([               (* list: contents of external-skills/ *)
    name: 𝕊,
    path: Path,
    has_skill_md: 𝔹
  ])?,
  active_non_core: Seq(𝕊)?,           (* list: non-core items still in .claude/skills/ *)
  kg_registered: 𝔹?,                  (* DEFAULT only: whether gem2-kg was updated *)
  completed_at: 𝕊                     (* ISO8601 *)
]

(* === Precondition === *)
P ≜ project_slug ≠ ⊥
    ∧ (operation = restore ⟹ skill_name ≠ ⊥)
    ∧ (operation = restore ⟹ ".gem-squared/external-skills/{skill_name}/" exists)

(* === Transform === *)
F ≜ <<
  0. Resolve default operation:
       IF operation = ⊥ → operation ≜ sweep.
       (* No args = sweep. User must explicitly say "restore <name>" or "list". *)

  1. Ensure archive directory exists:
       mkdir -p {project_root}/.gem-squared/external-skills/

  2. Execute operation:
       IF operation = sweep:
         Glob {project_root}/.claude/skills/*/ → all_dirs.
         (* Globs ALL subdirectories, not just SKILL.md holders — catches agents/, etc. *)
         swept_skills ≜ []
         FOR each dir in all_dirs:
           IF dir.name ∈ PROTECTED → skip (core skill or project identity skill).
           IF .gem-squared/external-skills/{dir.name}/ exists → skip, log conflict.
           ELSE → mv .claude/skills/{dir.name}/ → .gem-squared/external-skills/{dir.name}/
           Append dir.name to swept_skills.
         swept_count ≜ |swept_skills|.
         DEFAULT: Register batch in gem2-kg:
           FOR each name in swept_skills:
             gem2_knowledge(action="create", project_slug, type="skill-archive",
               content="Archived skill: {name}", tags=["skill-archive", name])
           Set kg_registered = ⊤.
         FALLBACK: skip registration. kg_registered = ⊥.

       IF operation = restore:
         Verify .gem-squared/external-skills/{skill_name}/ exists.
         IF .claude/skills/{skill_name}/ already exists → STOP, report conflict.
         mv .gem-squared/external-skills/{skill_name}/ → .claude/skills/{skill_name}/
         Record restored_skill, source_path, dest_path.
         DEFAULT: Register in gem2-kg:
           gem2_knowledge(action="create", project_slug, type="skill-restore",
             content="Restored skill: {skill_name}", tags=["skill-restore", skill_name])
           Set kg_registered = ⊤.
         FALLBACK: skip registration. kg_registered = ⊥.

       IF operation = list:
         Glob .gem-squared/external-skills/*/ → archived_skills (check has_skill_md per dir).
         Glob .claude/skills/*/ → all_active.
         active_non_core ≜ [d.name | d ∈ all_active, d.name ∉ PROTECTED].

  3. Record completed_at timestamp. Output B.
>>

(* === Constraint === *)
CONSTRAINT ≜ [
  ⊢ NEVER move PROTECTED (CORE_SKILLS ∪ {PROJECT_SKILL}) — permanently active,
  ⊢ NEVER delete skills — move only (bidirectional, reversible),
  ⊢ NEVER modify skill content — move directory as-is,
  ⊢ NEVER overwrite existing directory at destination — skip with conflict log,
  ⊢ NEVER touch ~/.claude/skills/ (global) — project-local {project_root}/.claude/skills/ only,
  ⊢ sweep is BATCH — all non-protected directories moved in one invocation, not one by one,
  ⊢ Default operation is sweep — no args means sweep, not list or restore,
  ⊢ gem2-kg registration failure is NOT blocking — move succeeds even if registration fails
]

(* === Invariant === *)
INV ≜ [
  ⊢ After sweep: {project_root}/.claude/skills/ contains ONLY PROTECTED items — zero non-protected,
  ⊢ A skill/dir exists in EITHER .claude/skills/ OR .gem-squared/external-skills/, never both,
  ⊢ Archived items retain full directory structure (SKILL.md, references/, scripts/, etc.),
  ⊢ /search-skill finds both active AND archived skills (with location field),
  ⊢ Archived skills are CONTRACTs — searchable via /search-kg, not active triggers,
  ⊢ CORE_SKILLS (12) are never archivable — hard-coded protection,
  ⊢ PROJECT_SKILL ({project_slug}) is never archivable — project identity protection,
  ⊢ Non-SKILL.md directories (e.g., agents/) are swept like any other non-protected item,
  ⊢ DEFAULT: gem2-kg has a record of sweep/restore operations for audit trail,
  ⊢ FALLBACK: filesystem is the sole record — fully functional without gem2-kg,
  ⊢ MANDATE BOUNDARY: skill location management only — never skill content modification
]

(* === Post-Execution Routing === *)
Routing ≜ [
  operation = sweep   → report: "{swept_count} non-protected items archived. PROTECTED ({|PROTECTED|}) remain.",
  operation = restore → report: "{skill_name} restored to .claude/skills/.",
  operation = list    → display: protected | non-protected active | archived
]
