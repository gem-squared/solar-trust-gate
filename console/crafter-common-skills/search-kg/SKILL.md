---
name: search-kg
description: >
  (What) Search proven unit-contracts and live work-plans for reusable patterns.
  (When) Before /plan-work, or anytime human asks "have we done something like this before?"
  (Why) Proven CONTRACTs are the reusable unit. Without retrieval, the archive is a graveyard.
  (How) L0: Glob/Grep/Read on archive/ + work-plan/. L1: additionally gem2-kg semantic search.
argument-hint: "[search query — what pattern are you looking for?]"
metadata:
  author: David Seo of GEM².AI
  version: 11.0.0-draft
allowed-tools:
  - Read
  - Glob
  - Grep
  - Bash(date *)
  - mcp__gem2-studio__gem2_search
  - mcp__gem2-studio__gem2_knowledge
---

(* TPMN SKILL — search-kg *)

(* === Grounding 5W === *)
Grounding_5W ≜ [
  who:   "Any role needing prior art — typically ARCHITECT before /plan-work",
  what:  "Search proven unit-contracts and live work-plans for reusable patterns",
  when:  "Before /plan-work decomposition, or anytime human asks 'have we done something like this before?'",
  where: "L0: .gem-squared/archive/ + .gem-squared/work-plan/ files. L1: additionally gem2-kg semantic store",
  why:   "Proven CONTRACTs are the reusable unit of TPMN. Without retrieval, the archive is a graveyard, not a library"
]

(* === Layers === *)
L0 ≜ "Filesystem search — Glob/Grep/Read on archive/ and work-plan/ directories"
L1 ≜ "gem2-studio MCP — additionally gem2_search(semantic) on entity_type='contract'"

(* === Input === *)
A ≜ [
  query: 𝕊,                            (* what the human is looking for — natural language *)
  project_slug: 𝕊,
  scope: {archive, live, all}?,         (* ⊥ = all. archive = proven only, live = work-plan/ only *)
  state_filter: {SUCCESS, FAILURE, —}?, (* ⊥ = no filter. SUCCESS = proven contracts only *)
  limit: ℕ?,                            (* ⊥ = 5. max results to return *)
  layer: {L0, L1}                       (* inherited from /init-session *)
]

(* === Output === *)
B ≜ [
  project_slug: 𝕊,
  searched_at: 𝕊,                      (* ISO8601 *)
  query: 𝕊,                            (* echoed back *)
  layer: {L0, L1},
  results: Seq([
    wp_id: 𝕊,                          (* e.g., "WP-ST-69" *)
    wp_title: 𝕊,                       (* WP-level title *)
    wp_state: {SUCCESS, FAILURE, —},   (* WP-level STATE *)
    source: {archive, live},            (* which directory *)
    unit_index: ℕ,                      (* 1-based unit number within WP *)
    unit_title: 𝕊,                     (* unit-work title *)
    unit_state: {SUCCESS, FAILURE, —}, (* per-unit State *)
    contract: [                         (* extracted CONTRACT *)
      a: 𝕊,                            (* input specification *)
      b: 𝕊,                            (* output specification *)
      p: 𝕊                             (* preconditions *)
    ],
    result_summary: 𝕊?,                (* Result field — ⊥ if PENDING *)
    relevance: {exact, partial, weak}   (* L0: keyword match. L1: semantic similarity *)
  ]),
  result_count: ℕ,                      (* |results| *)
  sources_searched: Seq(𝕊)             (* directories and/or MCP endpoints queried *)
]

(* === Precondition === *)
P ≜ query ≠ ⊥
    ∧ project_slug ≠ ⊥
    ∧ (".gem-squared/archive/" exists ∨ ".gem-squared/work-plan/" exists)

(* === Transform === *)
F ≜ <<
  1. L0 — Tag-based filesystem search:
       Determine scope directories:
         scope = archive → .gem-squared/archive/ only.
         scope = live    → .gem-squared/work-plan/ only.
         scope = all     → both directories.
       Glob *.md in scope directories → candidate WP files.
       Grep for `- Tags:` lines across all candidate files → tag index.
       Derive search tags from query:
         Convert query to {verb-ing}-{object} candidates.
         e.g., "terminal kill" → [killing-terminal, closing-terminal, managing-terminal].
       For each WP file with tag matches:
         Read header → extract wp_id, wp_title, wp_state.
         IF state_filter ≠ ⊥ ∧ wp_state ≠ state_filter → skip.
         Parse unit-works with matching tags:
           For each ### {N}. {title} | STATUS: {status} section:
             Extract Tags:, A:, B:, P:, Result:, State: fields.
             Score relevance:
               exact:   query tag matches a unit tag fully,
               partial: query verb OR object matches a tag component,
               weak:    query words found in unit title or A/B/P (fallback).
       Sort results: archive before live, SUCCESS before FAILURE,
         exact before partial.
       Trim to limit.
  2. L1 — Semantic search (IF layer = L1):
       gem2_search(action=semantic, query=query, project_slug=project_slug,
         entity_type='contract', limit=limit).
       For each result:
         Parse knowledge content → extract unit-contract fields.
         Score relevance by semantic similarity (from gem2-kg vector distance).
       Merge with L0 results:
         Deduplicate by wp_id + unit_index.
         L1 semantic results can promote weak L0 matches to partial
           if vector distance is close.
  3. Output B:
       Record searched_at timestamp.
       Assemble results Seq with all fields populated.
       Record sources_searched (directories queried + MCP endpoints if L1).
       Output B as structured report — human-readable table + raw data.
>>

(* === Constraint === *)
CONSTRAINT ≜ [
  ⊢ Strictly read-only — NEVER modifies any file,
  ⊢ NEVER modifies gem2-studio state — read-only queries only,
  ⊢ NEVER executes work — pattern retrieval only,
  ⊢ NEVER plans work — that is /plan-work mandate,
  ⊢ NEVER ranks by recency alone — relevance to query is primary sort key,
  ⊢ gem2-studio MCP unavailability does NOT block search — L0 is complete and valid
]

(* === Invariant === *)
INV ≜ [
  ⊢ Zero side effects — filesystem and gem2-studio unchanged after execution,
  ⊢ B is state (search results), not action — AI reads B and decides what to do with patterns,
  ⊢ Every result includes the full extracted CONTRACT (A, B, P) — not just titles,
  ⊢ Source provenance is always recorded: archive vs live, SUCCESS vs FAILURE,
  ⊢ L0 output is self-sufficient — human and AI can reuse patterns from file search alone,
  ⊢ L1 enhances relevance ranking via semantic similarity — does not replace L0 results,
  ⊢ Proven contracts (archive + SUCCESS) ranked above live/unverified contracts,
  ⊢ This skill is the READ counterpart to /archive-work's WRITE of proven contracts
]

(* === Post-Execution Routing === *)
Routing ≜ [
  result_count > 0  → /plan-work (patterns found — inform decomposition),
  result_count = 0  → /plan-work (no prior art — decompose from scratch),
  human_browsing    → display results, await human decision
]