(* TPMN SKILL — create-ce *)
(* WP-AO-26 Unit 3 — CE generator skill *)

(* === Layers === *)
L0 ≜ "filesystem registry — instantly callable via /ce/{workflow}/{stage}/"

(* === Input === *)
A ≜ [
  file_path: 𝕊,             (* path to TPMN contract markdown; relative resolves under active project's uploaded_files/ *)
  workflow_slug: 𝕊?,        (* override; ⊥ = derive from contract's **Workflow:** metadata *)
  stage_slug: 𝕊?,           (* override; ⊥ = derive from contract's **Contract:** signature *)
  vultr_model: 𝕊?,          (* override; ⊥ = use contract's Circus Executor field OR registry default vultr/Kimi-K2.6 *)
  force: 𝔹?                 (* ⊥ = ⊥ (false). If ⊤, overwrite existing CE at the same slug *)
]

(* === Output === *)
B ≜ [
  ce_slug: 𝕊,               (* "{workflow}/{stage}" *)
  registry_path: Path,      (* .gem-squared/ce-registry/{workflow}/{stage}.json *)
  endpoint_url: 𝕊,          (* https://ai-olympic.gemsquared.ai/ce/{workflow}/{stage}/ *)
  preview: [
    contract_title: 𝕊,
    a_summary:      𝕊,       (* first 200 chars of A block *)
    b_summary:      𝕊,       (* first 200 chars of B block *)
    p_pre_count:    ℕ,
    p_post_count:   ℕ,
    trust_gate_l1:  ℕ,
    trust_gate_l2:  ℕ,
    vultr_model:    𝕊        (* resolved — actual model the CE will call *)
  ],
  immediately_runnable: 𝔹    (* ⊤ — no redeploy needed *)
]

(* === Precondition === *)
P ≜ file_path ≠ ⊥
    ∧ resolved_file exists ∧ is a file ∧ is readable
    ∧ resolved_file passes parseContractMarkdown (5-block split format per Docs/contract-authoring-guide.md)

(* === Transform === *)
F ≜ <<
  1. Resolve file_path:
       IF absolute → use as-is.
       ELSE IF starts with "uploaded_files/" → resolve under
            {baseDir}/.gem-squared/workspace/{active_project}/uploaded_files/...
       ELSE → resolve under {baseDir}/.gem-squared/workspace/{active_project}/...
       Verify file exists.

  2. Parse the contract via parseContractFile(resolved_path):
       IF conflatedContractError → return rejection:
         { error: "contract_format_invalid",
           detail: "File lacks ## P_pre: header — likely the old A/F/B/P conflated
                    format. See Docs/contract-authoring-guide.md §2 for the split." }

  3. Apply overrides:
       spec.WorkflowSlug = workflow_slug ?: spec.WorkflowSlug
       spec.StageSlug    = stage_slug    ?: spec.StageSlug
       spec.VultrModel   = vultr_model   ?: spec.VultrModel

  4. validateCESpec(spec):
       IF error → return validation failure.

  5. Collision check:
       IF loadCESpec(spec.WorkflowSlug, spec.StageSlug) returns existing spec:
         IF force = ⊥ (false) → return { error: "ce_collision",
                                          existing: { ce_slug, created_at },
                                          hint: "pass force=true to overwrite" }
         IF force = ⊤ → preserve existing.CreatedAt, proceed to save.

  6. saveCESpec(spec):
       Atomic write via tmp + rename. Stamps CreatedAt (first save) + UpdatedAt.

  7. Compose output preview:
       a_summary  = first 200 chars of spec.A (string repr)
       b_summary  = first 200 chars of spec.B
       p_pre_count, p_post_count = lengths
       trust_gate_l1, trust_gate_l2 = spec fields
       vultr_model = spec.resolvedVultrModel() — actual runtime model

  8. Return:
       ce_slug = "{workflow}/{stage}"
       registry_path = cePath(workflow, stage)
       endpoint_url = "https://ai-olympic.gemsquared.ai/ce/{workflow}/{stage}/"
       immediately_runnable = ⊤
>>

(* === Constraint === *)
CONSTRAINT ≜ [
  ⊢ NEVER write outside .gem-squared/ce-registry/ — that is the CE namespace,
  ⊢ NEVER call the SaaS / Vultr LLM during CE creation — this is config-only,
  ⊢ NEVER mutate the source contract file — read-only,
  ⊢ Collision must be explicit — overwrites require force=true,
  ⊢ Conflated contracts are rejected — point caller to authoring guide,
  ⊢ Slugs must pass [a-z0-9][a-z0-9-]{0,62}[a-z0-9] regex — URL + filesystem safe
]

(* === Invariant === *)
INV ≜ [
  ⊢ After successful execution: /ce/{workflow}/{stage}/ is IMMEDIATELY callable,
  ⊢ Registry file is the single source of truth for CE behavior at runtime,
  ⊢ CE execution is single-F — NO L1/L2 calls inside; verification is orchestrator's job (future WP),
  ⊢ Registry survives binary restart (file-backed),
  ⊢ Idempotent for the same {workflow,stage} when force=⊤ — re-running updates UpdatedAt only,
  ⊢ MANDATE BOUNDARY: parse + validate + persist — does NOT execute the contract
]

(* === Pre-Execution Dialog === *)
Ask_Human ≜ <<
  [field: "file_path",
   prompt: "Path to the TPMN contract file (under uploaded_files/ or absolute)",
   required: ⊤,
   condition: file_path = ⊥]
>>

(* === Post-Execution Routing === *)
Routing ≜ [
  on_success      → return endpoint_url to user; CE is live,
  on_rejection    → surface error + link to Docs/contract-authoring-guide.md,
  ce_collision    → ask human about force=true,
  next_phase      → orchestrator (WP-AO-27) chains L1 → /ce/{wf}/{stage}/ → L2
]
