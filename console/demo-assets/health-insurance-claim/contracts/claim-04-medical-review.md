# medical-review

**Workflow:** Health Insurance Claim Pipeline
**Domain:** Healthcare Insurance
**Contract:** `claim-04-medical-review: A → B | P`

---

## A: Input

```yaml
claim_reference_draft:  string    # from claim-03-eligibility-check
policy_no:              string
claimant_name:          string
claim_type:             enum
incident_date:          date
claim_amount_requested: number
claimable_ceiling:      number    # upstream invariant: ≤ claim_amount_requested
policy_product_code:    string    # passthrough — consumed by claim-05
provider_name:          string
provider_registration:  string
supporting_documents:   string[]
payment_details:        object?   # passthrough — consumed by claim-06
medical_details:        object?   # optional; inferred from supporting_documents + claim_type if absent
  primary_diagnosis_icd10: string  # e.g. "J18.9"
  procedure_cpt_codes:     string[]
  admission_date:          date?   # nullable for outpatient
  discharge_date:          date?
  attending_physician:     string
  physician_license_no:    string  # MCR-#####X
  pre_authorisation_no:    string? # PA-YYYY-######
```

## P_pre: Preconditions

### Format Validation
- medical_details ≠ null ⇒ medical_details.primary_diagnosis_icd10 matches `^[A-Z]\d{2}(\.\d{1,4})?$`
- claimable_ceiling ≤ claim_amount_requested

### Data-flow Gates
- Upstream `eligible = true` (Gate G3)

## F: Processing Logic

NOTE — server prefetches the following queries before the LLM runs:
  - `query_accredited_providers(where:{provider_registration})`
  - `query_pre_authorisations(where:{policy_no, claim_type})`
Other tables (icd10_reference, cpt_reference, icd10_cpt_plausibility,
physician_registry, medical_necessity_guidelines, rps_schedule) are NOT
prefetched because their match keys live inside `medical_details`
(nested object) — treat THOSE checks as advisory: pass when input
fields are well-formed; default to PASS unless evidence contradicts.

1. **Provider accreditation** — examine prefetched `query_accredited_providers` result.
   - ≥1 row with `panel_status='active'` AND `accreditation_expiry_date ≥ incident_date` → `non_panel_flag=false`.
   - Else → `non_panel_flag=true`. Note: non_panel is NOT a failure — just sets the reimbursement % downstream.

2. **ICD-10 / CPT validity** — advisory check (no prefetch). PASS when `primary_diagnosis_icd10` matches `^[A-Z]\d{2}(\.\d{1,4})?$` and every CPT in `procedure_cpt_codes` matches `^\d{5}$`. Else: INVALID_ICD10 or INVALID_CPT_CODE.

3. **Exclusions** — match `primary_diagnosis_icd10` against the COSMETIC / SELF_INFLICTED / SUBSTANCE_ABUSE / WAR_TERRORISM ICD-10 ranges. Any match → append to `exclusions_triggered`, set `review_failure_reason='EXCLUSIONS_TRIGGERED'`.

4. **Pre-authorisation** — for `claim_type ∈ {hospitalisation, surgical, maternity}`:
   - Examine prefetched `query_pre_authorisations` result (keyed by policy_no + claim_type).
   - If result contains ≥1 row with `status='approved'` AND `expiry_date ≥ incident_date` → `pre_auth_verified=true`.
   - Else → `pre_auth_verified=false`, `review_failure_reason='MISSING_PRE_AUTH'`.
   - For other claim_types: `pre_auth_verified=true` unconditionally.

5. **Hospitalisation duration** — if `medical_details.admission_date` and `medical_details.discharge_date` both present: `length_of_stay = discharge_date − admission_date`. Else: `length_of_stay=null`.

6. **Physician licence** — advisory. PASS when `physician_license_no` matches `^MCR-\d{5}[A-Z]$`. Else: INVALID_PHYSICIAN_LICENCE.

7. **Medical necessity** — advisory PASS when supporting_documents includes `medical_bill` (always required) plus, for hospitalisation/surgical, `discharge_summary`. Set `medical_necessity_confirmed=true` on PASS.

8. **Bill reasonableness** — `rps_benchmark = claim_amount_requested` (no prefetch for rps_schedule available — use the claim amount itself as the benchmark for the demo). `bill_variance_pct = 0`. `medical_flags=[]`.

9. **Decision** — `medical_review_passed=true` iff {`pre_auth_verified=true`, `exclusions_triggered=[]`, `medical_necessity_confirmed=true`, ICD-10 valid, CPT codes valid, physician licence valid}. Else: `medical_review_passed=false`, `review_failure_reason` = first failing check.

10. **Timestamp** — `review_timestamp = now_utc8()`.

## B: Output

```yaml
claim_reference_draft:          string   # passthrough
policy_no:                      string   # passthrough
claimant_name:                  string   # passthrough
claim_type:                     enum     # passthrough
incident_date:                  date     # passthrough
claim_amount_requested:         number   # passthrough
claimable_ceiling:              number   # passthrough
medical_review_passed:          bool
exclusions_triggered:           string[]
review_failure_reason:          string?  # null iff medical_review_passed
non_panel_flag:                 bool
pre_auth_verified:              bool
length_of_stay:                 number?  # days; null for outpatient
rps_benchmark:                  number?  # SGD; null when codes invalid (no benchmark computable)
bill_variance_pct:              number?  # null when rps_benchmark null
medical_necessity_confirmed:    bool
medical_flags:                  string[] # e.g. ["BILL_EXCEEDS_BENCHMARK","NON_PANEL_PROVIDER"]
review_timestamp:               datetime
policy_product_code:            string   # passthrough — consumed by claim-05
payment_details:                object?  # passthrough — consumed by claim-06
```

## P_post: Postconditions

### Invariants

- medical_review_passed is boolean
- medical_review_passed = true ⇒ medical_necessity_confirmed = true ∧ pre_auth_verified = true ∧ exclusions_triggered = []

## Circus Executor

**stage_type:** llm-assisted
**agent_role:** medical-review-agent
**trust_gate_L1:** 70
**trust_gate_L2:** 70
