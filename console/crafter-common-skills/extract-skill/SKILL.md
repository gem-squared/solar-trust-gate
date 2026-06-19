---
name: extract-skill
description: >
  (What) Format a SUCCESS WP or unit-contract into a TPMN SKILL.md and store in .claude/skills/.
  (When) After /archive-work SUCCESS, or anytime human says "extract this as a skill" or "make this reusable."
  (Why) Proven CONTRACTs should be installable. This bridges TPMN archive to the .claude/skills/ ecosystem.
  (How) Read proven WP/UC → format as TPMN SKILL.md per TPMN-SKILL-STANDARD → write to .claude/skills/{name}/ → register.
argument-hint: "[WP-ID or unit reference — which proven contract to extract]"
metadata:
  author: David Seo of GEM².AI
  version: 13.0.0
allowed-tools:
  - Read
  - Write
  - Glob
  - Grep
  - Bash(mkdir *)
  - Bash(date *)
  - mcp__gem2-studio__gem2_skill
---

(* TPMN SKILL — extract-skill *)

(* === Grounding 5W === *)
Grounding_5W ≜ [
  who:   "Human or AI Pilot requesting skill extraction from proven archive",
  what:  "Format a SUCCESS WP or unit-contract into a TPMN SKILL.md file",
  when:  "After /archive-work SUCCESS, or explicit human request to make a proven pattern reusable as a skill",
  where: "Source: .gem-squared/archive/{WP}.md → Target: {project_root}/.claude/skills/{skill-name}/SKILL.md",
  why:   "Proven CONTRACTs are knowledge. Placing them in .claude/skills/ makes them discoverable by /search-skill and loadable by any AI agent that reads the skills directory"
]

(* === Layers === *)
DEFAULT ≜ "gem2-studio MCP — register extracted skill in gem2-kg after filesystem write"
FALLBACK ≜ "Filesystem only — read archive, write .claude/skills/, when MCP unreachable"

(* === Input === *)
A ≜ [
  source_wp: 𝕊,                        (* WP-ID, e.g., "WP-ST-69" *)
  source_unit: ℕ?,                      (* ⊥ = extract entire WP as skill. ℕ = specific unit only *)
  skill_name: 𝕊?,                      (* ⊥ = derive from WP title in kebab-case *)
  project_slug: 𝕊,
  layer: {DEFAULT, FALLBACK}            (* inherited from /init-session *)
]

(* === Output === *)
B ≜ [
  project_slug: 𝕊,
  source_wp: 𝕊,                        (* echoed *)
  source_unit: ℕ?,                      (* echoed *)
  skill_name: 𝕊,                       (* final kebab-case name *)
  skill_path: Path,                     (* e.g., ".claude/skills/deploy-staging/SKILL.md" *)
  extracted_at: 𝕊,                     (* ISO8601 *)
  contract_count: ℕ,                    (* number of unit-contracts included *)
  source_state: {SUCCESS},              (* must be SUCCESS — precondition *)
  format: {TPMN}                        (* always TPMN — this skill does NOT produce prose *)
]

(* === Precondition === *)
P ≜ source_wp ≠ ⊥
    ∧ project_slug ≠ ⊥
    ∧ ".gem-squared/archive/" exists
    ∧ source WP file exists in archive/
    ∧ source WP STATE = SUCCESS
    ∧ (source_unit = ⊥ ∨ source_unit ≤ WP.unit_count)

(* === Transform === *)
F ≜ <<
  1. Read and validate source via /search-kg:
       Invoke /search-kg(query=source_wp, project_slug, scope=archive, limit=1).
         Locate source WP in .gem-squared/archive/.
       Read .gem-squared/archive/{source_wp}.md.
       Parse header: confirm STATUS ∈ {COMPLETED, ABORTED} ∧ STATE = SUCCESS.
       IF source_unit ≠ ⊥ → extract that single unit-contract.
       IF source_unit = ⊥ → extract all unit-contracts.
       For each unit-contract, extract: title, A, B, P, Clarity, Tags, Result, State.
       IF any extracted unit State ≠ SUCCESS → warn human, skip that unit.

  2. Check for existing skill via /search-skill:
       Invoke /search-skill(query=skill_name, project_slug).
         IF match found with relevance = exact → existing skill detected.
           Warn human: "Skill '{skill_name}' already exists at {path}. Upsert?"
           IF human denies → STOP, output B with skill_path = existing path.
           IF human confirms → proceed to overwrite (upsert).
         IF no match → proceed to create new skill.

  3. Derive skill name:
       IF skill_name provided → use as-is (must be kebab-case).
       IF skill_name = ⊥ → derive from WP title:
         Strip "WP-ST-{N}: " prefix.
         Convert to kebab-case: lowercase, spaces→hyphens, strip special chars.
         e.g., "Deploy v2.3 to Staging" → "deploy-v23-to-staging".

  4. Format as TPMN SKILL.md:
       Follow .claude/TPMN-SKILL-STANDARD.md for structure.
       Generate YAML frontmatter:
         name: {skill_name}
         description: >
           (What) {WP objective — one line}
           (When) {derived from WP context — when this pattern applies}
           (Why) {extracted from WP — why this workflow exists}
           (How) {summary of unit-contract sequence}
         metadata:
           author: David Seo of GEM².AI
           version: 1.0.0
           extracted_from: {source_wp}
           extracted_at: {ISO8601}

       Generate TPMN body:
         (* TPMN SKILL — {skill_name} *)
         (* Extracted from {source_wp} — proven SUCCESS *)

         (* === Grounding 5W === *)
         Grounding_5W ≜ [ ... derived from WP context ... ]

         (* === Input === *)
         A ≜ [ ... generalized from unit-contract A fields ... ]

         (* === Output === *)
         B ≜ [ ... generalized from unit-contract B fields ... ]

         (* === Precondition === *)
         P ≜ ... generalized from unit-contract P fields ...

         (* === Transform === *)
         F ≜ <<
           ... unit-contract sequence from WP ...
           Each step corresponds to one unit-contract.
           Result fields included as reference (what was produced when this pattern was proven).
         >>

         (* === Constraint === *)
         CONSTRAINT ≜ [ ... derived from WP constraints and lessons ... ]

         (* === Provenance === *)
         Provenance ≜ [
           source: "{source_wp}",
           state: "SUCCESS",
           proven_at: "{WP archived_at}",
           unit_contracts: {contract_count}
         ]

  5. Write to .claude/skills/ (or upsert if existing):
       mkdir -p {project_root}/.claude/skills/{skill_name}/
       Write SKILL.md to {project_root}/.claude/skills/{skill_name}/SKILL.md.
       Record skill_path.

  6. Register in gem2-kg (DEFAULT path):
       gem2_skill(action=upsert, title="{skill_name}",
         content=SKILL.md contents, skill_type="contract",
         tags=["extracted", "proven", source_wp]).
       FALLBACK (gem2-studio MCP unavailable): skip — filesystem skill is the baseline.

  7. Output B.
>>

(* === Constraint === *)
CONSTRAINT ≜ [
  ⊢ ONLY extracts from SUCCESS WPs — never from FAILURE, PENDING, or IN_PROGRESS,
  ⊢ NEVER modifies the source archive file — read-only on archive/,
  ⊢ ALWAYS produces TPMN format — never prose, never market-standard format,
  ⊢ NEVER executes the extracted skill — extraction and formatting only,
  ⊢ NEVER overwrites an existing skill without human permission,
  ⊢ Generalize where possible — remove project-specific paths/names from A, B, P,
  ⊢ Preserve provenance — source WP, proven date, and STATE always recorded,
  ⊢ gem2-studio MCP registration is DEFAULT — FALLBACK skips (filesystem skill is sufficient)
]

(* === Invariant === *)
INV ≜ [
  ⊢ B is state (extraction result), not action — skill produces B and STOPS,
  ⊢ Every extracted skill has a Provenance section — traceable to source WP,
  ⊢ Every extracted skill follows TPMN-SKILL-STANDARD.md structure — not ad-hoc,
  ⊢ Target directory is always .claude/skills/ — the standard skills location,
  ⊢ TPMN format in .claude/skills/ is intentional — TPMN skills are platform-agnostic,
  ⊢ This skill is the WRITE counterpart to /search-skill's READ of .claude/skills/,
  ⊢ This skill closes the knowledge loop: archive → extract → .claude/skills/ → /search-skill → /plan-work
]

(* === Post-Execution Routing === *)
Routing ≜ [
  skill_path exists  → human reviews extracted skill,
  extraction_failed  → report to human with reason
]
