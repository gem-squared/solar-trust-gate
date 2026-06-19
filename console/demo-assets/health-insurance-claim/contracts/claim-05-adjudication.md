# claim-adjudication

**Workflow:** Health Insurance Claim Pipeline
**Domain:** Healthcare Insurance
**Contract:** `claim-05-adjudication: A → B | P`

---

## A: Input

```yaml
claim_reference_draft:  string    # from claim-04-medical-review
policy_no:              string
claimant_name:          string
claim_type:             enum
claim_amount_requested: number
claimable_ceiling:      number    # eligibility-check; ≤ claim_amount_requested
rps_benchmark:          number?   # medical-review; null when codes invalid upstream
non_panel_flag:         bool      # medical-review
policy_product_code:    string    # {COMP-HEALTH-GOLD, COMP-HEALTH-SILVER, COMP-HEALTH-BRONZE}
payment_details:        object?   # passthrough — consumed by claim-06
```

NOTE: benefit_schedule is queried at runtime from plan_benefits (F-block
step 1), not supplied as input. The agent retrieves
{deductible_annual, co_payment_pct, co_insurance_pct, co_insurance_cap,
non_panel_reimbursement_pct} per (policy_product_code, claim_type).

## P_pre: Preconditions

### Format Validation
- claim_amount_requested > 0
- claimable_ceiling ≤ claim_amount_requested

### Data-flow Gates
- Upstream `medical_review_passed = true` (Gate G4)

## F: Processing Logic

1. **Benefit schedule retrieval** — `query_plan_benefits(where:{policy_product_code, claim_type})`. Load `{deductible_annual, co_payment_pct, co_insurance_pct, co_insurance_cap, non_panel_reimbursement_pct}`.

2. **Deductible utilised** — `SUM(amount) AS deductible_utilised` from `query_deductible_ledger(where:{policy_no, benefit_year:YEAR(incident_date)})`. Default 0 when empty.

3. **Adjudication base** —
   - `base = min(claim_amount_requested, rps_benchmark)` (excess over benchmark = claimant liability)
   - `base = min(base, claimable_ceiling)` (eligibility-check cap)
   - if `non_panel_flag=true`: `base = base × (non_panel_reimbursement_pct / 100)` (GOLD 80%, SILVER 70%, BRONZE 60%)
   - `adjudication_base = round(base, 2)`

4. **Deductible** —
   - `deductible_remaining = max(0, deductible_annual − deductible_utilised)`
   - `amount_after_deductible = max(0, adjudication_base − deductible_remaining)`
   - `deductible_applied_this_claim = adjudication_base − amount_after_deductible`

5. **Co-payment** — `co_pay_amount = round(amount_after_deductible × (co_payment_pct / 100), 2)`; `amount_after_copay = amount_after_deductible − co_pay_amount`.

6. **Co-insurance** —
   - `co_insurance_raw = round(amount_after_copay × (co_insurance_pct / 100), 2)`
   - `co_insurance_amount = min(co_insurance_raw, co_insurance_cap)`
   - `amount_after_coinsurance = amount_after_copay − co_insurance_amount`

7. **Net payable** — `net_payable = round(amount_after_coinsurance, 2)`. Invariants: `0 ≤ net_payable ≤ adjudication_base`.

8. **Claimant liability** — `claimant_liability = round(claim_amount_requested − net_payable, 2)`.

9. **Status** —
   - `net_payable > 0` → `adjudication_status = 'approved'`
   - `net_payable = 0` and no upstream rejection → `'zero_benefit'`
   - upstream rejection → `'rejected'`

10. **Timestamp** — `adjudication_timestamp = now_utc8()`.

## B: Output

```yaml
claim_reference_draft:          string    # passthrough
policy_no:                      string    # passthrough
claimant_name:                  string    # passthrough
claim_type:                     enum      # passthrough
claim_amount_requested:         number    # passthrough
adjudication_base:              number    # SGD; ≤ min(rps_benchmark, claimable_ceiling)
deductible_applied_this_claim:  number    # SGD
co_pay_amount:                  number    # SGD
co_insurance_amount:            number    # SGD; ≤ co_insurance_cap
net_payable:                    number    # SGD; insurer disbursement
claimant_liability:             number    # SGD; out-of-pocket
adjudication_status:            enum      # {approved, zero_benefit, rejected}
adjudication_notes:             string
adjudication_timestamp:         datetime
incident_date:                  date      # passthrough — needed by claim-06 for ledger writes
payment_details:                object?   # passthrough — consumed by claim-06
```

## P_post: Postconditions

### Invariants

- net_payable ≥ 0 ∧ claimant_liability ≥ 0
- **conservation**: net_payable + claimant_liability = claim_amount_requested (±0.01)
- adjudication_status='approved' ⇔ net_payable > 0

## Circus Executor

**stage_type:** deterministic
**agent_role:** claim-adjudication-agent
**trust_gate_L1:** 70
**trust_gate_L2:** 70
