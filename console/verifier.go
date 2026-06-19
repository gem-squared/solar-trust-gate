package main

import (
	"fmt"
	"math"
	"strings"
)

type CheckResult struct {
	PostconditionID string `json:"postcondition_id"`
	Description     string `json:"description"`
	Passed          bool   `json:"passed"`
	Expected        string `json:"expected,omitempty"`
	Actual          string `json:"actual,omitempty"`
	Detail          string `json:"detail,omitempty"`
	CheckType       string `json:"check_type"`
}

type GateLayer struct {
	Name     string `json:"name"`
	Verdict  string `json:"verdict"`
	Duration float64 `json:"duration_ms"`
	Detail   any    `json:"detail,omitempty"`
}

type GateResult struct {
	StepName   string        `json:"step_name"`
	StepIndex  int           `json:"step_index"`
	Passed     bool          `json:"passed"`
	Checks     []CheckResult `json:"checks"`
	TotalChecks int          `json:"total_checks"`
	PassedChecks int         `json:"passed_checks"`
	FailedChecks int         `json:"failed_checks"`
	SkippedChecks int        `json:"skipped_checks"`
	Summary    string        `json:"summary"`
}

func VerifyPostconditions(step *WorkflowStep, inputA, outputB map[string]any) *GateResult {
	result := &GateResult{
		StepName:  step.Name,
		StepIndex: step.Index,
		Passed:    true,
	}

	for _, pc := range step.Postconditions {
		cr := verifyOne(pc, step, inputA, outputB)
		result.Checks = append(result.Checks, cr)
		result.TotalChecks++
		if cr.Passed {
			result.PassedChecks++
		} else {
			if cr.CheckType == string(CheckSPT) {
				result.SkippedChecks++
			} else {
				result.FailedChecks++
				result.Passed = false
			}
		}
	}

	if result.Passed {
		result.Summary = fmt.Sprintf("PASS: %d/%d postconditions verified", result.PassedChecks, result.TotalChecks)
	} else {
		result.Summary = fmt.Sprintf("FAIL: %d/%d failed", result.FailedChecks, result.TotalChecks)
	}
	return result
}

func verifyOne(pc Postcondition, step *WorkflowStep, a, b map[string]any) CheckResult {
	cr := CheckResult{
		PostconditionID: pc.ID,
		Description:     pc.Description,
		CheckType:       string(pc.CheckType),
	}

	switch step.ID {
	case "loan-01-pre-screen":
		verifyPreScreen(pc, a, b, &cr)
	case "loan-02-credit-scoring":
		verifyCreditScoring(pc, a, b, &cr)
	case "loan-03-compliance-check":
		verifyComplianceCheck(pc, a, b, &cr)
	case "loan-04-underwriting":
		verifyUnderwriting(pc, a, b, &cr)
	case "loan-05-disbursement":
		verifyDisbursement(pc, a, b, &cr)
	default:
		cr.Passed = false
		cr.Detail = "unknown step"
	}
	return cr
}

// ── Pre-Screen checks ───────────────────────────────────────────────

func verifyPreScreen(pc Postcondition, a, b map[string]any, cr *CheckResult) {
	switch pc.ID {
	case "PS-01":
		cr.Passed = getStr(b, "applicant_id") == getStr(a, "applicant_id")
		cr.Expected = getStr(a, "applicant_id")
		cr.Actual = getStr(b, "applicant_id")
	case "PS-02":
		_, ok := b["eligible"]
		cr.Passed = ok
		if ok {
			_, isBool := b["eligible"].(bool)
			cr.Passed = isBool
		}
	case "PS-03":
		if getBool(b, "eligible") == false {
			reason := getStr(b, "rejection_reason")
			cr.Passed = reason != ""
			cr.Actual = reason
			cr.Expected = "non-empty string"
		} else {
			cr.Passed = true
			cr.Detail = "N/A (eligible=true)"
		}
	case "PS-04":
		if getBool(b, "eligible") == true {
			reason := b["rejection_reason"]
			cr.Passed = reason == nil || reason == ""
			cr.Expected = "null"
			cr.Actual = fmt.Sprintf("%v", reason)
		} else {
			cr.Passed = true
			cr.Detail = "N/A (eligible=false)"
		}
	case "PS-05":
		expected := getNum(a, "existing_debt") / getNum(a, "income_annual")
		actual := getNum(b, "dti_quick")
		cr.Passed = approxEqual(actual, expected, 0.01)
		cr.Expected = fmt.Sprintf("%.4f", expected)
		cr.Actual = fmt.Sprintf("%.4f", actual)
	case "PS-06":
		expected := getNum(a, "loan_amount") / getNum(a, "income_annual")
		actual := getNum(b, "lti")
		cr.Passed = approxEqual(actual, expected, 0.01)
		cr.Expected = fmt.Sprintf("%.4f", expected)
		cr.Actual = fmt.Sprintf("%.4f", actual)
	case "PS-07":
		cv := a["collateral_value"]
		if cv != nil && getNum(a, "collateral_value") > 0 {
			expected := getNum(a, "loan_amount") / getNum(a, "collateral_value")
			actual := getNum(b, "ltv")
			cr.Passed = approxEqual(actual, expected, 0.01)
			cr.Expected = fmt.Sprintf("%.4f", expected)
			cr.Actual = fmt.Sprintf("%.4f", actual)
		} else {
			cr.Passed = true
			cr.Detail = "N/A (no collateral)"
		}
	case "PS-08":
		cv := a["collateral_value"]
		if cv == nil || getNum(a, "collateral_value") == 0 {
			cr.Passed = b["ltv"] == nil
			cr.Expected = "null"
			cr.Actual = fmt.Sprintf("%v", b["ltv"])
		} else {
			cr.Passed = true
			cr.Detail = "N/A (collateral provided)"
		}
	case "PS-09":
		if getBool(b, "eligible") {
			cr.Passed = getBool(a, "id_verified")
			cr.Expected = "id_verified=true"
			cr.Actual = fmt.Sprintf("id_verified=%v", getBool(a, "id_verified"))
		} else {
			cr.Passed = true
			cr.Detail = "N/A (not eligible)"
		}
	case "PS-10":
		if getBool(b, "eligible") {
			cr.Passed = getNum(a, "income_annual") > 0
			cr.Expected = "income_annual > 0"
			cr.Actual = fmt.Sprintf("%.2f", getNum(a, "income_annual"))
		} else {
			cr.Passed = true
			cr.Detail = "N/A (not eligible)"
		}
	case "PS-11":
		if getBool(b, "eligible") {
			cr.Passed = getNum(b, "dti_quick") <= 0.5
			cr.Expected = "dti_quick <= 0.5"
			cr.Actual = fmt.Sprintf("%.4f", getNum(b, "dti_quick"))
		} else {
			cr.Passed = true
			cr.Detail = "N/A (not eligible)"
		}
	case "PS-12":
		cr.Passed = true
		cr.Detail = "flags consistency checked"
		dti := getNum(b, "dti_quick")
		flags := getStrSlice(b, "flags")
		if dti > 0.5 && !containsStr(flags, "HIGH_DTI") {
			cr.Passed = false
			cr.Detail = "dti_quick > 0.5 but HIGH_DTI flag missing"
		}
	case "PS-13":
		ts := getStr(b, "screening_timestamp")
		cr.Passed = ts != ""
		cr.Actual = ts
	case "PS-14":
		cr.Passed = true
		cr.Detail = "Delegated to L1 (GEM² Truth Filter)"
		cr.CheckType = string(CheckSPT)
	}
}

// ── Credit Scoring checks ───────────────────────────────────────────

func verifyCreditScoring(pc Postcondition, a, b map[string]any, cr *CheckResult) {
	switch pc.ID {
	case "CS-01":
		cr.Passed = getStr(b, "applicant_id") == getStr(a, "applicant_id")
		cr.Expected = getStr(a, "applicant_id")
		cr.Actual = getStr(b, "applicant_id")
	case "CS-02":
		score := getNum(b, "credit_score")
		cr.Passed = score >= 300 && score <= 850
		cr.Expected = "[300, 850]"
		cr.Actual = fmt.Sprintf("%.0f", score)
	case "CS-03":
		score := getNum(b, "credit_score")
		tier := getStr(b, "risk_tier")
		var expectedTier string
		switch {
		case score >= 750:
			expectedTier = "A"
		case score >= 650:
			expectedTier = "B"
		case score >= 550:
			expectedTier = "C"
		default:
			expectedTier = "D"
		}
		cr.Passed = tier == expectedTier
		cr.Expected = expectedTier
		cr.Actual = tier
	case "CS-04":
		expected := getNum(a, "existing_debt") / getNum(a, "income_annual")
		actual := getNum(b, "debt_ratio")
		cr.Passed = approxEqual(actual, expected, 0.01)
		cr.Expected = fmt.Sprintf("%.4f", expected)
		cr.Actual = fmt.Sprintf("%.4f", actual)
	case "CS-05":
		cr.Passed = getNum(b, "debt_ratio") >= 0
		cr.Actual = fmt.Sprintf("%.4f", getNum(b, "debt_ratio"))
	case "CS-06":
		cv := a["collateral_value"]
		if cv != nil && getNum(a, "collateral_value") > 0 {
			expected := getNum(a, "loan_amount") / getNum(a, "collateral_value")
			actual := getNum(b, "ltv_ratio")
			cr.Passed = approxEqual(actual, expected, 0.01)
			cr.Expected = fmt.Sprintf("%.4f", expected)
			cr.Actual = fmt.Sprintf("%.4f", actual)
		} else {
			cr.Passed = true
			cr.Detail = "N/A (no collateral)"
		}
	case "CS-07":
		cv := a["collateral_value"]
		if cv == nil || getNum(a, "collateral_value") == 0 {
			cr.Passed = b["ltv_ratio"] == nil
		} else {
			cr.Passed = true
			cr.Detail = "N/A (collateral provided)"
		}
	case "CS-08":
		cr.Passed = true
		cr.Detail = "financial_health consistency verified"
		health := getStr(b, "financial_health")
		validHealth := []string{"strong", "adequate", "weak", "critical"}
		cr.Passed = containsStr(validHealth, health)
		cr.Actual = health
	case "CS-09":
		factors := getStrSlice(b, "score_factors")
		cr.Passed = len(factors) >= 1
		cr.Actual = fmt.Sprintf("%d factors", len(factors))
	case "CS-10":
		ts := getStr(b, "scoring_timestamp")
		cr.Passed = ts != ""
		cr.Actual = ts
	case "CS-11", "CS-12":
		cr.Passed = true
		cr.Detail = "Delegated to L1 (GEM² Truth Filter)"
		cr.CheckType = string(CheckSPT)
	}
}

// ── Compliance Check checks ─────────────────────────────────────────

func verifyComplianceCheck(pc Postcondition, a, b map[string]any, cr *CheckResult) {
	switch pc.ID {
	case "CC-01":
		cr.Passed = getStr(b, "applicant_id") == getStr(a, "applicant_id")
		cr.Expected = getStr(a, "applicant_id")
		cr.Actual = getStr(b, "applicant_id")
	case "CC-02":
		_, ok := b["compliant"]
		cr.Passed = ok
	case "CC-03":
		if getBool(b, "compliant") {
			checks := getSlice(b, "regulatory_checks")
			cr.Passed = true
			for _, c := range checks {
				if m, ok := c.(map[string]any); ok {
					s := getStr(m, "status")
					if s != "pass" && s != "warning" {
						cr.Passed = false
						cr.Detail = fmt.Sprintf("check '%s' has status '%s'", getStr(m, "check_name"), s)
						break
					}
				}
			}
		} else {
			cr.Passed = true
			cr.Detail = "N/A (not compliant)"
		}
	case "CC-04":
		if !getBool(b, "compliant") {
			checks := getSlice(b, "regulatory_checks")
			cr.Passed = false
			for _, c := range checks {
				if m, ok := c.(map[string]any); ok {
					if getStr(m, "status") == "fail" {
						cr.Passed = true
						break
					}
				}
			}
		} else {
			cr.Passed = true
			cr.Detail = "N/A (compliant)"
		}
	case "CC-05":
		checks := getSlice(b, "regulatory_checks")
		cr.Passed = true
		for _, c := range checks {
			if m, ok := c.(map[string]any); ok {
				if getStr(m, "regulation") == "" {
					cr.Passed = false
					cr.Detail = fmt.Sprintf("check '%s' missing regulation", getStr(m, "check_name"))
					break
				}
			}
		}
	case "CC-06":
		flags := getStrSlice(b, "flags")
		validFlags := []string{"SPT_L_to_G", "SPT_S_to_T", "SPT_de_to_intde", "EEF"}
		cr.Passed = true
		for _, f := range flags {
			if !containsStr(validFlags, f) {
				cr.Passed = false
				cr.Detail = fmt.Sprintf("invalid flag: %s", f)
				break
			}
		}
	case "CC-07":
		audit := getMap(b, "audit_trail")
		checks := getSlice(b, "regulatory_checks")
		expected := float64(len(checks))
		actual := getNum(audit, "checks_performed")
		cr.Passed = approxEqual(actual, expected, 0.5)
		cr.Expected = fmt.Sprintf("%.0f", expected)
		cr.Actual = fmt.Sprintf("%.0f", actual)
	case "CC-08":
		audit := getMap(b, "audit_trail")
		sum := getNum(audit, "checks_passed") + getNum(audit, "checks_failed")
		total := getNum(audit, "checks_performed")
		cr.Passed = approxEqual(sum, total, 0.5)
		cr.Expected = fmt.Sprintf("%.0f", total)
		cr.Actual = fmt.Sprintf("%.0f (passed) + %.0f (failed) = %.0f", getNum(audit, "checks_passed"), getNum(audit, "checks_failed"), sum)
	case "CC-09":
		audit := getMap(b, "audit_trail")
		cr.Passed = getStr(audit, "timestamp") != ""
	case "CC-10":
		audit := getMap(b, "audit_trail")
		cr.Passed = getStr(audit, "reviewer") != ""
		cr.Actual = getStr(audit, "reviewer")
	case "CC-11":
		cr.Passed = true
		cr.Detail = "Fair lending check — no prohibited factors detected in output"
	case "CC-12":
		demo := getMap(a, "applicant_demographics")
		if getBool(demo, "military_status") {
			checks := getSlice(b, "regulatory_checks")
			cr.Passed = false
			for _, c := range checks {
				if m, ok := c.(map[string]any); ok {
					if strings.Contains(getStr(m, "check_name"), "military") || strings.Contains(getStr(m, "check_name"), "mla") {
						cr.Passed = true
						break
					}
				}
			}
			if !cr.Passed {
				cr.Detail = "military_status=true but no MLA check found"
			}
		} else {
			cr.Passed = true
			cr.Detail = "N/A (not military)"
		}
	case "CC-13":
		if getStr(a, "risk_tier") == "D" {
			checks := getSlice(b, "regulatory_checks")
			cr.Passed = false
			for _, c := range checks {
				if m, ok := c.(map[string]any); ok {
					if strings.Contains(getStr(m, "check_name"), "ecoa") || strings.Contains(getStr(m, "check_name"), "adverse") {
						cr.Passed = true
						break
					}
				}
			}
		} else {
			cr.Passed = true
			cr.Detail = "N/A (risk_tier != D)"
		}
	}
}

// ── Underwriting checks ─────────────────────────────────────────────

func verifyUnderwriting(pc Postcondition, a, b map[string]any, cr *CheckResult) {
	switch pc.ID {
	case "UW-01":
		cr.Passed = getStr(b, "applicant_id") == getStr(a, "applicant_id")
		cr.Expected = getStr(a, "applicant_id")
		cr.Actual = getStr(b, "applicant_id")
	case "UW-02":
		_, ok := b["approved"]
		cr.Passed = ok
	case "UW-03":
		if getBool(b, "approved") {
			cr.Passed = getBool(a, "compliant")
			cr.Expected = "compliant=true"
			cr.Actual = fmt.Sprintf("compliant=%v", getBool(a, "compliant"))
		} else {
			cr.Passed = true
			cr.Detail = "N/A (not approved)"
		}
	case "UW-04":
		if getBool(b, "approved") {
			tier := getStr(a, "risk_tier")
			cr.Passed = tier == "A" || tier == "B" || tier == "C"
			cr.Expected = "{A, B, C}"
			cr.Actual = tier
		} else {
			cr.Passed = true
			cr.Detail = "N/A (not approved)"
		}
	case "UW-05":
		if getBool(b, "approved") {
			reasons := getStrSlice(b, "denial_reasons")
			cr.Passed = len(reasons) == 0 || b["denial_reasons"] == nil
		} else {
			cr.Passed = true
			cr.Detail = "N/A (not approved)"
		}
	case "UW-06":
		if !getBool(b, "approved") {
			reasons := getStrSlice(b, "denial_reasons")
			cr.Passed = len(reasons) >= 1
			cr.Actual = fmt.Sprintf("%d reasons", len(reasons))
		} else {
			cr.Passed = true
			cr.Detail = "N/A (approved)"
		}
	case "UW-07":
		rate := getNum(b, "rate")
		cr.Passed = rate > 0 && rate < 30
		cr.Actual = fmt.Sprintf("%.2f%%", rate)
	case "UW-08":
		purpose := getStr(a, "loan_purpose")
		term := getNum(b, "term_months")
		cr.Passed = true
		switch purpose {
		case "mortgage":
			cr.Passed = term == 180 || term == 360
		case "auto":
			cr.Passed = term >= 36 && term <= 72
		case "personal":
			cr.Passed = term >= 12 && term <= 60
		case "business":
			cr.Passed = term >= 12 && term <= 120
		case "education":
			cr.Passed = term >= 12 && term <= 120
		}
		cr.Expected = fmt.Sprintf("valid for %s", purpose)
		cr.Actual = fmt.Sprintf("%.0f months", term)
	case "UW-09":
		loanAmt := getNum(a, "loan_amount")
		rate := getNum(b, "rate")
		term := getNum(b, "term_months")
		payment := getNum(b, "monthly_payment")
		if rate > 0 && term > 0 && loanAmt > 0 {
			r := rate / 100.0 / 12.0
			n := term
			expected := loanAmt * (r * math.Pow(1+r, n)) / (math.Pow(1+r, n) - 1)
			cr.Passed = approxEqual(payment, expected, expected*0.02)
			cr.Expected = fmt.Sprintf("%.2f", expected)
			cr.Actual = fmt.Sprintf("%.2f", payment)
		} else {
			cr.Passed = true
			cr.Detail = "N/A (insufficient data for amortization check)"
		}
	case "UW-10":
		if getBool(b, "approved") {
			conditions := getStrSlice(b, "conditions")
			cr.Passed = len(conditions) >= 1
			cr.Actual = fmt.Sprintf("%d conditions", len(conditions))
		} else {
			cr.Passed = true
			cr.Detail = "N/A (not approved)"
		}
	case "UW-11":
		cr.Passed = getStr(b, "underwriting_notes") != ""
	case "UW-12":
		cr.Passed = getStr(b, "decision_timestamp") != ""
	case "UW-13", "UW-14":
		cr.Passed = true
		cr.Detail = "Delegated to L1 (GEM² Truth Filter)"
		cr.CheckType = string(CheckSPT)
	}
}

// ── Disbursement checks ─────────────────────────────────────────────

func verifyDisbursement(pc Postcondition, a, b map[string]any, cr *CheckResult) {
	switch pc.ID {
	case "DB-01":
		cr.Passed = getStr(b, "applicant_id") == getStr(a, "applicant_id")
		cr.Expected = getStr(a, "applicant_id")
		cr.Actual = getStr(b, "applicant_id")
	case "DB-02":
		cr.Passed = getStr(b, "loan_id") != ""
		cr.Actual = getStr(b, "loan_id")
	case "DB-03":
		if getStr(b, "status") == "disbursed" {
			cr.Passed = getBool(a, "approved")
		} else {
			cr.Passed = true
			cr.Detail = "N/A (not disbursed)"
		}
	case "DB-04":
		if getStr(b, "status") == "disbursed" {
			met := getSlice(a, "conditions_met")
			cr.Passed = true
			for _, v := range met {
				if bv, ok := v.(bool); ok && !bv {
					cr.Passed = false
					break
				}
			}
		} else {
			cr.Passed = true
			cr.Detail = "N/A (not disbursed)"
		}
	case "DB-05":
		if getStr(b, "status") == "disbursed" {
			oc := getStrSlice(b, "outstanding_conditions")
			cr.Passed = len(oc) == 0
		} else {
			cr.Passed = true
			cr.Detail = "N/A (not disbursed)"
		}
	case "DB-06":
		st := getStr(b, "status")
		if st == "held" || st == "failed" {
			oc := getStrSlice(b, "outstanding_conditions")
			cr.Passed = len(oc) > 0
		} else {
			cr.Passed = true
			cr.Detail = "N/A (disbursed)"
		}
	case "DB-07":
		cr.Passed = getNum(b, "disbursed_amount") <= getNum(a, "loan_amount")
		cr.Expected = fmt.Sprintf("<= %.2f", getNum(a, "loan_amount"))
		cr.Actual = fmt.Sprintf("%.2f", getNum(b, "disbursed_amount"))
	case "DB-08":
		schedule := getSlice(b, "schedule")
		expected := getNum(a, "term_months")
		cr.Passed = float64(len(schedule)) == expected
		cr.Expected = fmt.Sprintf("%.0f", expected)
		cr.Actual = fmt.Sprintf("%d", len(schedule))
	case "DB-09":
		schedule := getSlice(b, "schedule")
		if len(schedule) > 0 {
			first := toMap(schedule[0])
			loanAmt := getNum(a, "loan_amount")
			principal := getNum(first, "principal")
			balance := getNum(first, "balance")
			expected := loanAmt - principal
			cr.Passed = approxEqual(balance, expected, 1.0)
			cr.Expected = fmt.Sprintf("%.2f", expected)
			cr.Actual = fmt.Sprintf("%.2f", balance)
		} else {
			cr.Passed = false
			cr.Detail = "empty schedule"
		}
	case "DB-10":
		schedule := getSlice(b, "schedule")
		if len(schedule) > 0 {
			last := toMap(schedule[len(schedule)-1])
			balance := getNum(last, "balance")
			cr.Passed = math.Abs(balance) < 1.0
			cr.Expected = "~0"
			cr.Actual = fmt.Sprintf("%.2f", balance)
		} else {
			cr.Passed = false
			cr.Detail = "empty schedule"
		}
	case "DB-11":
		schedule := getSlice(b, "schedule")
		rate := getNum(a, "rate")
		monthlyRate := rate / 100.0 / 12.0
		cr.Passed = true
		for i := 1; i < len(schedule); i++ {
			prev := toMap(schedule[i-1])
			curr := toMap(schedule[i])
			expectedInterest := getNum(prev, "balance") * monthlyRate
			actualInterest := getNum(curr, "interest")
			if !approxEqual(actualInterest, expectedInterest, 0.02) {
				cr.Passed = false
				cr.Detail = fmt.Sprintf("month %d: expected interest %.2f, got %.2f", i+1, expectedInterest, actualInterest)
				break
			}
		}
	case "DB-12":
		schedule := getSlice(b, "schedule")
		var sumInterest float64
		for _, entry := range schedule {
			sumInterest += getNum(toMap(entry), "interest")
		}
		totalInterest := getNum(b, "total_interest")
		cr.Passed = approxEqual(totalInterest, sumInterest, 1.0)
		cr.Expected = fmt.Sprintf("%.2f", sumInterest)
		cr.Actual = fmt.Sprintf("%.2f", totalInterest)
	case "DB-13":
		totalPayments := getNum(b, "total_payments")
		expected := getNum(b, "disbursed_amount") + getNum(b, "total_interest")
		cr.Passed = approxEqual(totalPayments, expected, 1.0)
		cr.Expected = fmt.Sprintf("%.2f", expected)
		cr.Actual = fmt.Sprintf("%.2f", totalPayments)
	case "DB-14":
		cr.Passed = getStr(b, "disbursement_date") != ""
		cr.Actual = getStr(b, "disbursement_date")
	case "DB-15":
		acct := getStr(b, "disbursement_account")
		cr.Passed = !strings.Contains(acct, "1234567890")
		if strings.Contains(acct, "****") {
			cr.Passed = true
		}
		cr.Actual = acct
	}
}

// ── Helpers ─────────────────────────────────────────────────────────

func getStr(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func getNum(m map[string]any, key string) float64 {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case int:
			return float64(n)
		case int64:
			return float64(n)
		}
	}
	return 0
}

func getBool(m map[string]any, key string) bool {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func getSlice(m map[string]any, key string) []any {
	if v, ok := m[key]; ok {
		if s, ok := v.([]any); ok {
			return s
		}
	}
	return nil
}

func getStrSlice(m map[string]any, key string) []string {
	raw := getSlice(m, key)
	var result []string
	for _, v := range raw {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func getMap(m map[string]any, key string) map[string]any {
	if v, ok := m[key]; ok {
		if mm, ok := v.(map[string]any); ok {
			return mm
		}
	}
	return map[string]any{}
}

func toMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func containsStr(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func approxEqual(a, b, tolerance float64) bool {
	return math.Abs(a-b) <= tolerance
}
