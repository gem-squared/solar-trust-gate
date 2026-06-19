package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ExtractedFacts is anchor A — the independent truth extracted from a claim PDF
// by Upstage Document Parse + Information Extract. All downstream Solar judgments
// must ground every conclusion in this struct; no facts outside it are admissible.
type ExtractedFacts struct {
	InsuredName   string         `json:"피보험자명"`
	DiagnosisCode string         `json:"진단코드"`
	ClaimItems    []string       `json:"청구항목"`
	ClaimedAmount int64          `json:"청구금액"`
	PolicyLimit   int64          `json:"약관한도"`
	PolicyRef     string         `json:"약관조항ref"`
	EvidenceSpans []EvidenceSpan `json:"evidence_spans,omitempty"`
	// RiderClaims captures any special rider/특약 codes asserted in the document
	// (structured OR narrative). Key for the 2막 demo: CI-RIDER-2026-07 appears
	// only in the narrative section, not structured fields.
	RiderClaims   []string       `json:"rider_claims,omitempty"`
	// AttachedDocs lists the supporting documents attached to this claim.
	// TFC Rule R5 checks whether 특약 가입증명서 is present here.
	AttachedDocs  []string       `json:"attached_docs,omitempty"`
	RawText       string         `json:"raw_text,omitempty"`
}

// EvidenceSpan links an extracted field to its source location in the document.
type EvidenceSpan struct {
	Field string `json:"field"`
	Text  string `json:"text"`
	Page  int    `json:"page,omitempty"`
}

var sampleSaveMu sync.Mutex

// parseDocument calls Upstage /v1/document-digitization (document-parse model)
// using multipart/form-data upload. Returns the markdown text of the document.
// Saves the raw response to Doc/api-samples/upstage_document_parse.json on first call.
func parseDocument(pdfBytes []byte) (string, error) {
	apiKey := solarAPIKey()
	if apiKey == "" {
		return "", fmt.Errorf("UPSTAGE_API_KEY not set")
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("model", "document-parse")
	part, err := writer.CreateFormFile("document", "claim.pdf")
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(part, bytes.NewReader(pdfBytes)); err != nil {
		return "", fmt.Errorf("write pdf bytes: %w", err)
	}
	writer.Close()

	url := upstageDocAIBase() + "/document-digitization"
	req, err := http.NewRequest("POST", url, &body)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("document-parse request: %w", err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("document-parse HTTP %d: %s", resp.StatusCode, string(respBytes))
	}

	saveSampleOnce("upstage_document_parse.json", respBytes)

	var result map[string]any
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return "", fmt.Errorf("parse response JSON: %w", err)
	}

	// Extract markdown text from content field (Upstage Document Parse response shape)
	if content, ok := result["content"].(map[string]any); ok {
		if md, ok := content["markdown"].(string); ok && md != "" {
			return md, nil
		}
		if txt, ok := content["text"].(string); ok && txt != "" {
			return txt, nil
		}
	}
	// Fallback: serialize whole result as text
	raw, _ := json.Marshal(result)
	return string(raw), nil
}

// extractFacts calls Upstage /v1/information-extraction with a Korean insurance schema.
// Returns structured anchor A (ExtractedFacts).
// Saves the raw response to Doc/api-samples/upstage_information_extraction.json on first call.
func extractFacts(parsedText string) (*ExtractedFacts, error) {
	apiKey := solarAPIKey()
	if apiKey == "" {
		return nil, fmt.Errorf("UPSTAGE_API_KEY not set")
	}

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"피보험자명": map[string]any{
				"type":        "string",
				"description": "피보험자(보험 가입자)의 이름",
			},
			"진단코드": map[string]any{
				"type":        "string",
				"description": "ICD-10 진단 코드 (예: J18.1)",
			},
			"청구항목": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
				"description": "청구된 의료 항목 목록 (예: 입원료, 수술비, 약제비)",
			},
			"청구금액": map[string]any{
				"type":        "integer",
				"description": "총 청구 금액 (원 단위 정수)",
			},
			"약관한도": map[string]any{
				"type":        "integer",
				"description": "보험 약관상 지급 한도 금액 (원 단위 정수)",
			},
			"약관조항ref": map[string]any{
				"type":        "string",
				"description": "지급 근거가 되는 약관 조항 번호 또는 명칭",
			},
			"rider_claims": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
				"description": "문서에 언급된 특약(rider) 코드 또는 특약명 목록 (청구 경위 서술 포함하여 추출)",
			},
			"attached_docs": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
				"description": "첨부된 서류 목록 (체크된 항목만 포함, 미첨부 항목 제외)",
			},
			"evidence_spans": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"field": map[string]any{"type": "string"},
						"text":  map[string]any{"type": "string"},
						"page":  map[string]any{"type": "integer"},
					},
				},
				"description": "각 추출 필드의 원문 근거 (필드명·원문텍스트·페이지번호)",
			},
		},
		"required": []string{"피보험자명", "진단코드", "청구항목", "청구금액", "약관한도", "약관조항ref"},
	}

	reqBody := map[string]any{
		"model": "information-extract",
		"messages": []map[string]any{
			{
				"role":    "user",
				"content": parsedText,
			},
		},
		"schema": schema,
	}
	data, _ := json.Marshal(reqBody)

	url := upstageIEEndpoint()
	req, err := http.NewRequest("POST", url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("build ie request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("information-extraction request: %w", err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		// If dedicated IE endpoint fails, fall back to Solar chat extraction
		log.Printf("[INGEST] /information-extraction HTTP %d — falling back to solar-chat extraction", resp.StatusCode)
		saveSampleOnce("upstage_information_extraction_error.json", respBytes)
		return extractFactsViaSolar(parsedText)
	}

	saveSampleOnce("upstage_information_extraction.json", respBytes)
	return parseIEResponse(respBytes, parsedText)
}

// extractFactsViaSolar is the fallback: uses Solar Pro 3 chat completions
// to extract structured anchor A from parsed text when the dedicated IE endpoint
// is unavailable.
func extractFactsViaSolar(parsedText string) (*ExtractedFacts, error) {
	systemPrompt := `당신은 한국 보험금 청구서에서 핵심 사실을 추출하는 전문가입니다.
다음 보험금 청구서 텍스트에서 아래 필드를 추출하여 JSON 형식으로만 응답하세요.

출력 JSON 스키마:
{
  "피보험자명": "string — 피보험자 이름",
  "진단코드": "string — ICD-10 코드 (예: J18.1)",
  "청구항목": ["string"] — 청구된 의료 항목 목록,
  "청구금액": integer — 총 청구 금액 (원),
  "약관한도": integer — 약관상 지급 한도 (원),
  "약관조항ref": "string — 지급 근거 약관 조항",
  "rider_claims": ["string"] — 문서에 언급된 특약 코드나 특약명 목록 (청구 경위 서술 포함, 없으면 빈 배열),
  "attached_docs": ["string"] — 실제 첨부된 서류 목록 (■ 체크된 항목만, □ 미첨부 항목 제외),
  "evidence_spans": [{"field":"string","text":"string","page":integer}]
}

규칙: JSON만 출력하고, 추측 금지, 문서에 없는 값은 빈 문자열/0/빈 배열로 표시.`

	raw, err := solarChatMessages(
		envOr("UPSTAGE_MODEL", solarAuditModel),
		[]map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": parsedText},
		},
		60*time.Second,
	)
	if err != nil {
		return nil, fmt.Errorf("solar extraction: %w", err)
	}

	stripped := stripCodeFencesForJSON(raw)
	var facts ExtractedFacts
	if err := json.Unmarshal([]byte(stripped), &facts); err != nil {
		return nil, fmt.Errorf("parse solar extraction JSON: %w", err)
	}
	return &facts, nil
}

// parseIEResponse parses the Upstage information-extraction API response
// and maps it to ExtractedFacts. Handles two possible response shapes:
// (a) chat-completions envelope with choices[].message.content (JSON string)
// (b) direct object with extracted fields at top level
func parseIEResponse(respBytes []byte, rawText string) (*ExtractedFacts, error) {
	var result map[string]any
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("parse IE response: %w", err)
	}

	// Shape (a): chat completions envelope
	if choices, ok := result["choices"].([]any); ok && len(choices) > 0 {
		if msg, ok := choices[0].(map[string]any)["message"].(map[string]any); ok {
			if content, ok := msg["content"].(string); ok {
				stripped := stripCodeFencesForJSON(content)
				var facts ExtractedFacts
				if err := json.Unmarshal([]byte(stripped), &facts); err != nil {
					return nil, fmt.Errorf("parse IE content JSON: %w", err)
				}
				facts.RawText = rawText
				return &facts, nil
			}
		}
	}

	// Shape (b): direct object — marshal back and unmarshal into facts
	data, _ := json.Marshal(result)
	var facts ExtractedFacts
	if err := json.Unmarshal(data, &facts); err != nil {
		return nil, fmt.Errorf("parse IE direct object: %w", err)
	}
	facts.RawText = rawText
	return &facts, nil
}

// ─── Pre-flight Document Classification + Routing ──────────────────────────

// DocClassification holds the result of Upstage document classification.
type DocClassification struct {
	Category   string  `json:"category"`
	Confidence float64 `json:"confidence"`
}

// koreanClaimCategories defines the 4 custom categories for claim routing.
var koreanClaimCategories = []map[string]string{
	{"name": "보험금청구서", "description": "건강보험 청구 문서"},
	{"name": "진단서", "description": "의사 발급 진단 문서"},
	{"name": "약관", "description": "보험 약관 문서"},
	{"name": "기타", "description": "그 외 문서"},
}

// classifyDocument calls Upstage document classification with Korean insurance categories.
// Returns the top predicted category name and confidence score.
// Saves the raw response to Doc/api-samples/upstage_classification.json on first call.
func classifyDocument(pdfBytes []byte) (string, float64, error) {
	apiKey := solarAPIKey()
	if apiKey == "" {
		return "", 0, fmt.Errorf("UPSTAGE_API_KEY not set")
	}

	catJSON, _ := json.Marshal(koreanClaimCategories)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("model", "document-classification")
	_ = writer.WriteField("categories", string(catJSON))
	part, err := writer.CreateFormFile("document", "claim.pdf")
	if err != nil {
		return "", 0, fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(part, bytes.NewReader(pdfBytes)); err != nil {
		return "", 0, fmt.Errorf("write pdf bytes: %w", err)
	}
	writer.Close()

	url := upstageDocAIBase() + "/document-classification"
	req, err := http.NewRequest("POST", url, &body)
	if err != nil {
		return "", 0, fmt.Errorf("build classify request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("classification request: %w", err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", 0, fmt.Errorf("classification HTTP %d: %s", resp.StatusCode, string(respBytes))
	}

	saveSampleOnce("upstage_classification.json", respBytes)

	var result map[string]any
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return "", 0, fmt.Errorf("parse classification response: %w", err)
	}

	// Upstage classification response: {model, prediction: [{category, confidence}]}
	if preds, ok := result["prediction"].([]any); ok && len(preds) > 0 {
		if top, ok := preds[0].(map[string]any); ok {
			cat, _ := top["category"].(string)
			conf, _ := top["confidence"].(float64)
			return cat, conf, nil
		}
	}
	// Fallback: look for output/result field shapes
	if output, ok := result["output"].(map[string]any); ok {
		if cat, ok := output["category"].(string); ok {
			conf, _ := output["confidence"].(float64)
			return cat, conf, nil
		}
	}
	return "기타", 0.0, nil
}

// ClassifyAndRoute classifies the PDF and returns either the category (if it's a
// 보험금청구서) or an error map suitable for early-blocking non-claim documents.
// Returns (docClass, nil) when the document should proceed; (docClass, earlyBlock) when blocked.
func ClassifyAndRoute(pdfBytes []byte) (docClass string, earlyBlock map[string]any, err error) {
	cat, conf, cerr := classifyDocument(pdfBytes)
	if cerr != nil {
		log.Printf("[INGEST] classification error (allowing through): %v", cerr)
		// If classification fails, allow through so the pipeline doesn't break
		return "보험금청구서", nil, nil
	}
	if cat == "보험금청구서" && conf >= 0.5 {
		return cat, nil, nil
	}
	// Block non-claim documents
	return cat, map[string]any{
		"status":     "blocked",
		"reason":     "보험금 청구서가 아닙니다",
		"doc_class":  cat,
		"confidence": conf,
	}, nil
}

// upstageDocAIBase returns the Upstage Document AI endpoint base.
// Upstage document digitization and classification use the same /v1 base as chat.
func upstageDocAIBase() string {
	if v := os.Getenv("UPSTAGE_DOCAI_BASE"); v != "" {
		return v
	}
	return solarBaseURL // https://api.upstage.ai/v1
}

// ─── Claim Ingest Orchestrator ──────────────────────────────────────────────

// IngestClaim orchestrates parse → extract for a claim PDF.
// Returns anchor A (ExtractedFacts) for downstream Solar judgment.
func IngestClaim(pdfBytes []byte) (*ExtractedFacts, error) {
	parsedText, err := parseDocument(pdfBytes)
	if err != nil {
		return nil, fmt.Errorf("document parse: %w", err)
	}
	facts, err := extractFacts(parsedText)
	if err != nil {
		return nil, fmt.Errorf("information extract: %w", err)
	}
	facts.RawText = parsedText
	return facts, nil
}

// saveSampleOnce writes raw API response bytes to Doc/api-samples/<name> exactly once
// per process lifetime (subsequent calls for the same name are no-ops).
var savedSamples = map[string]bool{}

func saveSampleOnce(name string, data []byte) {
	sampleSaveMu.Lock()
	defer sampleSaveMu.Unlock()
	if savedSamples[name] {
		return
	}
	dir := filepath.Join("Doc", "api-samples")
	_ = os.MkdirAll(dir, 0755)
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		savedSamples[name] = true
		return // already exists from a prior run
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, data, "", "  "); err != nil {
		_ = os.WriteFile(path, data, 0644)
	} else {
		_ = os.WriteFile(path, pretty.Bytes(), 0644)
	}
	savedSamples[name] = true
	log.Printf("[INGEST] saved API sample: %s", path)
}

// FactsToAnchorMap converts ExtractedFacts to the map[string]any anchor A
// used as input to the CE runtime and Solar judgment prompts.
func FactsToAnchorMap(f *ExtractedFacts) map[string]any {
	items := make([]any, len(f.ClaimItems))
	for i, it := range f.ClaimItems {
		items[i] = it
	}
	spans := make([]any, len(f.EvidenceSpans))
	for i, sp := range f.EvidenceSpans {
		spans[i] = map[string]any{
			"field": sp.Field,
			"text":  sp.Text,
			"page":  sp.Page,
		}
	}
	riderClaims := make([]any, len(f.RiderClaims))
	for i, r := range f.RiderClaims {
		riderClaims[i] = r
	}
	attachedDocs := make([]any, len(f.AttachedDocs))
	for i, d := range f.AttachedDocs {
		attachedDocs[i] = d
	}
	return map[string]any{
		"피보험자명":        f.InsuredName,
		"진단코드":         f.DiagnosisCode,
		"청구항목":         items,
		"청구금액":         f.ClaimedAmount,
		"약관한도":         f.PolicyLimit,
		"약관조항ref":      f.PolicyRef,
		"rider_claims":   riderClaims,
		"attached_docs":  attachedDocs,
		"evidence_spans": spans,
	}
}

// upstageIEEndpoint returns the resolved information-extraction endpoint.
func upstageIEEndpoint() string {
	if v := os.Getenv("UPSTAGE_IE_ENDPOINT"); v != "" {
		return v
	}
	return solarBaseURL + "/information-extraction"
}
