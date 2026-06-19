# disbursement

**Workflow:** Health Insurance Claim Pipeline
**Domain:** Healthcare Insurance
**Contract:** `claim-06-disbursement: A → B | P`

---

## A: Input

```yaml
claim_reference_draft:  string    # DRAFT-YYYYMMDD-##### (from intake)
policy_no:              string
claimant_name:          string
claim_type:             enum
claim_amount_requested: number    # passthrough from intake (for conservation check)
net_payable:            number    # SGD ≥ 0 (from adjudication)
adjudication_status:    enum      # {approved, zero_benefit, rejected}; only 'approved' proceeds
claimant_liability:     number    # SGD
adjudication_notes:     string
payment_details:
  payment_mode:    enum   # {direct_credit, cheque, giro, provider_direct}
  bank_name:       string?
  bank_account_no: string?
  bank_branch_code: string?
  payee_name:      string
```

## P_pre: Preconditions

### Format Validation
- claim_reference_draft matches `^DRAFT-\d{8}-\d{5}$`

### Data-flow Gates
- Upstream `adjudication_status='approved'` (Gate G5)

## F: Processing Logic

1. **Approval gate** — verify `adjudication_status='approved'`. Else:
   - `'zero_benefit'` → halt, `disbursement_status='halted'`, note `ZERO_BENEFIT_NO_DISBURSEMENT`
   - `'rejected'` → halt, `disbursement_status='halted'`, note `CLAIM_REJECTED_UPSTREAM`

2. **Net payable floor** — `net_payable > 0`. Else: halt, `disbursement_status='halted'`, note `ZERO_NET_PAYABLE`.

3. **Payment mode validation** —
   - `direct_credit` / `giro` → bank_account_no per bank format:
     - DBS / POSB: `^\d{10}$`
     - OCBC: `^\d{9,10}$`
     - UOB: `^\d{10}$`
     - Standard Chartered: `^\d{9}$`
     - other: `^\d{7,16}$`
   - `cheque` → no bank fields required (mailed to registered address)
   - `provider_direct` → bank fields sourced from `accredited_providers` (no claimant bank)

4. **Anti-fraud** — payee-name vs claimant-name (case-insensitive, normalised whitespace):
   - {direct_credit, giro, cheque}: payee_name must match claimant_name. Mismatch → `disbursement_status='pending_manual_review'`, halt.
   - provider_direct: payee_name must match registered provider name. Mismatch → `disbursement_status='pending_manual_review'`.

5. **Claim reference finalisation** —
   - Atomic increment: `claim_reference_no = "CLM-" + now_utc8().year + "-" + zfill7(claim_sequence.next_val)` matching `^CLM-\d{4}-\d{7}$`
   - Persist to claims table; mark draft → finalised
   - MAS-§7 traceability: `settlement_ref = "SETT-" + now_utc8().YYYYMMDD + "-" + zfill5(seq)` matching `^SETT-\d{8}-\d{5}$`

6. **Disbursement date** — business days T+N (excl. weekends + SG public holidays):
   - direct_credit / giro → T+3
   - cheque → T+7
   - provider_direct → T+5

7. **Deductible ledger write** —
   ```
   INSERT INTO deductible_ledger
     (policy_no, benefit_year, claim_reference_no, amount, posted_at)
     VALUES (:policy_no, YEAR(:incident_date), :claim_reference_no,
             :deductible_applied_this_claim, now_utc8())
   ```

8. **Annual utilisation ledger write** —
   ```
   INSERT INTO claim_utilisation
     (policy_no, claim_type, benefit_year, claim_reference_no, net_payable, status, posted_at)
     VALUES (:policy_no, :claim_type, YEAR(:incident_date), :claim_reference_no,
             :net_payable, 'paid', now_utc8())
   ```

9. **Output assembly** — mask bank_account_no to last-4 (`****XXXX`).

10. **Timestamp** — `processing_timestamp = now_utc8()`.

## B: Output

```yaml
claim_reference_no:     string?   # CLM-YYYY-####### (YYYY = now_utc8().year); null when halted
settlement_ref:         string?   # SETT-YYYYMMDD-##### (MAS-§7); null when halted
policy_no:              string    # passthrough
claimant_name:          string    # passthrough
claim_type:             enum      # passthrough
claim_amount_requested: number    # passthrough — needed for L2 conservation check
disbursement_status:    enum      # {disbursed, halted, pending_manual_review}
net_payable:            number    # SGD; 0 when halted
claimant_liability:     number    # passthrough
payment_mode:           enum      # passthrough
payee_name:             string
masked_bank_account_no: string?   # "****XXXX" tail-4 only; null for cheque
disbursement_date:      date?     # T+N business days; null when halted
incident_date:          date      # passthrough
remarks:                string    # audit rationale
processing_timestamp:   datetime
```

## P_post: Postconditions

### Invariants

- disbursement_status='disbursed' ⇒ claim_reference_no matches `^CLM-\d{4}-\d{7}$` ∧ settlement_ref matches `^SETT-\d{8}-\d{5}$`
- conservation: net_payable + claimant_liability = claim_amount_requested (±0.01)

## Circus Executor

**stage_type:** hybrid
**agent_role:** disbursement-agent
**trust_gate_L1:** 70
**trust_gate_L2:** 70
