package main

func LoanPreScreen() WorkflowStep {
	return WorkflowStep{
		ID:     "loan-01-pre-screen",
		Name:   "Pre-Screen",
		Domain: "loan-approval",
		Index:  0,
		InputFields: []ContractField{
			{Name: "applicant_id", Type: "string", Required: true},
			{Name: "full_name", Type: "string", Required: true},
			{Name: "income_annual", Type: "number", Required: true},
			{Name: "income_source", Type: "enum", Required: true, Enum: []string{"salary", "self_employed", "pension", "investment", "other"}},
			{Name: "loan_amount", Type: "number", Required: true},
			{Name: "loan_purpose", Type: "enum", Required: true, Enum: []string{"mortgage", "auto", "personal", "business", "education"}},
			{Name: "employment_years", Type: "number", Required: true},
			{Name: "existing_debt", Type: "number", Required: true},
			{Name: "collateral_value", Type: "number", Nullable: true},
			{Name: "id_document_type", Type: "enum", Required: true, Enum: []string{"passport", "drivers_license", "national_id"}},
			{Name: "id_verified", Type: "bool", Required: true},
		},
		OutputFields: []ContractField{
			{Name: "applicant_id", Type: "string", Required: true},
			{Name: "eligible", Type: "bool", Required: true},
			{Name: "rejection_reason", Type: "string", Nullable: true},
			{Name: "dti_quick", Type: "number", Required: true},
			{Name: "lti", Type: "number", Required: true},
			{Name: "ltv", Type: "number", Nullable: true},
			{Name: "flags", Type: "string[]", Required: true},
			{Name: "screening_timestamp", Type: "datetime", Required: true},
		},
		Postconditions: []Postcondition{
			{ID: "PS-01", Description: "applicant_id in B matches A", CheckType: CheckConsistency, Expression: "B.applicant_id == A.applicant_id"},
			{ID: "PS-02", Description: "eligible is boolean", CheckType: CheckCompleteness, Expression: "B.eligible is bool"},
			{ID: "PS-03", Description: "If ineligible, rejection_reason is non-empty", CheckType: CheckConsistency, Expression: "B.eligible == false => B.rejection_reason != ''"},
			{ID: "PS-04", Description: "If eligible, rejection_reason is null", CheckType: CheckConsistency, Expression: "B.eligible == true => B.rejection_reason == null"},
			{ID: "PS-05", Description: "dti_quick = existing_debt / income_annual", CheckType: CheckArithmetic, Expression: "B.dti_quick == A.existing_debt / A.income_annual"},
			{ID: "PS-06", Description: "lti = loan_amount / income_annual", CheckType: CheckArithmetic, Expression: "B.lti == A.loan_amount / A.income_annual"},
			{ID: "PS-07", Description: "If collateral provided, ltv = loan_amount / collateral_value", CheckType: CheckArithmetic, Expression: "A.collateral_value != null => B.ltv == A.loan_amount / A.collateral_value"},
			{ID: "PS-08", Description: "If no collateral, ltv is null", CheckType: CheckConsistency, Expression: "A.collateral_value == null => B.ltv == null"},
			{ID: "PS-09", Description: "eligible=true implies id_verified=true", CheckType: CheckConsistency, Expression: "B.eligible == true => A.id_verified == true"},
			{ID: "PS-10", Description: "eligible=true implies income_annual > 0", CheckType: CheckConsistency, Expression: "B.eligible == true => A.income_annual > 0"},
			{ID: "PS-11", Description: "eligible=true implies dti_quick <= 0.5", CheckType: CheckConsistency, Expression: "B.eligible == true => B.dti_quick <= 0.5"},
			{ID: "PS-12", Description: "flags array contains correct flags for threshold breaches", CheckType: CheckConsistency},
			{ID: "PS-13", Description: "screening_timestamp is valid ISO 8601", CheckType: CheckCompleteness},
			{ID: "PS-14", Description: "No SPT violation: result is about THIS applicant only", CheckType: CheckSPT},
		},
		FunctionDescription: `1. Identity gate: reject if id_verified=false.
2. Income floor: check income_annual > 0.
3. Employment minimum: check employment_years >= 1 (waived for pension/investment).
4. Debt-to-income ratio: dti_quick = existing_debt / income_annual. Flag if > 0.5.
5. Loan-to-income ratio: lti = loan_amount / income_annual. Flag if > 5.0.
6. Collateral coverage (secured): ltv = loan_amount / collateral_value. Flag if > 0.8.
7. Eligibility: eligible = id_verified AND income_annual > 0 AND employment_check AND dti_quick <= 0.5.
8. Rejection reason: populate from first failing check.`,
		SampleInputA: map[string]any{
			"applicant_id":     "APP-2026-0847",
			"full_name":        "Jane Doe",
			"income_annual":    85000.0,
			"income_source":    "salary",
			"loan_amount":      200000.0,
			"loan_purpose":     "mortgage",
			"employment_years": 3.0,
			"existing_debt":    25000.0,
			"collateral_value": 280000.0,
			"id_document_type": "passport",
			"id_verified":      true,
		},
	}
}

func LoanCreditScoring() WorkflowStep {
	return WorkflowStep{
		ID:     "loan-02-credit-scoring",
		Name:   "Credit Scoring",
		Domain: "loan-approval",
		Index:  1,
		InputFields: []ContractField{
			{Name: "applicant_id", Type: "string", Required: true},
			{Name: "income_annual", Type: "number", Required: true},
			{Name: "loan_amount", Type: "number", Required: true},
			{Name: "existing_debt", Type: "number", Required: true},
			{Name: "collateral_value", Type: "number", Nullable: true},
			{Name: "credit_history", Type: "object", Required: true},
		},
		OutputFields: []ContractField{
			{Name: "applicant_id", Type: "string", Required: true},
			{Name: "credit_score", Type: "number", Required: true},
			{Name: "risk_tier", Type: "enum", Required: true, Enum: []string{"A", "B", "C", "D"}},
			{Name: "debt_ratio", Type: "number", Required: true},
			{Name: "ltv_ratio", Type: "number", Nullable: true},
			{Name: "financial_health", Type: "enum", Required: true, Enum: []string{"strong", "adequate", "weak", "critical"}},
			{Name: "score_factors", Type: "string[]", Required: true},
			{Name: "scoring_timestamp", Type: "datetime", Required: true},
		},
		Postconditions: []Postcondition{
			{ID: "CS-01", Description: "applicant_id in B matches A", CheckType: CheckConsistency, Expression: "B.applicant_id == A.applicant_id"},
			{ID: "CS-02", Description: "credit_score in [300, 850]", CheckType: CheckRange, Expression: "300 <= B.credit_score <= 850"},
			{ID: "CS-03", Description: "risk_tier consistent with credit_score", CheckType: CheckConsistency, Expression: "B.credit_score >= 750 => B.risk_tier == 'A'; >= 650 => 'B'; >= 550 => 'C'; else 'D'"},
			{ID: "CS-04", Description: "debt_ratio = existing_debt / income_annual", CheckType: CheckArithmetic, Expression: "B.debt_ratio == A.existing_debt / A.income_annual"},
			{ID: "CS-05", Description: "debt_ratio >= 0", CheckType: CheckRange, Expression: "B.debt_ratio >= 0"},
			{ID: "CS-06", Description: "If collateral, ltv_ratio = loan_amount / collateral_value", CheckType: CheckArithmetic, Expression: "A.collateral_value != null => B.ltv_ratio == A.loan_amount / A.collateral_value"},
			{ID: "CS-07", Description: "If no collateral, ltv_ratio is null", CheckType: CheckConsistency, Expression: "A.collateral_value == null => B.ltv_ratio == null"},
			{ID: "CS-08", Description: "financial_health consistent with tier + debt_ratio", CheckType: CheckConsistency},
			{ID: "CS-09", Description: "score_factors is non-empty", CheckType: CheckCompleteness, Expression: "len(B.score_factors) >= 1"},
			{ID: "CS-10", Description: "scoring_timestamp is valid ISO 8601", CheckType: CheckCompleteness},
			{ID: "CS-11", Description: "No S->T violation: score is snapshot, not permanent trait", CheckType: CheckSPT},
			{ID: "CS-12", Description: "No delta-e->integral-de: single score, not portfolio extrapolation", CheckType: CheckSPT},
		},
		FunctionDescription: `1. Base score: start at 600.
   - oldest_account_months > 60 -> +50
   - missed_payments = 0 -> +80; per missed -> -30
   - utilization_pct < 30 -> +40; > 70 -> -60
   - bankruptcies > 0 -> -200
   - inquiries_6mo > 3 -> -20
2. Clamp: credit_score = clamp(base_score, 300, 850)
3. Risk tier: A >= 750, B >= 650, C >= 550, D < 550
4. debt_ratio = existing_debt / income_annual
5. ltv_ratio = loan_amount / collateral_value (null if unsecured)
6. financial_health: strong/adequate/weak/critical based on tier + debt_ratio`,
		SampleInputA: map[string]any{
			"applicant_id":     "APP-2026-0847",
			"income_annual":    85000.0,
			"loan_amount":      200000.0,
			"existing_debt":    25000.0,
			"collateral_value": 280000.0,
			"credit_history": map[string]any{
				"accounts_open":         5,
				"accounts_closed":       2,
				"missed_payments":       0,
				"oldest_account_months": 84,
				"utilization_pct":       22.0,
				"bankruptcies":          0,
				"inquiries_6mo":         1,
			},
		},
	}
}

func LoanComplianceCheck() WorkflowStep {
	return WorkflowStep{
		ID:     "loan-03-compliance-check",
		Name:   "Compliance Check",
		Domain: "loan-approval",
		Index:  2,
		InputFields: []ContractField{
			{Name: "applicant_id", Type: "string", Required: true},
			{Name: "credit_score", Type: "number", Required: true},
			{Name: "risk_tier", Type: "enum", Required: true, Enum: []string{"A", "B", "C", "D"}},
			{Name: "loan_amount", Type: "number", Required: true},
			{Name: "loan_purpose", Type: "enum", Required: true, Enum: []string{"mortgage", "auto", "personal", "business", "education"}},
			{Name: "loan_type", Type: "enum", Required: true, Enum: []string{"secured", "unsecured"}},
			{Name: "income_annual", Type: "number", Required: true},
			{Name: "debt_ratio", Type: "number", Required: true},
			{Name: "applicant_demographics", Type: "object", Required: true},
		},
		OutputFields: []ContractField{
			{Name: "applicant_id", Type: "string", Required: true},
			{Name: "compliant", Type: "bool", Required: true},
			{Name: "regulatory_checks", Type: "object[]", Required: true},
			{Name: "flags", Type: "string[]", Required: true},
			{Name: "audit_trail", Type: "object", Required: true},
		},
		Postconditions: []Postcondition{
			{ID: "CC-01", Description: "applicant_id in B matches A", CheckType: CheckConsistency, Expression: "B.applicant_id == A.applicant_id"},
			{ID: "CC-02", Description: "compliant is boolean", CheckType: CheckCompleteness},
			{ID: "CC-03", Description: "compliant=true implies all checks pass or warn", CheckType: CheckConsistency, Expression: "B.compliant == true => all(B.regulatory_checks[].status in {pass, warning})"},
			{ID: "CC-04", Description: "compliant=false implies at least one check failed", CheckType: CheckConsistency, Expression: "B.compliant == false => any(B.regulatory_checks[].status == 'fail')"},
			{ID: "CC-05", Description: "Every check has non-empty regulation reference", CheckType: CheckCompleteness},
			{ID: "CC-06", Description: "flags subset of valid SPT/EEF enum", CheckType: CheckEnum, Expression: "B.flags subset {SPT_L_to_G, SPT_S_to_T, SPT_de_to_intde, EEF}"},
			{ID: "CC-07", Description: "checks_performed = len(regulatory_checks)", CheckType: CheckArithmetic, Expression: "B.audit_trail.checks_performed == len(B.regulatory_checks)"},
			{ID: "CC-08", Description: "checks_passed + checks_failed = checks_performed", CheckType: CheckArithmetic, Expression: "B.audit_trail.checks_passed + B.audit_trail.checks_failed == B.audit_trail.checks_performed"},
			{ID: "CC-09", Description: "audit_trail.timestamp is valid ISO 8601", CheckType: CheckCompleteness},
			{ID: "CC-10", Description: "reviewer identifies engine and version", CheckType: CheckCompleteness},
			{ID: "CC-11", Description: "No prohibited demographic factors in fair lending", CheckType: CheckConsistency},
			{ID: "CC-12", Description: "If military_status=true, MLA check present", CheckType: CheckConsistency, Expression: "A.applicant_demographics.military_status == true => 'military_lending' in B.regulatory_checks[].check_name"},
			{ID: "CC-13", Description: "If risk_tier=D, ECOA check present", CheckType: CheckConsistency, Expression: "A.risk_tier == 'D' => 'ecoa_adverse_action' in B.regulatory_checks[].check_name"},
		},
		FunctionDescription: `1. Fair lending check: verify no prohibited factors in scoring.
2. Usury limit check: lookup state-specific caps for loan_type.
3. Military lending check: if military_status=true, verify MAPR <= 36%.
4. TILA disclosure readiness: verify APR, finance charge, amount financed available.
5. ECOA adverse action: if risk_tier=D, verify adverse action notice preparable.
6. SPT flag scan: check for L->G, S->T, delta-e->integral-de violations.
7. Aggregate: compliant = no_fair_lending AND no_usury AND no_MLA AND TILA_ready AND no_SPT.`,
		SampleInputA: map[string]any{
			"applicant_id":  "APP-2026-0847",
			"credit_score":  770.0,
			"risk_tier":     "A",
			"loan_amount":   200000.0,
			"loan_purpose":  "mortgage",
			"loan_type":     "secured",
			"income_annual": 85000.0,
			"debt_ratio":    0.294,
			"applicant_demographics": map[string]any{
				"age":             32,
				"state":           "CA",
				"military_status": false,
			},
		},
	}
}

func LoanUnderwriting() WorkflowStep {
	return WorkflowStep{
		ID:     "loan-04-underwriting",
		Name:   "Underwriting",
		Domain: "loan-approval",
		Index:  3,
		InputFields: []ContractField{
			{Name: "applicant_id", Type: "string", Required: true},
			{Name: "credit_score", Type: "number", Required: true},
			{Name: "risk_tier", Type: "enum", Required: true, Enum: []string{"A", "B", "C", "D"}},
			{Name: "compliant", Type: "bool", Required: true},
			{Name: "debt_ratio", Type: "number", Required: true},
			{Name: "ltv_ratio", Type: "number", Nullable: true},
			{Name: "loan_amount", Type: "number", Required: true},
			{Name: "loan_purpose", Type: "enum", Required: true, Enum: []string{"mortgage", "auto", "personal", "business", "education"}},
			{Name: "income_annual", Type: "number", Required: true},
			{Name: "collateral_value", Type: "number", Nullable: true},
			{Name: "flags", Type: "string[]", Required: true},
		},
		OutputFields: []ContractField{
			{Name: "applicant_id", Type: "string", Required: true},
			{Name: "approved", Type: "bool", Required: true},
			{Name: "denial_reasons", Type: "string[]", Nullable: true},
			{Name: "rate", Type: "number", Required: true},
			{Name: "term_months", Type: "number", Required: true},
			{Name: "monthly_payment", Type: "number", Required: true},
			{Name: "conditions", Type: "string[]", Required: true},
			{Name: "underwriting_notes", Type: "string", Required: true},
			{Name: "decision_timestamp", Type: "datetime", Required: true},
		},
		Postconditions: []Postcondition{
			{ID: "UW-01", Description: "applicant_id in B matches A", CheckType: CheckConsistency, Expression: "B.applicant_id == A.applicant_id"},
			{ID: "UW-02", Description: "approved is boolean", CheckType: CheckCompleteness},
			{ID: "UW-03", Description: "approved=true implies compliant=true", CheckType: CheckConsistency, Expression: "B.approved == true => A.compliant == true"},
			{ID: "UW-04", Description: "approved=true implies risk_tier in {A,B,C}", CheckType: CheckConsistency, Expression: "B.approved == true => A.risk_tier in {'A','B','C'}"},
			{ID: "UW-05", Description: "approved=true implies denial_reasons null/empty", CheckType: CheckConsistency, Expression: "B.approved == true => B.denial_reasons == null or len(B.denial_reasons) == 0"},
			{ID: "UW-06", Description: "approved=false implies denial_reasons non-empty", CheckType: CheckConsistency, Expression: "B.approved == false => len(B.denial_reasons) >= 1"},
			{ID: "UW-07", Description: "rate > 0 and reasonable for tier", CheckType: CheckRange, Expression: "B.rate > 0 and B.rate < 30"},
			{ID: "UW-08", Description: "term_months valid for loan_purpose", CheckType: CheckConsistency},
			{ID: "UW-09", Description: "monthly_payment approximates amortization formula", CheckType: CheckArithmetic},
			{ID: "UW-10", Description: "conditions non-empty if approved", CheckType: CheckConsistency, Expression: "B.approved == true => len(B.conditions) >= 1"},
			{ID: "UW-11", Description: "underwriting_notes non-empty", CheckType: CheckCompleteness, Expression: "len(B.underwriting_notes) > 0"},
			{ID: "UW-12", Description: "decision_timestamp is valid ISO 8601", CheckType: CheckCompleteness},
			{ID: "UW-13", Description: "No S->T: decision about application, not applicant", CheckType: CheckSPT},
			{ID: "UW-14", Description: "No L->G: denial reason specific, not generalized", CheckType: CheckSPT},
		},
		FunctionDescription: `1. Compliance gate: if compliant=false, reject immediately.
2. Risk tier gate: if risk_tier=D, deny.
3. Rate determination: base_rate=5.0%. A: +0%, B: +1.5%, C: +3.5%. LTV>0.8: +0.5%, LTV>0.9: +1.0%. debt_ratio>0.4: +0.25%.
4. Term selection: mortgage=360, auto=60, personal=36, business=60, education=120 months.
5. Monthly payment: standard amortization P = L[r(1+r)^n]/[(1+r)^n - 1] where L=loan_amount, r=rate/12, n=term_months.
6. Conditions: collateral appraisal (secured), insurance verification (mortgage/auto), income re-verification if employment < 2 years.
7. Approval: approved = compliant AND risk_tier in {A,B,C} AND rate <= usury_cap.`,
		SampleInputA: map[string]any{
			"applicant_id":     "APP-2026-0847",
			"credit_score":     770.0,
			"risk_tier":        "A",
			"compliant":        true,
			"debt_ratio":       0.294,
			"ltv_ratio":        0.714,
			"loan_amount":      200000.0,
			"loan_purpose":     "mortgage",
			"income_annual":    85000.0,
			"collateral_value": 280000.0,
			"flags":            []any{},
		},
	}
}

func LoanDisbursement() WorkflowStep {
	return WorkflowStep{
		ID:     "loan-05-disbursement",
		Name:   "Disbursement",
		Domain: "loan-approval",
		Index:  4,
		InputFields: []ContractField{
			{Name: "applicant_id", Type: "string", Required: true},
			{Name: "approved", Type: "bool", Required: true},
			{Name: "loan_amount", Type: "number", Required: true},
			{Name: "rate", Type: "number", Required: true},
			{Name: "term_months", Type: "number", Required: true},
			{Name: "monthly_payment", Type: "number", Required: true},
			{Name: "conditions", Type: "string[]", Required: true},
			{Name: "conditions_met", Type: "bool[]", Required: true},
			{Name: "disbursement_account", Type: "object", Required: true},
		},
		OutputFields: []ContractField{
			{Name: "loan_id", Type: "string", Required: true},
			{Name: "applicant_id", Type: "string", Required: true},
			{Name: "disbursed_amount", Type: "number", Required: true},
			{Name: "disbursement_date", Type: "date", Required: true},
			{Name: "disbursement_account", Type: "string", Required: true},
			{Name: "schedule", Type: "object[]", Required: true},
			{Name: "total_interest", Type: "number", Required: true},
			{Name: "total_payments", Type: "number", Required: true},
			{Name: "outstanding_conditions", Type: "string[]", Required: true},
			{Name: "status", Type: "enum", Required: true, Enum: []string{"disbursed", "held", "failed"}},
		},
		Postconditions: []Postcondition{
			{ID: "DB-01", Description: "applicant_id in B matches A", CheckType: CheckConsistency, Expression: "B.applicant_id == A.applicant_id"},
			{ID: "DB-02", Description: "loan_id is non-empty unique string", CheckType: CheckCompleteness, Expression: "len(B.loan_id) > 0"},
			{ID: "DB-03", Description: "status=disbursed implies approved=true", CheckType: CheckConsistency, Expression: "B.status == 'disbursed' => A.approved == true"},
			{ID: "DB-04", Description: "status=disbursed implies all conditions met", CheckType: CheckConsistency, Expression: "B.status == 'disbursed' => all(A.conditions_met) == true"},
			{ID: "DB-05", Description: "status=disbursed implies outstanding_conditions empty", CheckType: CheckConsistency, Expression: "B.status == 'disbursed' => len(B.outstanding_conditions) == 0"},
			{ID: "DB-06", Description: "status held/failed implies outstanding_conditions non-empty", CheckType: CheckConsistency, Expression: "B.status in {'held','failed'} => len(B.outstanding_conditions) > 0"},
			{ID: "DB-07", Description: "disbursed_amount <= loan_amount", CheckType: CheckRange, Expression: "B.disbursed_amount <= A.loan_amount"},
			{ID: "DB-08", Description: "schedule length = term_months", CheckType: CheckArithmetic, Expression: "len(B.schedule) == A.term_months"},
			{ID: "DB-09", Description: "schedule[0].balance = loan_amount - schedule[0].principal", CheckType: CheckArithmetic},
			{ID: "DB-10", Description: "final balance near zero", CheckType: CheckArithmetic, Expression: "abs(B.schedule[-1].balance) < 1.0"},
			{ID: "DB-11", Description: "Each interest = prior_balance * (rate/12)", CheckType: CheckArithmetic},
			{ID: "DB-12", Description: "total_interest = sum(schedule[].interest)", CheckType: CheckArithmetic, Expression: "B.total_interest == sum(B.schedule[].interest)"},
			{ID: "DB-13", Description: "total_payments = disbursed_amount + total_interest", CheckType: CheckArithmetic, Expression: "B.total_payments == B.disbursed_amount + B.total_interest"},
			{ID: "DB-14", Description: "disbursement_date not future beyond T+3 days", CheckType: CheckCompleteness},
			{ID: "DB-15", Description: "Account number masked in output", CheckType: CheckConsistency},
		},
		FunctionDescription: `1. Approval gate: if approved=false, halt.
2. Conditions gate: if any conditions_met[i]=false, halt. List outstanding.
3. Account validation: routing_number is 9 digits, account_number non-empty.
4. Disbursement amount: disbursed_amount = loan_amount.
5. Payment schedule: for each month, interest_i = balance_i * (rate/12), principal_i = monthly_payment - interest_i.
6. Loan ID: generate unique loan_id (e.g., "LOAN-2026-XXXX").
7. TILA final disclosure: total_interest = sum(interest), total_payments = loan_amount + total_interest.`,
		SampleInputA: map[string]any{
			"applicant_id":    "APP-2026-0847",
			"approved":        true,
			"loan_amount":     200000.0,
			"rate":            5.0,
			"term_months":     360,
			"monthly_payment": 1073.64,
			"conditions":      []any{"collateral_appraisal", "insurance_verification"},
			"conditions_met":  []any{true, true},
			"disbursement_account": map[string]any{
				"bank_name":      "First National Bank",
				"routing_number": "021000021",
				"account_number": "1234567890",
				"account_type":   "checking",
			},
		},
	}
}

func LoanApprovalWorkflow() Workflow {
	return Workflow{
		ID:     "loan-approval",
		Name:   "Loan Approval Pipeline",
		Domain: "financial-services",
		Steps: []WorkflowStep{
			LoanPreScreen(),
			LoanCreditScoring(),
			LoanComplianceCheck(),
			LoanUnderwriting(),
			LoanDisbursement(),
		},
	}
}
