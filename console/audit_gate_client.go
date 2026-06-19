package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// ── Audit-Gate Client ──────────────────────────────────────────────
//
// Wraps the gem2-tpmn-checker SaaS endpoints:
//   POST /api/audit-gate/p-check  → L1 pre-execution gate (ALLOW|DENY + score)
//   POST /api/audit-gate/o-check  → L2 post-execution gate (SUCCESS|FAILURE + score)
//
// Spec: /Users/inseokseo/Gem-squared-AI/gem2-TPMN-checker/AUDIT_GATE_API.md
// The console handlers (audit_gate_handlers.go) call into this client after
// validating the caller's payload and injecting the gem2_api_key + LLM provider
// key from server-side env.

const defaultAuditGateBaseURL = "https://gem2-tpmn-checker.fly.dev"

func auditGateBaseURL() string {
	if v := os.Getenv("GEM2_GATE_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultAuditGateBaseURL
}

// AuditGateRequest mirrors the SaaS request envelope. One of {I,A} or {O,B}
// is populated depending on whether the call targets p-check or o-check.
type AuditGateRequest struct {
	I interface{} `json:"i,omitempty"`
	O interface{} `json:"o,omitempty"`
	A interface{} `json:"a,omitempty"`
	B interface{} `json:"b,omitempty"`

	P []string `json:"p"`
	T int      `json:"t"`

	// WP-AO-61 — evidence is a separate top-level field per external mapping
	// verification (2026-05-18). Retrieved ledger rows + compliance snippets
	// belong here, NOT mixed into p[] (which is reserved for the contract's
	// P_pre / P_post rules verbatim). Even if the SaaS treats it as unknown,
	// it's omitempty-safe — and when supported, gives the audit-gate LLM
	// cleaner separation between rules-to-evaluate and grounding facts.
	Evidence []string `json:"evidence,omitempty"`

	SessionContext string `json:"session_context,omitempty"`
	Provider       string `json:"provider,omitempty"`
	Grammar        string `json:"grammar,omitempty"`

	Gem2APIKey      string `json:"gem2_api_key"`
	AnthropicAPIKey string `json:"anthropic_api_key,omitempty"`
	OpenAIAPIKey    string `json:"openai_api_key,omitempty"`
	GeminiAPIKey    string `json:"gemini_api_key,omitempty"`

	ClaudeModel string `json:"claude_model,omitempty"`
	OpenAIModel string `json:"openai_model,omitempty"`
	GeminiModel string `json:"gemini_model,omitempty"`
}

// GateVerdict is the Solar-native gate output schema (no `verdict` field).
// ALLOW/DENY is derived externally: risk_score < T → ALLOW, risk_score ≥ T → DENY.
type GateVerdict struct {
	RiskScore          int      `json:"risk_score"`           // 0-100; higher = riskier
	EvidenceRefs       []string `json:"evidence_refs"`        // anchor A fields or span texts cited
	Confidence         int      `json:"confidence"`           // 0-100; verifier confidence in its own assessment
	EpistemicTag       string   `json:"epistemic_tag"`        // dominant tag: ⊢|⊨|⊬|⊥
	DriftFingerprints  []string `json:"drift_fingerprints"`   // detected SPT drift patterns
}

// AuditGateResponse mirrors the SaaS 200 response. `Meta` is kept as
// json.RawMessage so we forward it to the caller unchanged.
// Verdict is derived from GateVerdict.RiskScore for Solar provider;
// Score is aliased to RiskScore; Reasons aliased to EvidenceRefs.
type AuditGateResponse struct {
	Verdict string          `json:"verdict"`
	Score   int             `json:"score"`
	Reasons []string        `json:"reasons"`
	Meta    json.RawMessage `json:"meta,omitempty"`
	// Gate is the Solar-native verdict detail (populated by solarPCheck/solarOCheck).
	Gate *GateVerdict `json:"gate,omitempty"`
	// EvidenceChecks is populated by solarL2EpistemicGate — per-인용근거 audit.
	EvidenceChecks []L2EvidenceCheck `json:"evidence_checks,omitempty"`
}

// auditGateError represents a failure path with the HTTP status the console
// should return to its own caller. Code is a short tag for log + JSON body.
type auditGateError struct {
	HTTPStatus int
	Code       string
	Detail     string
	RetryAfter string // when Code == "saas_rate_limited"
}

func (e *auditGateError) Error() string {
	return fmt.Sprintf("audit_gate: %d %s — %s", e.HTTPStatus, e.Code, e.Detail)
}

// callPCheck → Solar-native L1 gate when provider=solar/upstage (no GEM2 proxy needed).
// Falls through to the GEM2 SaaS for all other providers.
func callPCheck(ctx context.Context, req AuditGateRequest) (*AuditGateResponse, error) {
	if req.Provider == "solar" || req.Provider == "upstage" {
		return solarPCheck(req)
	}
	return callAuditGate(ctx, "/api/audit-gate/p-check", req)
}

// callOCheck → Solar-native L2 gate when provider=solar/upstage (no GEM2 proxy needed).
// Falls through to the GEM2 SaaS for all other providers.
func callOCheck(ctx context.Context, req AuditGateRequest) (*AuditGateResponse, error) {
	if req.Provider == "solar" || req.Provider == "upstage" {
		return solarOCheck(req)
	}
	return callAuditGate(ctx, "/api/audit-gate/o-check", req)
}

// callAuditGate is the shared transport — JSON encode, POST with 30s timeout,
// map upstream status to a console-side status with a stable error code.
func callAuditGate(ctx context.Context, path string, req AuditGateRequest) (*AuditGateResponse, error) {
	// WP-AO-52 Unit 2 — body-shape normalization before marshal. The SaaS spec
	// (AUDIT_GATE_API.md §L1/L2) mandates `i` and `o` as STRINGS, while the
	// canvas runner threads payload as `map[string]any`. Without coercion the
	// JSON encodes as an object → SaaS returns 400 saas_validation_failed.
	// Also banner-prefixes A/B (matches demo-advanced pattern) and clamps T.
	normalizeAuditGateRequest(&req, path)

	body, err := json.Marshal(req)
	if err != nil {
		return nil, &auditGateError{HTTPStatus: 500, Code: "encode_error", Detail: err.Error()}
	}

	// WP-AO-49 Unit 1 — diagnostic log of outbound body. Truncated to 800 bytes
	// to keep journal-readable while still capturing the field shapes that
	// triggered "saas_validation_failed" 400s during the canvas Run retest.
	log.Printf("[AUDIT-GATE] outbound %s (%d bytes): %s", path, len(body), truncateBytes(body, 800))

	url := auditGateBaseURL() + path

	// WP-AO-66 — bump per-attempt timeout 30s→120s and add ONE retry on
	// context-deadline. SaaS infra responds in ~500ms but its downstream
	// Gemini call occasionally spikes well past a minute under load
	// (observed in canvas chains where 12 sequential audit calls amplify
	// the tail-latency risk). 120s + single retry gives the SaaS room to
	// finish without the demo timing out under realistic Gemini latency.
	const perAttemptTimeout = 120 * time.Second
	var (
		resp     *http.Response
		respBody []byte
		readErr  error
		doErr    error
		attempts int
	)
	for attempts = 0; attempts < 2; attempts++ {
		reqCtx, cancel := context.WithTimeout(ctx, perAttemptTimeout)
		httpReq, herr := http.NewRequestWithContext(reqCtx, "POST", url, bytes.NewReader(body))
		if herr != nil {
			cancel()
			return nil, &auditGateError{HTTPStatus: 500, Code: "request_build_error", Detail: herr.Error()}
		}
		httpReq.Header.Set("Content-Type", "application/json")
		resp, doErr = http.DefaultClient.Do(httpReq)
		if doErr == nil {
			// CRITICAL: drain body BEFORE cancel(). The response body is a
			// streaming reader tied to reqCtx; cancel kills the underlying
			// connection mid-read → io.ReadAll returns 'context canceled'.
			respBody, readErr = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			cancel()
			break
		}
		cancel()
		// Retry only on context-deadline; other errors (DNS, conn-refused,
		// parent-ctx cancel) are not transient → fail fast.
		if !errors.Is(doErr, context.DeadlineExceeded) {
			return nil, &auditGateError{HTTPStatus: 504, Code: "saas_network_error", Detail: doErr.Error()}
		}
		// Parent context already cancelled → don't retry (caller gave up).
		if ctx.Err() != nil {
			return nil, &auditGateError{HTTPStatus: 504, Code: "saas_network_error", Detail: doErr.Error()}
		}
		log.Printf("[AUDIT-GATE] %s attempt %d timed out (%s) — retrying once", path, attempts+1, perAttemptTimeout)
	}
	if doErr != nil {
		return nil, &auditGateError{HTTPStatus: 504, Code: "saas_network_error", Detail: doErr.Error()}
	}

	// WP-AO-52 Unit 1 — log upstream non-200 response body so diagnosing
	// SaaS validation failures doesn't require re-instrumentation. Outbound
	// logging from WP-AO-49 only catches the request side; this matches the
	// response side. 200 path stays quiet to avoid log noise on hot canvas runs.
	if resp.StatusCode != 200 {
		log.Printf("[AUDIT-GATE] upstream %d response (%d bytes): %s", resp.StatusCode, len(respBody), truncateBytes(respBody, 800))
	}

	switch {
	case resp.StatusCode == 200:
		if readErr != nil {
			return nil, &auditGateError{HTTPStatus: 502, Code: "saas_read_error", Detail: readErr.Error()}
		}
		var out AuditGateResponse
		if err := json.Unmarshal(respBody, &out); err != nil {
			return nil, &auditGateError{HTTPStatus: 502, Code: "saas_unparseable", Detail: string(respBody)}
		}
		if out.Verdict == "" {
			return nil, &auditGateError{HTTPStatus: 502, Code: "saas_empty_verdict", Detail: string(respBody)}
		}
		return &out, nil

	case resp.StatusCode == 400:
		return nil, &auditGateError{HTTPStatus: 400, Code: "saas_validation_failed", Detail: string(respBody)}

	case resp.StatusCode == 401:
		// gem2_api_key rejected upstream — surface as 502 (this is OUR misconfig,
		// not the caller's fault)
		return nil, &auditGateError{HTTPStatus: 502, Code: "upstream_auth_failed", Detail: string(respBody)}

	case resp.StatusCode == 429:
		return nil, &auditGateError{
			HTTPStatus: 429,
			Code:       "saas_rate_limited",
			Detail:     string(respBody),
			RetryAfter: resp.Header.Get("Retry-After"),
		}

	case resp.StatusCode >= 500:
		return nil, &auditGateError{HTTPStatus: 502, Code: "saas_upstream_error", Detail: string(respBody)}

	default:
		return nil, &auditGateError{
			HTTPStatus: 502,
			Code:       "saas_unexpected_status",
			Detail:     fmt.Sprintf("upstream %d: %s", resp.StatusCode, truncate(string(respBody), 300)),
		}
	}
}

// writeAuditGateError writes a JSON error body to the console caller with the
// mapped HTTP status. Used by both handlers.
func writeAuditGateError(w http.ResponseWriter, e *auditGateError) {
	w.Header().Set("Content-Type", "application/json")
	if e.RetryAfter != "" {
		w.Header().Set("Retry-After", e.RetryAfter)
	}
	w.WriteHeader(e.HTTPStatus)

	body := map[string]string{
		"error": e.Code,
		"detail": truncate(e.Detail, 500),
	}
	if e.RetryAfter != "" {
		body["retry_after"] = e.RetryAfter
	}
	// best-effort encode; if it fails we've already written the status
	_ = json.NewEncoder(w).Encode(body)
}

// normalizeAuditGateRequest enforces the SaaS request envelope shape before
// marshaling. Mutates req in place. Path discriminates L1 (`i`+`a`) vs L2
// (`o`+`b`) so we don't accidentally banner the wrong field.
//
// Rules (per AUDIT_GATE_API.md):
//   - `i` / `o` MUST be string. If the caller supplied a Go map/struct/slice,
//     JSON-marshal it and use the encoded string.
//   - `a` / `b` may be string OR object. We keep object as-is, but for string
//     values that contain a newline (typical multi-line schema text from
//     `## A:` / `## B:` blocks) we prefix with a one-line banner so the SaaS
//     LLM treats it as schema framing rather than free-form NL. Matches the
//     proven demo-advanced pattern (workflow_gates.go::handleWorkflowPCheck).
//   - `p` is already nil→empty-defended at the call site (WP-AO-49 U2). Keep.
//   - `t` is clamped: 0 (or unset) → 70 (sane default); >100 → 100; <0 → 0.
//     0 is technically in spec range but functionally degrades the gate.
func normalizeAuditGateRequest(req *AuditGateRequest, path string) {
	isL1 := strings.Contains(path, "p-check")

	// Coerce I (L1) — input value MUST be string per spec line 88.
	if isL1 && req.I != nil {
		if _, ok := req.I.(string); !ok {
			if buf, err := json.Marshal(req.I); err == nil {
				req.I = string(buf)
			}
		}
	}
	// Coerce O (L2) — output value MUST be string per spec line 198.
	if !isL1 && req.O != nil {
		if _, ok := req.O.(string); !ok {
			if buf, err := json.Marshal(req.O); err == nil {
				req.O = string(buf)
			}
		}
	}

	// Banner-prefix A/B if multi-line string (gives SaaS LLM contextual framing).
	if isL1 {
		if s, ok := req.A.(string); ok && strings.Contains(s, "\n") && !strings.HasPrefix(s, "Input schema:") {
			req.A = "Input schema:\n" + s
		}
	} else {
		if s, ok := req.B.(string); ok && strings.Contains(s, "\n") && !strings.HasPrefix(s, "Output schema:") {
			req.B = "Output schema:\n" + s
		}
	}

	// Threshold clamp. 0 is in spec range [0,100] but functionally means
	// "accept any score" — degrades governance. Default 70 matches the
	// demo-advanced auditThresholdDefault.
	switch {
	case req.T <= 0:
		req.T = 70
	case req.T > 100:
		req.T = 100
	}
}
