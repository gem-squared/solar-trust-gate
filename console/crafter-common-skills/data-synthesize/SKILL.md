---
name: data-synthesize
description: >
  (What) Populate a CE's spec.SampleI + spec.ReferenceData on disk so it has
  pre-fill input and reference DB tables ready for /ce/{wf}/{stage}/ invocation.
  (When) After /create-ce registers a CE that needs test data — or anytime the
  human wants to refresh a CE's synthetic data.
  (Why) Judges click Run on the canvas without typing JSON; reference data flows
  automatically into the LLM user prompt via ce_handlers.go.buildCEUserPrompt.
  (How) Read uploaded files OR a source dir → slice per-stage tables + scenario
  input → atomically patch the CE registry JSON.
argument-hint: "<ce_slug> [--mode=upload|from-disk|llm-generate] [--source=<path>] [--force]"
metadata:
  author: David Seo of GEM².AI
  version: 1.0.0
  introduced_by: WP-AO-40
allowed-tools:
  - Read
  - Bash(curl *)
  - Bash(ls *)
  - Bash(cat *)
---

(* TPMN SKILL — data-synthesize *)

(* Pre-fill a registered Contract-Executor's SampleI + ReferenceData fields,
   so the CE's runtime invocation has DB-table context + a sample input
   already baked into spec.ReferenceData / spec.SampleI on disk.
   Demo-critical: judges click Run without typing JSON.                    *)

(* === Modes === *)
MODE ≜ {
  upload       : files uploaded via HTTP body (multipart or base64-in-JSON)
                  — for the canvas-driven flow,
  from-disk    : server reads files directly from a path on its filesystem
                  — for operator-side bootstrap (e.g., deploy script),
  llm-generate : Wolfi/Vultr generates N synthetic scenarios from the
                  contract's A schema + F logic
                  — v1: documented, implementation deferred to a follow-up
}

(* === Input === *)
A ≜ [
  ce_slug:           𝕊,                          (* "{workflow_slug}/{stage_slug}" *)
  mode:              {upload, from-disk, llm-generate},
  uploaded_files:    Seq([filename: 𝕊, content_base64: 𝕊])?,  (* mode=upload *)
  source_dir:        Path?,                                     (* mode=from-disk *)
  num_scenarios:     ℕ?,                          (* default 5, mode=llm-generate *)
  scenario_themes:   Seq(𝕊)?,                    (* "happy-path", "denied-premium", ... *)
  force:             𝔹?                          (* default false; required to overwrite *)
]

(* === Output === *)
B ≜ [
  ce_slug:           𝕊,
  registry_path:     Path,                       (* {ceRegistryRoot}/{wf}/{stage}.json *)
  sample_i:          𝕊,                          (* JSON-stringified pre-fill input *)
  reference_data:    map[𝕊 → 𝕊],                 (* table_name → JSON-encoded rows *)
  tables_count:      ℕ,
  sample_i_bytes:    ℕ,
  test_scenarios:    Seq([                       (* full scenario bundle, optional v1 *)
    name:             𝕊,
    input:            object,
    expected_output:  object?,
    expected_verdict: {ALLOW, DENY, SUCCESS, FAILURE}?
  ])?,
  patched_at:        𝕊                           (* ISO 8601 *)
]

(* === Precondition === *)
P ≜ ce_slug ≠ ⊥
    ∧ ce_slug matches `^[a-z0-9][a-z0-9-]+/[a-z0-9][a-z0-9-]+$`
    ∧ the CESpec file exists at {ceRegistryRoot}/{wf}/{stage}.json
    ∧ (mode = upload      ⟹ uploaded_files ≠ ⊥ ∧ |uploaded_files| > 0)
    ∧ (mode = from-disk   ⟹ source_dir ≠ ⊥ ∧ directory readable)
    ∧ (mode = llm-generate ⟹ Vultr LLM key in env ∧ contract has A schema)

(* === Transform === *)
F ≜ <<
  1. Record patched_at timestamp. Validate ce_slug format and locate the
     existing CESpec JSON at {ceRegistryRoot}/{workflow}/{stage}.json.
     Return 404 if missing — /create-ce must run first.

  2. Pre-flight sticky-data guard:
       IF (spec.SampleI ≠ "" ∨ spec.ReferenceData ≠ ∅) ∧ force ≠ ⊤:
         → return 409 with diagnostic naming the already-populated fields;
           the human must re-call with --force.

  3. Branch by mode:
     MODE = upload:
       Decode each uploaded_files[i].content_base64.
       Write to a temp dir keyed by filename.
       Continue at step 4 with the temp dir as the source.

     MODE = from-disk:
       Verify source_dir readable; pass through to step 4.

     MODE = llm-generate:                                   (* v1: deferred *)
       Build Vultr prompt: spec.A + spec.PPre + spec.FBlock + N + themes.
       Call Vultr. Parse N scenarios from response. Validate each against
       spec.PPre (re-prompt failures, max 3 retries). Continue at step 5.

  4. Parse synthetic-data directory (upload/from-disk):
       Walk source_dir non-recursively (plus shallow into subdirs allowed
       per slice-config — e.g., documents/ for medical-review).
       Classify by filename pattern:
         db_*.json                    → reference table
         claim_*_full_pipeline.json   → scenario bundle (per-stage I/O)
         documents/<scenario>/*       → per-scenario document blobs
       Return SyntheticBundle:
         { tables: map[table_name → json.RawMessage],
           scenarios: Seq(ScenarioBundle) }

  5. Slice per stage:
       Look up the per-CE slice-config — which tables this CE needs and
       which scenario's stage-N._input to use for sample_i.
       Build:
         sample_i       = scenarios[0].stages[stage_idx]._input
                          JSON-stringified
         reference_data = { table_name → tables[table_name]
                            JSON-stringified | name ∈ slice-config.tables }
     Validate the sample input against spec.PPre's Type-Alignment rules
     (basic shape check). Reject if blatantly malformed.

  6. Atomic patch:
       Read existing CESpec.
       Set spec.SampleI = sample_i.
       Set spec.ReferenceData = reference_data.
       Set spec.UpdatedAt = now().
       Write to {registry_path}.new, fsync, atomic rename to {registry_path}.
       Output B.
>>

(* === Constraint === *)
CONSTRAINT ≜ [
  ⊢ NEVER overwrite spec.SampleI or spec.ReferenceData without --force=true
    — the prior data may have been hand-curated by an operator,
  ⊢ NEVER touch the contract's A / P_pre / F / B / P_post / Circus Executor
    blocks — those are /create-ce's mandate; this skill ONLY writes the
    runtime spec.SampleI + spec.ReferenceData fields,
  ⊢ NEVER fabricate scenarios that violate the CE's P_pre — every
    synthesized scenario MUST pass P_pre validation; failures retry up to
    N=3 then return 422 with the failing rule names,
  ⊢ NEVER write reference_data values as raw Go objects — always
    JSON-stringify so the ce_handlers.go prompt builder can pass them
    verbatim into the LLM user prompt,
  ⊢ NEVER call the CE itself — this skill is metadata-only;
    /ce/{wf}/{stage}/ stays untouched until the canvas Run button fires
]

(* === Invariant === *)
INV ≜ [
  ⊢ All synthesized scenarios satisfy spec.PPre — no fabricated input that
    would be auto-rejected by L1 P-check,
  ⊢ All entries in spec.ReferenceData are JSON-stringified strings — the
    map type is map[string]string by Go-struct convention; values are
    JSON-encoded rows/arrays,
  ⊢ Atomic patch invariant: the CESpec file is either fully old or fully
    new on disk; no partial-write window (write-to-.new + fsync + rename),
  ⊢ Sticky-data invariant: a CE that already has populated SampleI +
    ReferenceData is NEVER silently overwritten — --force is required,
  ⊢ Per-CE slice-config: which tables go into reference_data is
    deterministic per ce_slug (hardcoded for v1; registry-driven in v2)
]

(* === Pre-Execution Dialog === *)
Ask_Human ≜ <<
  [field: "mode",
   prompt: "Synthesize test data for {ce_slug}.\n
            (a) upload      — drop synthetic data files via the canvas UI\n
            (b) from-disk   — server reads from a path you specify\n
            (c) llm-generate — Wolfi generates N diverse scenarios for you (v1: deferred)\n
            Pick a / b / c:",
   required: ⊤,
   condition: mode = ⊥]

  [field: "uploaded_files",
   prompt: "Upload synthetic data files (db_*.json + claim_*_full_pipeline.json).",
   required: ⊤,
   condition: mode = upload ∧ uploaded_files = ⊥]

  [field: "source_dir",
   prompt: "Path on the server's filesystem (e.g., /opt/gem2-crafter/Docs/...).",
   required: ⊤,
   condition: mode = from-disk ∧ source_dir = ⊥]

  [field: "force_confirm",
   prompt: "{ce_slug} already has sample_i + reference_data populated.\n
            Overwrite? [yes / no]",
   required: ⊤,
   condition: spec.SampleI ≠ "" ∧ force = ⊥]
>>

(* === Post-Execution Routing === *)
Routing ≜ [
  success           → return to caller (AI-Pilot or human) with B; the CE
                      is now demo-ready and a canvas Run will fetch
                      reference data automatically via ce_handlers.go,
  sticky_data       → 409 to caller with diagnostic — caller decides
                      whether to re-call with --force,
  p_pre_violation   → 422 to caller with the failing P_pre rule names,
  ce_not_found      → 404 — /create-ce must run before /data-synthesize,
  llm_generate_v1   → 501 Not Implemented (mode reserved; v2 ships this)
]

(* === Cross-skill notes === *)
NOTES ≜ [
  ⊢ /create-ce must run FIRST to register the CE. /data-synthesize patches
    an existing registry entry — it does not create CEs.
  ⊢ ce_handlers.go.buildCEUserPrompt already injects spec.ReferenceData
    into the LLM user prompt at runtime. Once /data-synthesize populates
    the field, every /ce/{wf}/{stage}/ POST automatically carries the
    DB tables — no further plumbing on the workflow runner side.
  ⊢ /api/crafter/ce-registry's light projection drops SampleI +
    ReferenceData from the list response (keeps the payload small).
    Use GET /api/workflow/ce-contract?slug=... to see the full spec.
  ⊢ Recommended demo arc (WP-AO-41 builds this): /create-ce ×6 →
    /data-synthesize ×6 (mode=from-disk with the synthetic data folder)
    → /api/workflow/save with a pre-built workflow.json → canvas opens
    with the demo workflow ALREADY laid out → judge clicks Run.
]
