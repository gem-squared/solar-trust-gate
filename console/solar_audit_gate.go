package main

// Solar-native L1/L2 audit gates.
//
// Solar Pro 3 acts as the verifier (V), guided by TPMN protocol prompts.
// The evidence anchor A (ExtractedFacts) is injected so verification is
// grounded in document-derived facts, not naive introspection (C2 constraint).
//
// Output schema: GateVerdict{risk_score, evidence_refs, confidence, epistemic_tag, drift_fingerprints}
// ALLOW/DENY derived: risk_score < T → ALLOW/SUCCESS; risk_score ≥ T → DENY/FAILURE.

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

const solarAuditModel = "solar-pro3-260323"

// solarPCheck — L1 pre-execution gate.
// Checks that input I satisfies preconditions P.
// Verdict: ALLOW | DENY. Score: 0-100.
func solarPCheck(req AuditGateRequest) (*AuditGateResponse, error) {
	model := envOr("UPSTAGE_MODEL", solarAuditModel)

	iJSON, _ := json.MarshalIndent(req.I, "", "  ")
	aJSON, _ := json.MarshalIndent(req.A, "", "  ")

	pLines := strings.Join(req.P, "\n- ")
	if len(req.P) > 0 {
		pLines = "- " + pLines
	}

	systemPrompt := `You are a TPMN-Checker performing an L1 precondition gate (P-check).
Your role: verify that the input satisfies ALL listed preconditions before execution is allowed.
This is a STRUCTURED VERIFICATION task. You do NOT execute the task — you gate it.

Epistemic discipline (tag every finding):
- ⊢ GROUNDED: confirmed directly from input fields
- ⊨ INFERRED: derived from ⊢ with visible chain
- ⊬ EXTRAPOLATED: beyond evidence — flag explicitly
- ⊥ UNKNOWN: field absent or unverifiable

Output ONLY valid JSON. No markdown fences. Schema:
{
  "risk_score": <integer 0-100; 0=fully satisfied, 100=all preconditions failed>,
  "evidence_refs": ["<field name or literal value cited as evidence>"],
  "confidence": <integer 0-100; your confidence in this assessment>,
  "epistemic_tag": "<dominant tag: ⊢|⊨|⊬|⊥>",
  "drift_fingerprints": []
}`

	userPrompt := fmt.Sprintf(`## Input (I) — value to gate
%s

## Input Schema (A) — what I represents
%s

## Preconditions (P) — ALL must hold for low risk_score
%s

## Session Context
%s

Verify each precondition against I.
- If ALL preconditions hold (⊢): risk_score 0-30 (low risk → ALLOW)
- If ANY precondition fails or is ⊥: risk_score 70-100 (high risk → DENY)
- evidence_refs: list each input field name you checked
- epistemic_tag: dominant tag across all findings`,
		string(iJSON),
		string(aJSON),
		pLines,
		req.SessionContext,
	)

	messages := []map[string]string{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": userPrompt},
	}

	log.Printf("[SOLAR-L1] calling %s for p-check (P: %d conditions)", model, len(req.P))
	start := time.Now()

	raw, err := solarChatMessages(model, messages, 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("solar L1 call: %w", err)
	}
	log.Printf("[SOLAR-L1] p-check complete in %v", time.Since(start))

	return parseSolarAuditResponse(raw, "ALLOW", "DENY")
}

// L2EvidenceCheck is a per-인용근거 epistemic audit result.
// L2 emits one of these for each 인용근거 cited in F's output.
type L2EvidenceCheck struct {
	InYonGunGeo string `json:"인용근거"`
	Tag         string `json:"tag"`                    // ⊢|⊨|⊬|⊥
	AnchorField string `json:"anchor_field,omitempty"` // field in A that grounds this
	TFCRuleID   string `json:"tfc_rule,omitempty"`     // TFC rule that applies
	Note        string `json:"note,omitempty"`
}

// GateVerdictV2 extends GateVerdict with per-인용근거 evidence checks (L2 epistemic climax).
type GateVerdictV2 struct {
	RiskScore         int               `json:"risk_score"`
	EvidenceChecks    []L2EvidenceCheck `json:"evidence_checks,omitempty"`
	EvidenceRefs      []string          `json:"evidence_refs"`
	Confidence        int               `json:"confidence"`
	EpistemicTag      string            `json:"epistemic_tag"`
	DriftFingerprints []string          `json:"drift_fingerprints"`
}

// solarL1LightGate — lightweight L1 completeness gate using TFC preconditions.
// Design: fast, no SPT/EEF, just "is judgment even attemptable?" (G2).
// Takes TFC preconditions (from TCE) instead of raw CE p_pre.
func solarL1LightGate(anchorA map[string]any, tfc *TFC) (*AuditGateResponse, error) {
	model := envOr("UPSTAGE_MODEL", solarAuditModel)

	aJSON, _ := json.MarshalIndent(anchorA, "", "  ")
	preconditions := TFCPreconditions(tfc)

	pLines := strings.Join(preconditions, "\n- ")
	if len(preconditions) > 0 {
		pLines = "- " + pLines
	}

	systemPrompt := `당신은 보험금 청구 심사 시스템의 L1 경량 사전조건 게이트입니다.
역할: 앵커 A의 데이터가 판정을 시도하기에 충분한가를 확인합니다. 가벼운 완전성·형식 검사만 합니다.
SPT 드리프트나 인식론 심층 분석은 이 단계의 과제가 아닙니다 (그것은 L2).

JSON만 출력하세요. 마크다운 펜스 없음. 스키마:
{
  "risk_score": <0-100; 0=완전하게 판정 가능, 100=판정 불가>,
  "evidence_refs": ["확인한 A 필드명"],
  "confidence": <0-100>,
  "epistemic_tag": "⊢|⊨|⊬|⊥",
  "drift_fingerprints": []
}`

	userPrompt := fmt.Sprintf(`## 앵커 A (추출된 사실)
%s

## TFC 사전조건 (L1 규칙 — 약관 조항에서 유래)
%s

각 사전조건을 A에 대조하세요.
- 모든 조건 충족 (⊢): risk_score 0-25 → ALLOW
- 필수 필드 누락 또는 형식 오류: risk_score 70-100 → DENY
- evidence_refs: 확인한 필드명 나열`,
		string(aJSON),
		pLines,
	)

	messages := []map[string]string{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": userPrompt},
	}

	log.Printf("[SOLAR-L1] calling %s for light precondition gate (%d TFC rules)", model, len(preconditions))
	start := time.Now()

	raw, err := solarChatMessages(model, messages, 45*time.Second)
	if err != nil {
		return nil, fmt.Errorf("solar L1 light gate: %w", err)
	}
	log.Printf("[SOLAR-L1] light gate complete in %v", time.Since(start))

	return parseSolarAuditResponse(raw, "ALLOW", "DENY")
}

// solarL2EpistemicGate — epistemic climax gate (L2).
// Input: {TFC + anchor A + F-output}. Per-인용근거 ⊢⊨⊬⊥ tagging + SPT drift detection.
// This is the hero of the demo — catches ungrounded evidence references (2막).
func solarL2EpistemicGate(tfc *TFC, anchorA map[string]any, judgmentF map[string]any) (*AuditGateResponse, error) {
	model := envOr("UPSTAGE_MODEL", solarAuditModel)

	tfcJSON := TFCToJSON(tfc)
	aJSON, _ := json.MarshalIndent(anchorA, "", "  ")
	fJSON, _ := json.MarshalIndent(judgmentF, "", "  ")

	postconditions := TFCPostconditions(tfc)
	pLines := strings.Join(postconditions, "\n- ")
	if len(postconditions) > 0 {
		pLines = "- " + pLines
	}

	systemPrompt := `당신은 보험금 청구 심사의 L2 근거 역추적 검증 게이트입니다.
역할: F(Solar 판정)의 모든 인용근거를 앵커 A(독립 추출 사실)에 역추적하고, TFC 후제조건을 대조합니다.

핵심 임무:
1. F의 "인용근거" 배열에 있는 항목을 하나씩 evidence_checks 배열로 열거하세요.
   인용근거가 없거나 빈 배열이면, F의 판정 "사유"에서 언급된 근거를 항목으로 추출하세요.
2. 각 항목에 태그: ⊢(앵커 A에 직접 있음) ⊨(A에서 추론 가능) ⊬(A에 없음/미검증 주장) ⊥(확인 불가)
3. TFC 후제조건 대조 — 특히 R5(특약은 가입증명서 필요):
   F가 특약 코드(rider_claims)를 인용근거로 사용했다면 반드시 확인하세요:
   - A의 attached_docs에 해당 특약 가입증명서가 있는가?
   - 없으면: tag=⊬, tfc_rule=R5, note="가입증명서 미첨부 — TFC R5 위반"
4. drift_fingerprints: 실제 감지된 패턴만 기재하세요.
   ⊬ 케이스(미검증 주장)는 "⊬ 미검증 특약 주장" 또는 "⊥ 가입증명서 누락"으로 기재하세요.
   Δe→∫de는 "희박한 근거를 확립된 추세로 과장"하는 경우에만 사용하세요.

출력: JSON만. 마크다운 코드 블록 없음.
evidence_checks 배열에 F의 인용근거를 반드시 하나 이상 포함하세요.

출력 예시 (특약 ⊬ 케이스):
{
  "risk_score": 75,
  "evidence_checks": [
    {"인용근거": "피보험자명: 김도현", "tag": "⊢", "anchor_field": "피보험자명", "tfc_rule": "R1", "note": "A에서 확인"},
    {"인용근거": "CI-RIDER-2026-07 특약 보장", "tag": "⊬", "anchor_field": null, "tfc_rule": "R5", "note": "가입증명서 미첨부 — TFC R5 위반. attached_docs에 없음"},
    {"인용근거": "진단코드: K35.80", "tag": "⊢", "anchor_field": "진단코드", "tfc_rule": "R2", "note": "A에서 확인"}
  ],
  "evidence_refs": ["피보험자명", "진단코드"],
  "confidence": 88,
  "epistemic_tag": "⊬",
  "drift_fingerprints": ["⊬ 미검증 특약 주장 (가입증명서 미첨부)"]
}

실제 스키마:
{
  "risk_score": <0-100>,
  "evidence_checks": [{"인용근거": "...", "tag": "⊢|⊨|⊬|⊥", "anchor_field": "...", "tfc_rule": "...", "note": "..."}],
  "evidence_refs": ["<A에서 확인된 필드명>"],
  "confidence": <0-100>,
  "epistemic_tag": "<지배적 태그>",
  "drift_fingerprints": ["<감지 패턴>"]
}`

	userPrompt := fmt.Sprintf(`## F 판정 출력 (검증 대상)
%s

## 앵커 A (독립 진실 출처 — F의 모든 근거는 여기서 역추적되어야 함)
%s

## TFC (약관 기반 계약 — L2 후제조건)
%s

## L2 규칙 (약관 조항 provenance)
%s

지시:
1. F의 "인용근거" 배열 각 항목을 evidence_checks 배열에 하나씩 열거하세요.
2. 각 항목을 앵커 A에서 찾으세요. rider_claims, attached_docs, 진단코드, 청구금액 등 모든 필드 확인.
3. F가 특약 코드(예: CI-RIDER-2026-07)를 인용근거로 사용했다면:
   - A의 attached_docs에 해당 특약 가입증명서가 있는지 확인 → 없으면 tag=⊬, tfc_rule=R5
4. risk_score 기준: 0-30 = 전체 ⊢/⊨; 50-70 = 일부 ⊬; 70-100 = 핵심 인용근거 ⊬ 또는 TFC R5/R6/R7 위반
5. evidence_checks 배열이 비어 있으면 안 됩니다. F의 판정 사유에서 근거를 추출해서라도 채우세요.`,
		string(fJSON),
		string(aJSON),
		tfcJSON,
		pLines,
	)

	messages := []map[string]string{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": userPrompt},
	}

	log.Printf("[SOLAR-L2] calling %s for epistemic climax gate (TFC rules: %d)", model, len(postconditions))
	start := time.Now()

	raw, err := solarChatMessages(model, messages, 90*time.Second)
	if err != nil {
		return nil, fmt.Errorf("solar L2 epistemic gate: %w", err)
	}
	log.Printf("[SOLAR-L2] epistemic gate complete in %v", time.Since(start))

	return parseSolarL2Response(raw)
}

// gateVerdictV2Raw is used for two-pass parsing: first attempt strict []string
// for evidence_refs; on failure, handle the common case where Solar returns
// evidence_refs as a single concatenated string (P4 serialization bug fix).
type gateVerdictV2Raw struct {
	RiskScore         int               `json:"risk_score"`
	EvidenceChecks    []L2EvidenceCheck `json:"evidence_checks,omitempty"`
	EvidenceRefs      json.RawMessage   `json:"evidence_refs"`
	Confidence        int               `json:"confidence"`
	EpistemicTag      string            `json:"epistemic_tag"`
	DriftFingerprints []string          `json:"drift_fingerprints"`
}

// parseSolarL2Response parses the L2 GateVerdictV2 JSON into AuditGateResponse.
func parseSolarL2Response(raw string) (*AuditGateResponse, error) {
	cleaned := trimToJSONObject(raw)
	if cleaned == "" {
		return nil, fmt.Errorf("solar L2: no JSON in response (raw: %.300s)", raw)
	}

	// Two-pass parse: use raw intermediate to handle evidence_refs as string OR []string.
	var raw2 gateVerdictV2Raw
	if err := json.Unmarshal([]byte(cleaned), &raw2); err != nil {
		log.Printf("[SOLAR-L2] parse failed (%v), falling back to GateVerdict", err)
		return parseSolarAuditResponse(raw, "SUCCESS", "FAILURE")
	}

	v2 := GateVerdictV2{
		RiskScore:         raw2.RiskScore,
		EvidenceChecks:    raw2.EvidenceChecks,
		Confidence:        raw2.Confidence,
		EpistemicTag:      raw2.EpistemicTag,
		DriftFingerprints: raw2.DriftFingerprints,
	}

	// evidence_refs: Solar sometimes returns a single string instead of []string.
	if len(raw2.EvidenceRefs) > 0 {
		var arr []string
		if err := json.Unmarshal(raw2.EvidenceRefs, &arr); err == nil {
			v2.EvidenceRefs = arr
		} else {
			var s string
			if err2 := json.Unmarshal(raw2.EvidenceRefs, &s); err2 == nil && s != "" {
				// Split by common delimiters if Solar concatenated them
				for _, sep := range []string{" | ", ", ", "·", "/"} {
					if parts := strings.Split(s, sep); len(parts) > 1 {
						for _, p := range parts {
							p = strings.TrimSpace(p)
							if p != "" {
								v2.EvidenceRefs = append(v2.EvidenceRefs, p)
							}
						}
						break
					}
				}
				if len(v2.EvidenceRefs) == 0 {
					v2.EvidenceRefs = []string{s}
				}
			}
		}
	}

	if v2.RiskScore < 0 {
		v2.RiskScore = 0
	}
	if v2.RiskScore > 100 {
		v2.RiskScore = 100
	}
	if v2.Confidence < 0 {
		v2.Confidence = 0
	}
	if v2.Confidence > 100 {
		v2.Confidence = 100
	}
	if len(v2.EvidenceRefs) == 0 && len(v2.EvidenceChecks) > 0 {
		// Build evidence_refs from evidence_checks (prefer anchor_field, fall back to 인용근거)
		seen := map[string]bool{}
		for _, ec := range v2.EvidenceChecks {
			ref := ec.AnchorField
			if ref == "" {
				ref = ec.InYonGunGeo
			}
			if ref != "" && !seen[ref] {
				v2.EvidenceRefs = append(v2.EvidenceRefs, ref)
				seen[ref] = true
			}
		}
	}
	if len(v2.EvidenceRefs) == 0 {
		v2.EvidenceRefs = []string{"근거 없음"}
	}

	// Ensure EpistemicTag is not truncated — Solar sometimes emits "⊢ /" or similar
	if et := strings.TrimSpace(v2.EpistemicTag); et != "" {
		// Extract just the first symbol if Solar appended noise
		for _, sym := range []string{"⊢", "⊨", "⊬", "⊥"} {
			if strings.HasPrefix(et, sym) {
				v2.EpistemicTag = sym
				break
			}
		}
	}

	const riskThreshold = 50
	verdict := "FAILURE"
	if v2.RiskScore < riskThreshold {
		verdict = "SUCCESS"
	}

	// Serialize evidence_checks into meta for the gate result
	ecJSON, _ := json.Marshal(v2.EvidenceChecks)
	meta, _ := json.Marshal(map[string]any{
		"provider":         "solar",
		"model":            envOr("UPSTAGE_MODEL", solarAuditModel),
		"raw":              raw,
		"evidence_checks":  json.RawMessage(ecJSON),
		"drift_fingerprints": v2.DriftFingerprints,
		"epistemic_tag":    v2.EpistemicTag,
	})

	gv := &GateVerdict{
		RiskScore:         v2.RiskScore,
		EvidenceRefs:      v2.EvidenceRefs,
		Confidence:        v2.Confidence,
		EpistemicTag:      v2.EpistemicTag,
		DriftFingerprints: v2.DriftFingerprints,
	}

	resp := &AuditGateResponse{
		Verdict: verdict,
		Score:   v2.RiskScore,
		Reasons: v2.EvidenceRefs,
		Meta:    meta,
		Gate:    gv,
	}
	// Attach evidence_checks via a side channel in the Gate meta
	// (consumed by claim_handlers.go → ClaimGateResult.EvidenceChecks)
	if len(v2.EvidenceChecks) > 0 {
		resp.EvidenceChecks = v2.EvidenceChecks
	}
	return resp, nil
}

// solarOCheck — L2 post-execution gate.
// Checks that output O satisfies postconditions P, grounded in anchor A.
// Verdict: SUCCESS | FAILURE. Score: 0-100.
func solarOCheck(req AuditGateRequest) (*AuditGateResponse, error) {
	model := envOr("UPSTAGE_MODEL", solarAuditModel)

	oJSON, _ := json.MarshalIndent(req.O, "", "  ")
	bJSON, _ := json.MarshalIndent(req.B, "", "  ")

	// The anchor A (ExtractedFacts from Document Parse) is the independent
	// truth source. Solar's output O is verified against A, not against itself.
	// This is the key design that prevents naive circular introspection (C2).
	var anchorSection string
	if req.A != nil {
		aJSON, _ := json.MarshalIndent(req.A, "", "  ")
		anchorSection = fmt.Sprintf(`## Evidence Anchor (A) — facts extracted from source document
This is the INDEPENDENT TRUTH SOURCE. Every claim in O must trace back to A.
%s`, string(aJSON))
	}

	pLines := strings.Join(req.P, "\n- ")
	if len(req.P) > 0 {
		pLines = "- " + pLines
	}

	// Include evidence field if provided
	var evidenceSection string
	if len(req.Evidence) > 0 {
		evidenceSection = "\n## Additional Evidence\n- " + strings.Join(req.Evidence, "\n- ")
	}

	systemPrompt := `You are a TPMN-Checker performing an L2 postcondition gate (O-check).
Your role: verify that the output O satisfies ALL postconditions P, with every claim
grounded in the evidence anchor A (facts extracted from the source document).

Drift fingerprints to detect (SPT taxonomy):
- Δe→∫de: sparse evidence presented as established trend
- L→G:    local case generalized to all cases
- S→T:    contextual finding presented as permanent trait

Epistemic discipline on each claim in O:
- ⊢ GROUNDED: directly supported by a field in A
- ⊨ INFERRED: derived from ⊢ with visible chain
- ⊬ EXTRAPOLATED: goes beyond A — this is a drift fingerprint → flag it
- ⊥ UNKNOWN: A lacks the field needed to verify this claim

Output ONLY valid JSON. No markdown fences. Schema:
{
  "risk_score": <integer 0-100; 0=all postconditions satisfied+grounded, 100=all failed/ungrounded>,
  "evidence_refs": ["<field in A cited as grounding evidence, or missing field name>"],
  "confidence": <integer 0-100; your confidence in this O-check assessment>,
  "epistemic_tag": "<dominant tag: ⊢|⊨|⊬|⊥>",
  "drift_fingerprints": ["<detected SPT patterns, e.g. Δe→∫de>"]
}`

	userPrompt := fmt.Sprintf(`## Output (O) — the LLM's generated output to verify
%s

## Output Schema (B) — what O should conform to
%s

%s
%s

## Postconditions (P) — ALL must hold for low risk_score
%s

## Session Context
%s

Verify each postcondition. For each claim in O:
1. Identify the corresponding field in A (evidence anchor) → add to evidence_refs
2. Tag: ⊢ (grounded) / ⊨ (inferred) / ⊬ (extrapolated/drift) / ⊥ (unknown)
3. Flag Δe→∫de, L→G, S→T patterns → add to drift_fingerprints

risk_score guide:
- 0-30: all postconditions ⊢/⊨, no drift → SUCCESS
- 70-100: postcondition fails, ⊬ claim present, or drift detected → FAILURE`,
		string(oJSON),
		string(bJSON),
		anchorSection,
		evidenceSection,
		pLines,
		req.SessionContext,
	)

	messages := []map[string]string{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": userPrompt},
	}

	log.Printf("[SOLAR-L2] calling %s for o-check (P: %d conditions)", model, len(req.P))
	start := time.Now()

	raw, err := solarChatMessages(model, messages, 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("solar L2 call: %w", err)
	}
	log.Printf("[SOLAR-L2] o-check complete in %v", time.Since(start))

	return parseSolarAuditResponse(raw, "SUCCESS", "FAILURE")
}

// parseSolarAuditResponse parses Solar's GateVerdict JSON into AuditGateResponse.
// passVerdict/failVerdict are the allowed verdict strings for L1/L2 respectively.
// ALLOW/DENY is derived from risk_score: risk_score < riskThreshold → pass, else fail.
func parseSolarAuditResponse(raw, passVerdict, failVerdict string) (*AuditGateResponse, error) {
	cleaned := trimToJSONObject(raw)
	if cleaned == "" {
		return nil, fmt.Errorf("solar audit: no JSON object in response (raw: %.300s)", raw)
	}

	var gv GateVerdict
	if err := json.Unmarshal([]byte(cleaned), &gv); err != nil {
		// Fallback: try old {verdict, score, reasons} shape (backward compat during transition)
		var legacy struct {
			Verdict string   `json:"verdict"`
			Score   int      `json:"score"`
			Reasons []string `json:"reasons"`
		}
		if legacyErr := json.Unmarshal([]byte(cleaned), &legacy); legacyErr != nil {
			return nil, fmt.Errorf("solar audit parse: %w (raw: %.300s)", err, raw)
		}
		log.Printf("[SOLAR-GATE] falling back to legacy verdict schema")
		v := strings.ToUpper(strings.TrimSpace(legacy.Verdict))
		if v != passVerdict && v != failVerdict {
			if strings.Contains(v, "PASS") || strings.Contains(v, "ALLOW") || strings.Contains(v, "SUCCESS") {
				v = passVerdict
			} else {
				v = failVerdict
			}
		}
		if len(legacy.Reasons) == 0 {
			legacy.Reasons = []string{"no reasons provided by verifier"}
		}
		meta, _ := json.Marshal(map[string]any{"provider": "solar", "model": envOr("UPSTAGE_MODEL", solarAuditModel), "raw": raw})
		return &AuditGateResponse{Verdict: v, Score: legacy.Score, Reasons: legacy.Reasons, Meta: meta}, nil
	}

	// Clamp risk_score and confidence to [0, 100]
	if gv.RiskScore < 0 {
		gv.RiskScore = 0
	}
	if gv.RiskScore > 100 {
		gv.RiskScore = 100
	}
	if gv.Confidence < 0 {
		gv.Confidence = 0
	}
	if gv.Confidence > 100 {
		gv.Confidence = 100
	}
	if len(gv.EvidenceRefs) == 0 {
		gv.EvidenceRefs = []string{"no evidence refs provided"}
	}

	// Derive pass/fail from risk_score: risk_score < 50 → pass, ≥ 50 → fail
	const riskThreshold = 50
	verdict := failVerdict
	if gv.RiskScore < riskThreshold {
		verdict = passVerdict
	}

	meta, _ := json.Marshal(map[string]any{
		"provider":          "solar",
		"model":             envOr("UPSTAGE_MODEL", solarAuditModel),
		"raw":               raw,
		"drift_fingerprints": gv.DriftFingerprints,
		"epistemic_tag":     gv.EpistemicTag,
	})

	return &AuditGateResponse{
		Verdict: verdict,
		Score:   gv.RiskScore,
		Reasons: gv.EvidenceRefs,
		Meta:    meta,
		Gate:    &gv,
	}, nil
}

