---
name: search-skill
description: >
  (What) Search .claude/skills/ and .gem-squared/external-skills/ for skills and skill directories relevant to the current task.
  (When) During /plan-work when domain capability is needed, or anytime human asks "what skills do we have?"
  (Why) Market-standard skills in .claude/skills/ contain domain knowledge. Archived skills in .gem-squared/external-skills/ remain discoverable. Discovery amplifies both.
  (How) Glob/Read on .claude/skills/*/ and .gem-squared/external-skills/*/ → parse SKILL.md if present, report directories without SKILL.md → return catalog.
argument-hint: "[search query — what capability are you looking for?]"
metadata:
  author: David Seo of GEM².AI
  version: 14.0.0
allowed-tools:
  - Read
  - Glob
  - Grep
  - Bash(date *)
---

(* TPMN SKILL — search-skill *)

(* === Grounding 5W === *)
Grounding_5W ≜ [
  who:   "Any role needing domain capabilities — typically AI Pilot during /plan-work",
  what:  "Search .claude/skills/ and .gem-squared/external-skills/ for installed and archived skills relevant to the current task",
  when:  "During /plan-work for domain capability discovery, or anytime human asks 'what skills do we have?'",
  where: "{project_root}/.claude/skills/*/ and {project_root}/.gem-squared/external-skills/*/",
  why:   "Market-standard skills encode domain knowledge (Figma, Sentry, deploy, etc.). Archived skills remain discoverable as CONTRACTs. TPMN orchestrates them; this skill discovers them"
]

(* === Layers === *)
L0 ≜ "Filesystem search — Glob/Read on .claude/skills/ and .gem-squared/external-skills/"
L1 ≜ "Same as L0 — both directories are always filesystem. No gem2-studio MCP needed"

(* === Input === *)
A ≜ [
  query: 𝕊?,                            (* ⊥ = list all installed skills. 𝕊 = search by keyword *)
  project_slug: 𝕊,
  include_tpmn: 𝔹?,                    (* ⊥ = true. Include TPMN-extracted skills in results *)
  layer: {L0, L1}                       (* inherited from /init-session — L0 and L1 behave identically *)
]

(* === Output === *)
B ≜ [
  project_slug: 𝕊,
  searched_at: 𝕊,                      (* ISO8601 *)
  query: 𝕊?,                           (* echoed back — ⊥ if catalog listing *)
  skills: Seq([
    skill_name: 𝕊,                     (* from YAML name field, or dirname if no SKILL.md *)
    skill_path: Path,                   (* e.g., ".claude/skills/figma-to-code/SKILL.md" or ".claude/skills/agents/" *)
    description: 𝕊,                    (* from YAML description field — first 200 chars. "Directory without SKILL.md" if none *)
    format: {TPMN, prose, directory, unknown},  (* directory = no SKILL.md present *)
    has_scripts: 𝔹,                    (* scripts/ subdirectory exists *)
    has_references: 𝔹,                 (* references/ subdirectory exists *)
    relevance: {exact, partial, none},  (* keyword match against query *)
    location: {active, archived}        (* active = .claude/skills/, archived = .gem-squared/external-skills/ *)
  ]),
  skill_count: ℕ,                       (* total items found (skills + directories) *)
  tpmn_count: ℕ,                        (* skills in TPMN format *)
  prose_count: ℕ,                       (* skills in prose/market-standard format *)
  directory_count: ℕ,                   (* directories without SKILL.md *)
  match_count: ℕ,                       (* items matching query — 0 if catalog listing *)
  archived_count: ℕ                     (* items in .gem-squared/external-skills/ *)
]

(* === Precondition === *)
P ≜ project_slug ≠ ⊥
    ∧ "{project_root}/.claude/skills/" exists

(* === Transform === *)
F ≜ <<
  1. Discover ALL items from active and archived locations:
       Scope 1 (active): Glob {project_root}/.claude/skills/*/ → all active directories.
       Scope 2 (archived): Glob {project_root}/.gem-squared/external-skills/*/ → all archived directories.
       (* Globs ALL subdirectories, not just SKILL.md holders — catches agents/, scripts-only dirs, etc. *)
       FOR each directory in merged list:
         IF SKILL.md exists in directory:
           Parse YAML frontmatter → extract name, description.
           Detect format:
             IF body contains "(* ===" OR "≜" OR "CONSTRAINT ≜" → format = TPMN.
             ELSE IF body contains markdown headers and prose → format = prose.
             ELSE → format = unknown.
         ELSE (no SKILL.md):
           skill_name = dirname.
           description = "Directory without SKILL.md".
           format = directory.
         Check subdirectories:
           has_scripts = scripts/ exists under dir.
           has_references = references/ exists under dir.
         Set location = active or archived based on scope.

  2. Filter by query (IF query ≠ ⊥):
       For each item:
         Match query keywords against: name, description, body content (first 500 chars if SKILL.md exists).
         Score relevance:
           exact:   query appears in name or first line of description,
           partial: query words found in description or body,
           none:    no match.
       IF include_tpmn = false → exclude format = TPMN from results.
       Sort: exact before partial. Within same relevance, alphabetical by name.

  3. IF query = ⊥ → catalog listing:
       Return all items (active + archived), sorted alphabetical.
       All relevance = none (not a search, just discovery).

  4. Compute counts:
       skill_count = total items found (active + archived).
       tpmn_count = count where format = TPMN.
       prose_count = count where format = prose.
       directory_count = count where format = directory.
       match_count = count where relevance ∈ {exact, partial}.
       archived_count = count where location = archived.

  5. Output B as structured report:
       Table format: name | location | format | relevance | description (truncated).
       Summary line: "{skill_count} items ({archived_count} archived, {tpmn_count} TPMN, {prose_count} prose, {directory_count} dirs), {match_count} matching query."
>>

(* === Constraint === *)
CONSTRAINT ≜ [
  ⊢ Strictly read-only — NEVER modifies any file in .claude/skills/ or .gem-squared/external-skills/,
  ⊢ NEVER converts or reformats skills — reads market-standard skills AS-IS,
  ⊢ NEVER judges prose vs TPMN — reports format, does not rank by format quality,
  ⊢ NEVER executes skills — discovery only,
  ⊢ Searches .claude/skills/ AND .gem-squared/external-skills/ — does NOT search .gem-squared/archive/ (that is /search-kg mandate),
  ⊢ Discovers ALL subdirectories — not just those with SKILL.md,
  ⊢ L0 and L1 behave identically — both directories are always filesystem
]

(* === Invariant === *)
INV ≜ [
  ⊢ B is state (skill catalog), not action — AI reads B and decides how to use discovered skills,
  ⊢ Zero side effects — .claude/skills/ and .gem-squared/external-skills/ unchanged after execution,
  ⊢ Format detection is heuristic, not authoritative — TPMN markers are sufficient but not exhaustive,
  ⊢ Both TPMN and prose skills are first-class results — no preference in ranking,
  ⊢ Directories without SKILL.md are reported as format=directory — discoverable but not parseable as skills,
  ⊢ This skill is the READ counterpart to /extract-skill's WRITE to .claude/skills/,
  ⊢ Dual scope: .claude/skills/ (active) + .gem-squared/external-skills/ (archived by /skill-to-kg),
  ⊢ Clear mandate boundary: /search-skill = skills discovery, /search-kg = .gem-squared/archive/ (proven WPs),
  ⊢ L0 and L1 behave identically — .claude/skills/ and .gem-squared/external-skills/ are always filesystem
]

(* === Post-Execution Routing === *)
Routing ≜ [
  match_count > 0  → /plan-work (domain skills found — can reference in unit-work decomposition),
  match_count = 0  → /plan-work (no domain skills — decompose with TPMN lifecycle only),
  human_browsing   → display catalog, await human decision
]
