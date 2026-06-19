# claim-intake

**Workflow:** Health Insurance Claim Pipeline
**Domain:** Healthcare Insurance
**Contract:** `claim-01-intake: A → B | P`

---

## A: Input

```yaml
policy_no:              string    # e.g. "HIC-2024-00123"
policy_holder:          string
claimant_name:          string
claimant_relationship:  enum      # {self, spouse, child, parent, sibling, other_dependent}
id_document_type:       enum      # {nric, passport, fin, birth_certificate}
id_document_no:         string
date_of_birth:          date      # YYYY-MM-DD
claim_date:             date
incident_date:          date
claim_type:             enum      # {hospitalisation, outpatient, surgical, dental, vision, maternity, mental_health, emergency}
provider_name:          string
provider_registration:  string
claim_amount_requested: number    # SGD
supporting_documents:   string[]  # subset of {medical_bill, discharge_summary, referral_letter, prescription, lab_report, imaging_report, specialist_memo, pre_auth_approval}
claimant_contact_email: string
claimant_contact_phone: string
medical_details:        object?   # optional; if absent claim-04 will infer plausible values from supporting_documents + claim_type
payment_details:        object?   # optional; if absent claim-06 will default to direct_credit with bank info pulled from policy registry
```

## P_pre: Preconditions

### Format Validation
- policy_no matches `^HIC-\d{4}-\d{5}$`
- claim_amount_requested > 0
- incident_date ≤ claim_date

## F: Processing Logic

1. **Policy number** — match regex `^HIC-\d{4}-\d{5}$`. Else: INVALID_POLICY_FORMAT. Then `query_policies(where:{policy_no})` — else: POLICY_NOT_FOUND.

2. **Identity document** — match by `id_document_type`:
   - nric: `^[STFG]\d{7}[A-Z]$`
   - fin: `^[FG]\d{7}[A-Z]$`
   - passport: `^[A-Z]{1,2}\d{6,9}$`
   - birth_certificate: `^[A-Z]{2}\d{6}[A-Z]$`

   Else: INVALID_ID_FORMAT.

3. **Date sanity** —
   - `date_of_birth < claim_date` else: INVALID_DATE_OF_BIRTH
   - `age = floor((claim_date − date_of_birth) / 365.25)` in [0, 120] else: INVALID_DATE_OF_BIRTH
   - `incident_date ≤ claim_date` else: FUTURE_INCIDENT_DATE
   - `claim_date == now_utc8().date()` else: INVALID_CLAIM_DATE

4. **Submission window** — `claim_date − incident_date ≤ 365` calendar days. Else: LATE_SUBMISSION.

5. **Claim amount** — `claim_amount_requested > 0`. Else: INVALID_CLAIM_AMOUNT.

6. **Documents vocabulary** — every item of `supporting_documents` ∈ `{medical_bill, discharge_summary, referral_letter, prescription, lab_report, imaging_report, specialist_memo, pre_auth_approval}`. Else: UNKNOWN_DOCUMENT_TYPE.

7. **Documents completeness** — minimum by `claim_type`:

   | claim_type | required |
   |---|---|
   | hospitalisation / surgical / maternity | medical_bill + discharge_summary |
   | outpatient / dental / vision / emergency | medical_bill |
   | mental_health | medical_bill |

   Absent required tokens → `missing_documents[]`. If non-empty: MISSING_REQUIRED_DOCUMENTS.

8. **Contact** —
   - email matches `^[^@\s]+@[^@\s]+\.[^@\s]+$` else: INVALID_EMAIL
   - phone matches `^\+?[0-9]{8,15}$` else: INVALID_PHONE

9. **Decision** — `intake_accepted` = all of {format ✓, policy_exists, id_format, dates, window, amount, vocab, docs, email, phone}.

10. **Rejection reason** — first failing check (per order above), else null.

11. **Reference** — `claim_reference_draft = "DRAFT-" + now_utc8().YYYYMMDD + "-" + zfill5(seq)`.

12. **Timestamp** — `intake_timestamp = now_utc8()`.

## B: Output

```yaml
claim_reference_draft:  string    # DRAFT-YYYYMMDD-##### (YYYY from now_utc8)
policy_no:              string    # passthrough
claimant_name:          string    # passthrough
id_document_type:       enum      # passthrough
id_document_no:         string    # passthrough
date_of_birth:          date      # passthrough
claimant_relationship:  enum      # passthrough
claim_type:             enum      # passthrough
incident_date:          date      # passthrough
claim_date:             date      # passthrough
claim_amount_requested: number    # passthrough
provider_name:          string    # passthrough — consumed by claim-04
provider_registration:  string    # passthrough — consumed by claim-04
supporting_documents:   string[]  # passthrough — consumed by claim-04
medical_details:        object?   # passthrough (nullable; may be filled by claim-04)
payment_details:        object?   # passthrough — consumed by claim-06
intake_accepted:        bool
rejection_reason:       string?   # null iff intake_accepted = true
missing_documents:      string[]  # empty iff all required present
intake_timestamp:       datetime  # ISO 8601 UTC+8
```

## P_post: Postconditions

### Invariants

- intake_accepted is boolean
- intake_accepted = true ⇒ rejection_reason = null ∧ missing_documents = []
- claim_reference_draft matches `^DRAFT-\d{8}-\d{5}$`

## Circus Executor

**stage_type:** llm-assisted
**agent_role:** claim-intake-agent
**trust_gate_L1:** 70
**trust_gate_L2:** 70
