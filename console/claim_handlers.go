package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// ── POST /api/claim/process ───────────────────────────────────────────────
//
// Unified Korean insurance claim processing endpoint.
// Accepts multipart/form-data with:
//   - pdf: claim PDF file (max 10MB)
//   - verify: "true" (default) | "false" — OFF/ON toggle for L0→L1→F→L2→L3
//
// Pipeline:
//   (1) Pre-flight classification → early-block if not 보험금청구서
//   (2) Document Parse + Information Extract → anchor A (ExtractedFacts)
//   (3a) verify=true:  run L0→L1→F(Solar)→L2→L3 per-node loop
//   (3b) verify=false: run Solar F judgment only (no gates)
//
// Returns ClaimProcessResponse JSON.

const claimPDFMaxBytes = 10 << 20 // 10 MB

// ClaimProcessResponse is the top-level response from POST /api/claim/process.
type ClaimProcessResponse struct {
	Status     string         `json:"status"` // ok | blocked | error
	DocClass   string         `json:"doc_class,omitempty"`
	AnchorA    map[string]any `json:"anchor_a,omitempty"`
	JudgmentB  map[string]any `json:"judgment_b,omitempty"`
	ClaimGateResult *ClaimGateResult    `json:"gate_result,omitempty"`
	AuditLogID string         `json:"audit_log_id,omitempty"`
	DurationMs int64          `json:"duration_ms"`
	Error      string         `json:"error,omitempty"`
}

// ClaimGateResult surfaces the Solar verification output for the UI.
type ClaimGateResult struct {
	RiskScore         int               `json:"risk_score"`
	EvidenceRefs      []string          `json:"evidence_refs"`
	Confidence        int               `json:"confidence"`
	EpistemicTag      string            `json:"epistemic_tag"`
	DriftFingerprints []string          `json:"drift_fingerprints"`
	L1Verdict         string            `json:"l1_verdict"`
	L2Verdict         string            `json:"l2_verdict"`
	VerifyMode        string            `json:"verify_mode"` // "on" | "off"
	// TFC is the contract produced by the TCE node — shown in the UI with provenance badges.
	TFC               *TFCForUI         `json:"tfc,omitempty"`
	// EvidenceChecks are the per-인용근거 epistemic audit results from L2.
	EvidenceChecks    []L2EvidenceCheck `json:"evidence_checks,omitempty"`
}

// TFCForUI is a simplified view of TFC for the frontend.
type TFCForUI struct {
	Category string         `json:"category"`
	Version  string         `json:"version"`
	Rules    []ContractRule `json:"rules"`
}

func handleClaimProcess(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	w.Header().Set("Content-Type", "application/json")

	// Parse multipart form (10 MB limit)
	if err := r.ParseMultipartForm(claimPDFMaxBytes); err != nil {
		writeClaimError(w, http.StatusBadRequest, "multipart_parse_error", err.Error(), start)
		return
	}

	// Read PDF bytes
	file, _, err := r.FormFile("pdf")
	if err != nil {
		writeClaimError(w, http.StatusBadRequest, "missing_pdf", "pdf field required", start)
		return
	}
	defer file.Close()

	pdfBytes, err := io.ReadAll(io.LimitReader(file, claimPDFMaxBytes+1))
	if err != nil {
		writeClaimError(w, http.StatusBadRequest, "read_pdf_error", err.Error(), start)
		return
	}
	if len(pdfBytes) > claimPDFMaxBytes {
		writeClaimError(w, http.StatusBadRequest, "pdf_too_large", "PDF exceeds 10MB limit", start)
		return
	}

	// Parse verify toggle (default true)
	verifyStr := r.FormValue("verify")
	verify := verifyStr != "false"
	verifyMode := "on"
	if !verify {
		verifyMode = "off"
	}

	// Bound the whole pipeline at 150s — returns audit_pending on timeout.
	ctx, cancelPipeline := context.WithTimeout(r.Context(), 150*time.Second)
	defer cancelPipeline()

	// ── Step 1: Pre-flight classification ────────────────────────────
	docClass, earlyBlock, cerr := ClassifyAndRoute(pdfBytes)
	if cerr != nil {
		log.Printf("[CLAIM] classification error: %v", cerr)
		// Non-fatal: proceed as 보험금청구서 if classification fails
		docClass = "보험금청구서"
		earlyBlock = nil
	}
	if earlyBlock != nil {
		earlyBlock["duration_ms"] = time.Since(start).Milliseconds()
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(earlyBlock)
		return
	}

	// ── Step 2: Parse PDF + extract anchor A ─────────────────────────
	facts, err := IngestClaim(pdfBytes)
	if err != nil {
		log.Printf("[CLAIM] ingest error: %v", err)
		writeClaimError(w, http.StatusInternalServerError, "ingest_error", err.Error(), start)
		return
	}
	anchorA := FactsToAnchorMap(facts)

	// ── Step 2b: TCE — pull template + bind instance values → TFC ────
	tfc, tceErr := RunTCE(docClass, anchorA)
	if tceErr != nil {
		log.Printf("[CLAIM] TCE error (continuing with nil TFC): %v", tceErr)
	}

	// ── Step 3: Solar judgment (F) ± verification (L0→L1→F→L2→L3) ──
	var judgmentB map[string]any
	var gateResult *ClaimGateResult

	if verify {
		judgmentB, gateResult, err = runClaimWithVerification(ctx, anchorA, facts, tfc)
	} else {
		judgmentB, err = runClaimFOnly(ctx, anchorA)
		gateResult = &ClaimGateResult{VerifyMode: verifyMode}
	}
	if gateResult != nil {
		gateResult.VerifyMode = verifyMode
		// Always attach TFC to the gate result (shown in UI even for verify=off)
		if tfc != nil {
			gateResult.TFC = &TFCForUI{
				Category: tfc.Category,
				Version:  tfc.Version,
				Rules:    tfc.Rules,
			}
		}
	}

	if err != nil {
		// Context timeout → return audit_pending (not a hard error)
		if ctx.Err() != nil {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":      "audit_pending",
				"error":       "Solar 응답 대기 중 — 잠시 후 다시 시도하세요",
				"duration_ms": time.Since(start).Milliseconds(),
			})
			return
		}
		log.Printf("[CLAIM] judgment error: %v", err)
		writeClaimError(w, http.StatusInternalServerError, "judgment_error", err.Error(), start)
		return
	}

	resp := ClaimProcessResponse{
		Status:     "ok",
		DocClass:   docClass,
		AnchorA:    anchorA,
		JudgmentB:  judgmentB,
		ClaimGateResult: gateResult,
		DurationMs: time.Since(start).Milliseconds(),
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// runClaimFOnly calls Solar F directly — competent grounding-based judgment (G1).
// No L0/L1/L2/L3 gates — used when verify=false.
// F prompt: judge based on ALL of anchor A (including rider_claims), not arithmetic.
// The validity of rider claims is NOT checked here — that is L2's job.
func runClaimFOnly(ctx context.Context, anchorA map[string]any) (map[string]any, error) {
	model := envOr("UPSTAGE_MODEL", solarAuditModel)

	systemPrompt := `당신은 한국 보험금 청구 1차 심사 담당자(fact adjudicator)입니다.
역할 분담: 당신은 청구서 사실(A)을 읽고 판정합니다. 약관 컴플라이언스 집행(특약 첨부 요건,
가입증명서 확인 등)은 별도 L1/L2 검증 게이트의 역할입니다 — 당신의 역할이 아닙니다.

판정 원칙:
1. A의 구조화 필드(피보험자명, 진단코드, 청구항목, 청구금액, 약관조항ref 등)를 기반으로 판정합니다.
2. rider_claims 배열에 특약 코드가 있으면, 청구인이 그 특약을 통한 추가 보장을 주장하는 것입니다.
   특약이 기본 약관한도를 초과하는 항목을 커버한다고 청구인이 주장하면, 그 주장을 판정에 반영하세요.
   (특약의 가입 여부, 가입증명서 첨부 여부, 약관상 유효성은 당신이 검증할 사항이 아닙니다.)
3. 판정 근거로 사용한 A의 필드값과 rider_claims 항목을 인용근거 배열에 직접 인용하세요.
4. A에 없는 사실을 인용하거나 추측하지 마세요.
5. 청구금액이 기본 약관한도를 초과하더라도, rider_claims에 해당 초과분을 커버하는 특약이 있다면
   특약을 인용근거로 삼아 전액 또는 특약 보장 범위까지 승인하세요.

JSON만 출력하세요. 마크다운 코드 블록 없음. 스키마:
{
  "판정": "승인 | 거절 | 보류",
  "사유": "판정 이유 (A의 사실만 근거, 200자 이내)",
  "인용근거": ["A 필드값 또는 rider_claims 항목 직접 인용"],
  "승인금액": 0
}`

	iJSON, _ := json.MarshalIndent(anchorA, "", "  ")
	userPrompt := fmt.Sprintf("## 앵커 A (보험금 청구 추출 사실)\n\n%s\n\n판정 결과를 JSON으로 출력하세요.", string(iJSON))

	messages := []map[string]string{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": userPrompt},
	}

	log.Printf("[SOLAR-F] calling %s for claim judgment", model)
	start := time.Now()
	raw, err := solarChatMessages(model, messages, 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("solar F call: %w", err)
	}
	log.Printf("[SOLAR-F] judgment complete in %v", time.Since(start))

	stripped := stripCodeFencesForJSON(raw)
	var out map[string]any
	if err := json.Unmarshal([]byte(stripped), &out); err != nil {
		return nil, fmt.Errorf("parse Solar F output: %w (raw: %.300s)", err, raw)
	}
	return out, nil
}

// runClaimWithVerification runs L0 → L1(TFC light gate) → F → L2(TFC epistemic climax) → L3.
// tfc may be nil (if TCE failed) — falls back to CE spec p_pre/p_post in that case.
func runClaimWithVerification(ctx context.Context, anchorA map[string]any, facts *ExtractedFacts, tfc *TFC) (map[string]any, *ClaimGateResult, error) {
	gr := &ClaimGateResult{
		DriftFingerprints: []string{},
		EvidenceRefs:      []string{},
	}

	// ── L0: LobsterTrap ingress — inspect raw PDF text ───────────────
	if facts.RawText != "" {
		l0 := ltInspect(facts.RawText)
		if l0.Verdict == "DENY" {
			gr.L1Verdict = "SKIPPED"
			gr.L2Verdict = "SKIPPED"
			gr.RiskScore = 100
			gr.EvidenceRefs = []string{l0.MatchedRule}
			gr.DriftFingerprints = l0.Flags
			return nil, gr, nil
		}
	}

	// ── L1: Lightweight precondition gate using TFC rules ────────────
	if tfc != nil {
		l1Resp, l1Err := solarL1LightGate(anchorA, tfc)
		if l1Err != nil {
			log.Printf("[CLAIM] L1 light gate error (continuing): %v", l1Err)
			gr.L1Verdict = "ERROR"
		} else {
			gr.L1Verdict = l1Resp.Verdict
			if l1Resp.Gate != nil {
				gr.RiskScore = l1Resp.Gate.RiskScore
				gr.EvidenceRefs = append(gr.EvidenceRefs, l1Resp.Gate.EvidenceRefs...)
				gr.Confidence = l1Resp.Gate.Confidence
				gr.EpistemicTag = l1Resp.Gate.EpistemicTag
			}
			if l1Resp.Verdict == "DENY" {
				return nil, gr, nil
			}
		}
	} else {
		// Fallback to CE spec p_pre when TCE unavailable
		spec, specErr := loadCESpec("korean-insurance-claim-pipeline", "insurance-adjudication")
		if specErr == nil {
			l1Req := AuditGateRequest{
				I: anchorA, A: spec.A, P: spec.PPre,
				Provider: defaultAuditProvider(), T: spec.TrustGateL1,
			}
			l1Resp, l1Err := callPCheck(ctx, l1Req)
			if l1Err != nil {
				log.Printf("[CLAIM] L1 fallback error (continuing): %v", l1Err)
				gr.L1Verdict = "ERROR"
			} else {
				gr.L1Verdict = l1Resp.Verdict
				if l1Resp.Gate != nil {
					gr.RiskScore = l1Resp.Gate.RiskScore
					gr.EvidenceRefs = append(gr.EvidenceRefs, l1Resp.Gate.EvidenceRefs...)
				}
				if l1Resp.Verdict == "DENY" {
					return nil, gr, nil
				}
			}
		} else {
			gr.L1Verdict = "ERROR"
		}
	}

	// ── F: Solar judgment — competent, grounded in A ──────────────────
	judgmentB, err := runClaimFOnly(ctx, anchorA)
	if err != nil {
		return nil, gr, err
	}

	// ── L2: Epistemic climax — {TFC + A + F} per-인용근거 audit ───────
	if tfc != nil {
		l2Resp, l2Err := solarL2EpistemicGate(tfc, anchorA, judgmentB)
		if l2Err != nil {
			log.Printf("[CLAIM] L2 epistemic gate error (continuing): %v", l2Err)
			gr.L2Verdict = "ERROR"
		} else {
			gr.L2Verdict = l2Resp.Verdict
			if l2Resp.Gate != nil {
				if l2Resp.Gate.RiskScore > gr.RiskScore {
					gr.RiskScore = l2Resp.Gate.RiskScore
				}
				gr.EvidenceRefs = dedupeStrings(append(gr.EvidenceRefs, l2Resp.Gate.EvidenceRefs...))
				gr.DriftFingerprints = dedupeStrings(append(gr.DriftFingerprints, l2Resp.Gate.DriftFingerprints...))
				if l2Resp.Gate.EpistemicTag != "" && l2Resp.Gate.EpistemicTag != "⊢" {
					gr.EpistemicTag = l2Resp.Gate.EpistemicTag
				}
				if l2Resp.Gate.Confidence > gr.Confidence {
					gr.Confidence = l2Resp.Gate.Confidence
				}
			}
			if len(l2Resp.EvidenceChecks) > 0 {
				gr.EvidenceChecks = l2Resp.EvidenceChecks
			}
		}
	} else {
		// Fallback L2 using CE spec
		spec, specErr := loadCESpec("korean-insurance-claim-pipeline", "insurance-adjudication")
		if specErr == nil {
			oJSON, _ := json.Marshal(judgmentB)
			aJSON, _ := json.Marshal(anchorA)
			l2Req := AuditGateRequest{
				O: string(oJSON), B: spec.B, A: string(aJSON), P: spec.PPost,
				Provider: defaultAuditProvider(), T: spec.TrustGateL2,
			}
			l2Resp, l2Err := callOCheck(ctx, l2Req)
			if l2Err != nil {
				log.Printf("[CLAIM] L2 fallback error (continuing): %v", l2Err)
				gr.L2Verdict = "ERROR"
			} else {
				gr.L2Verdict = l2Resp.Verdict
				if l2Resp.Gate != nil {
					if l2Resp.Gate.RiskScore > gr.RiskScore {
						gr.RiskScore = l2Resp.Gate.RiskScore
					}
					gr.EvidenceRefs = dedupeStrings(append(gr.EvidenceRefs, l2Resp.Gate.EvidenceRefs...))
					gr.DriftFingerprints = dedupeStrings(append(gr.DriftFingerprints, l2Resp.Gate.DriftFingerprints...))
					if l2Resp.Gate.EpistemicTag != "" {
						gr.EpistemicTag = l2Resp.Gate.EpistemicTag
					}
				}
			}
		} else {
			gr.L2Verdict = "ERROR"
		}
	}

	return judgmentB, gr, nil
}

// handleClaimScenarios returns the 3 Korean demo scenarios with pre-loaded PDF bytes.
func handleClaimScenarios(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"scenarios": koreanClaimScenarios,
	})
}

// handleTFCTemplate returns the current Contract DB template + HITL additions.
// GET /api/claim/tfc-template?category=보험금청구서
func handleTFCTemplate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	category := r.URL.Query().Get("category")
	if category == "" {
		category = "보험금청구서"
	}
	tmpl := GetContractTemplate(category)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"template": tmpl,
		"hitl_rules": GetHITLRules(),
	})
}

// handleHITLAddRule adds a rule to the TFC template (4막 HITL seed demo).
// POST /api/claim/hitl/add-rule  body: {"description":"...", "provenance":"...", "gate":"L2"}
func handleHITLAddRule(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req struct {
		Description string `json:"description"`
		Provenance  string `json:"provenance"`
		Gate        string `json:"gate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Description == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "description required"})
		return
	}
	rule := AddHITLRule(req.Description, req.Provenance, req.Gate)
	log.Printf("[HITL] added rule %s: %s (%s)", rule.ID, rule.Description, rule.Provenance)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"added": rule,
		"total_hitl_rules": len(GetHITLRules()),
	})
}

// handleHITLReset clears all HITL-added rules (demo reset).
// POST /api/claim/hitl/reset
func handleHITLReset(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	ClearHITLRules()
	_ = json.NewEncoder(w).Encode(map[string]any{"reset": true})
}

func writeClaimError(w http.ResponseWriter, status int, code, detail string, start time.Time) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":      "error",
		"error":       code,
		"detail":      detail,
		"duration_ms": time.Since(start).Milliseconds(),
	})
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
