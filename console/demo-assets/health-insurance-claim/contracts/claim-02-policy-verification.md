# policy-verification

**Workflow:** Health Insurance Claim Pipeline
**Domain:** Healthcare Insurance
**Contract:** `claim-02-policy-verification: A → B | P`

---

## A: Input

```yaml
claim_reference_draft:  string    # from claim-01-intake
policy_no:              string
claimant_name:          string
id_document_type:       enum      # {nric, passport, fin, birth_certificate}
id_document_no:         string
date_of_birth:          date
claimant_relationship:  enum      # {self, spouse, child, parent, sibling, other_dependent}
claim_type:             enum
incident_date:          date
claim_amount_requested: number
provider_name:          string    # passthrough — consumed by claim-04
provider_registration:  string    # passthrough — consumed by claim-04
supporting_documents:   string[]  # passthrough — consumed by claim-04
medical_details:        object?   # passthrough
payment_details:        object?   # passthrough — consumed by claim-06
```

## P_pre: Preconditions

### Format Validation
- policy_no matches `^HIC-\d{4}-\d{5}$`

### Data-flow Gates
- Upstream `intake_accepted = true` (Gate G1)

## F: Processing Logic

1. **Policy lookup** — `query_policies(where:{policy_no})`. Empty: POLICY_NOT_FOUND. Else retrieve `{policy_holder_id, policy_status, policy_start_date, policy_expiry_date, policy_product_code, premium_payment_mode, next_premium_due_date}`.

2. **Identity match** — `query_policy_members(where:{policy_no, id_document_type, id_document_no})`. Empty: IDENTITY_MISMATCH. Else retrieve `{member_id, relationship, dependent_status, dependent_coverage_end_date}`.

3. **Policy status** —
   - `active` → proceed
   - `lapsed` → POLICY_LAPSED
   - `cancelled` → POLICY_CANCELLED
   - `pending` → POLICY_PENDING_ACTIVATION
   - other → UNKNOWN_POLICY_STATUS

4. **Premium arrears** — `count_premium_ledger(where:{policy_no, payment_status:'unpaid'})` for `due_date ≤ incident_date`. If > 0: UNPAID_PREMIUMS.

5. **Coverage window** — `policy_start_date ≤ incident_date ≤ policy_expiry_date`:
   - `incident_date < policy_start_date` → INCIDENT_BEFORE_POLICY_START
   - `incident_date > policy_expiry_date` → OUT_OF_COVERAGE_PERIOD

6. **Dependent eligibility** — if `relationship ≠ 'self'`:
   - `dependent_status` must equal `'active'`. Else: DEPENDENT_NOT_ELIGIBLE.
   - if `dependent_coverage_end_date` is non-null and `< incident_date`: DEPENDENT_COVERAGE_EXPIRED.
   - else `dependent_verified = true`.

   If `relationship = 'self'`: `dependent_verified = true` unconditionally.

7. **Duplicate claim** — `count_claims(where:{policy_no, incident_date, claim_type, status:in('pending','approved','paid')})`. If > 0: DUPLICATE_CLAIM.

8. **Decision** — `policy_verified` = all of {found, identity, active, current_premiums, in_window, dependent_ok, not_duplicate}. `verification_failure` = first failing code, else null.

9. **Timestamp** — `verification_timestamp = now_utc8()`.

## B: Output

```yaml
claim_reference_draft:  string    # passthrough
policy_no:              string    # passthrough
claimant_name:          string    # passthrough
claim_type:             enum      # passthrough
incident_date:          date      # passthrough
claim_amount_requested: number    # passthrough
policy_verified:        bool
verification_failure:   string?   # null iff policy_verified = true
policy_start_date:      date?     # null when policy not found
policy_expiry_date:     date?     # null when policy not found
policy_product_code:    string?   # e.g. "COMP-HEALTH-GOLD"; null when policy not found
premium_payment_mode:   string?   # {monthly, quarterly, annual}; null when policy not found
dependent_verified:     bool
verification_timestamp: datetime
provider_name:          string    # passthrough — consumed by claim-04
provider_registration:  string    # passthrough — consumed by claim-04
supporting_documents:   string[]  # passthrough — consumed by claim-04
medical_details:        object?   # passthrough
payment_details:        object?   # passthrough — consumed by claim-06
```

## P_post: Postconditions

### Invariants

- policy_verified is boolean
- policy_verified = true ⇒ verification_failure = null ∧ dependent_verified = true ∧ policy_product_code is non-empty

## Circus Executor

**stage_type:** deterministic
**agent_role:** policy-verification-agent
**trust_gate_L1:** 70
**trust_gate_L2:** 70
