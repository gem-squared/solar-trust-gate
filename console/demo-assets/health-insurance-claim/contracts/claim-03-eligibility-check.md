# eligibility-check

**Workflow:** Health Insurance Claim Pipeline
**Domain:** Healthcare Insurance
**Contract:** `claim-03-eligibility-check: A → B | P`

---

## A: Input

```yaml
claim_reference_draft:  string    # from claim-02-policy-verification
policy_no:              string
claimant_name:          string
claim_type:             enum
incident_date:          date
claim_amount_requested: number
policy_product_code:    string    # {COMP-HEALTH-GOLD, COMP-HEALTH-SILVER, COMP-HEALTH-BRONZE}
dependent_verified:     bool      # MUST be true (Gate G2 from policy-verification)
provider_name:          string    # passthrough — consumed by claim-04
provider_registration:  string    # passthrough — consumed by claim-04
supporting_documents:   string[]  # passthrough — consumed by claim-04
medical_details:        object?   # passthrough
payment_details:        object?   # passthrough — consumed by claim-06
```

## P_pre: Preconditions

### Format Validation
- policy_no matches `^HIC-\d{4}-\d{5}$`
- claim_amount_requested > 0

### Data-flow Gates
- Upstream `dependent_verified = true` (Gate G2)

## F: Processing Logic

1. **Coverage lookup** — `query_plan_benefits(where:{policy_product_code, claim_type})`. Empty or `covered=false`: `claim_type_covered=false`, `exclusion_reason='BENEFIT_NOT_IN_PLAN'`. Else retrieve `{waiting_period_days, annual_limit, per_claim_limit, lifetime_limit, non_panel_reimbursement_pct}`.

2. **Waiting period** — verify `incident_date ≥ policy_start_date + waiting_period_days`. Else: `waiting_period_satisfied=false`, `eligibility_failure_reason='WAITING_PERIOD_NOT_MET'`.

   Plan schedule (days):

   | plan / claim_type | outpatient | hosp / surgical | maternity | pre_existing | emergency / dental / vision / mental_health |
   |---|---|---|---|---|---|
   | GOLD | 30 | 30 | 270 | 365 | 0 |
   | SILVER | 30 | 60 | 365 | 730 | 0 |
   | BRONZE | 60 | 90 | 365 | 730 | 0 |

3. **Annual limit** — `SUM(net_payable) AS annual_utilised` from `claim_utilisation` where `(policy_no, claim_type, benefit_year=YEAR(incident_date), status IN ('paid','pending'))`. Compute `annual_limit_remaining = annual_limit − annual_utilised`. If ≤ 0: `annual_limit_available=0`, ANNUAL_LIMIT_EXHAUSTED.

4. **Per-claim ceiling** — `claimable_ceiling = min(claim_amount_requested, per_claim_limit, annual_limit_remaining)`.

5. **Lifetime limit** — `SUM(net_payable) AS lifetime_utilised` from `claim_utilisation` where `(policy_no, status='paid')`. If `lifetime_utilised ≥ lifetime_limit`: LIFETIME_LIMIT_EXHAUSTED.

6. **Decision** — `eligible` = all of {`claim_type_covered`, `waiting_period_satisfied`, `annual_limit_remaining > 0`, `not lifetime_exhausted`}. `eligibility_failure_reason` = first failing code, else null.

7. **Timestamp** — `eligibility_timestamp = now_utc8()`.

## B: Output

```yaml
claim_reference_draft:      string    # passthrough
policy_no:                  string    # passthrough
claimant_name:              string    # passthrough
claim_type:                 enum      # passthrough
incident_date:              date      # passthrough
claim_amount_requested:     number    # passthrough
eligible:                   bool
eligibility_failure_reason: string?   # null iff eligible
claim_type_covered:         bool
waiting_period_satisfied:   bool
waiting_period_days:        number?   # null when BENEFIT_NOT_IN_PLAN (plan_benefits row absent)
annual_limit:               number?   # SGD; null when BENEFIT_NOT_IN_PLAN
annual_utilised:            number?   # SGD; null when BENEFIT_NOT_IN_PLAN
annual_limit_remaining:     number?   # SGD; null when BENEFIT_NOT_IN_PLAN
per_claim_limit:            number?   # SGD; null when BENEFIT_NOT_IN_PLAN
claimable_ceiling:          number?   # SGD; min(requested, per_claim, annual_remaining); null when ineligible
eligibility_timestamp:      datetime
policy_product_code:        string    # passthrough — consumed by claim-05
provider_name:              string    # passthrough — consumed by claim-04
provider_registration:      string    # passthrough — consumed by claim-04
supporting_documents:       string[]  # passthrough — consumed by claim-04
medical_details:            object?   # passthrough
payment_details:            object?   # passthrough — consumed by claim-06
```

## P_post: Postconditions

### Invariants

- eligible is boolean
- eligible = true ⇒ claim_type_covered = true ∧ waiting_period_satisfied = true ∧ claimable_ceiling > 0 ∧ claimable_ceiling ≤ claim_amount_requested

## Circus Executor

**stage_type:** deterministic
**agent_role:** eligibility-check-agent
**trust_gate_L1:** 70
**trust_gate_L2:** 70
