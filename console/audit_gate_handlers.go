package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// ── Audit-Gate Handlers ────────────────────────────────────────────
//
// POST /api/audit-gate/p-check  → L1 pre-execution gate (handled by handlePCheck)
// POST /api/audit-gate/o-check  → L2 post-execution gate (handled by handleOCheck)
//
// These are thin proxies: validate caller's payload, inject server-side
// secrets (gem2_api_key + LLM provider key), call SaaS via audit_gate_client.go,
// return the SaaS response verbatim on 200 or a mapped error on failure.

// callerPCheckRequest — what the console caller POSTs to /api/audit-gate/p-check.
// Excludes the secret keys; server injects them from env.
type callerPCheckRequest struct {
	I              interface{} `json:"i"`
	A              interface{} `json:"a"`
	P              []string    `json:"p"`
	T              *int        `json:"t"` // pointer so we can detect "missing" vs "zero"
	SessionContext string      `json:"session_context,omitempty"`
	Provider       string      `json:"provider,omitempty"`
	Grammar        string      `json:"grammar,omitempty"`
	ClaudeModel    string      `json:"claude_model,omitempty"`
	OpenAIModel    string      `json:"openai_model,omitempty"`
	GeminiModel    string      `json:"gemini_model,omitempty"`
}

// callerOCheckRequest — same shape but with O/B instead of I/A.
type callerOCheckRequest struct {
	O              interface{} `json:"o"`
	B              interface{} `json:"b"`
	P              []string    `json:"p"`
	T              *int        `json:"t"`
	SessionContext string      `json:"session_context,omitempty"`
	Provider       string      `json:"provider,omitempty"`
	Grammar        string      `json:"grammar,omitempty"`
	ClaudeModel    string      `json:"claude_model,omitempty"`
	OpenAIModel    string      `json:"openai_model,omitempty"`
	GeminiModel    string      `json:"gemini_model,omitempty"`
}

func handlePCheck(w http.ResponseWriter, r *http.Request) {
	var req callerPCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_request_body","detail":"`+strings.ReplaceAll(err.Error(), `"`, `'`)+`"}`, http.StatusBadRequest)
		return
	}
	if req.I == nil {
		http.Error(w, `{"error":"missing_field","detail":"'i' (input value) is required"}`, http.StatusBadRequest)
		return
	}
	if req.A == nil {
		http.Error(w, `{"error":"missing_field","detail":"'a' (input type) is required"}`, http.StatusBadRequest)
		return
	}
	if req.T == nil {
		http.Error(w, `{"error":"missing_field","detail":"'t' (threshold) is required"}`, http.StatusBadRequest)
		return
	}
	if *req.T < 0 || *req.T > 100 {
		http.Error(w, `{"error":"invalid_threshold","detail":"'t' must be in [0,100]"}`, http.StatusBadRequest)
		return
	}

	// Solar-native path: bypass GEM2 proxy, call Solar directly.
	provider := req.Provider
	if provider == "" {
		if os.Getenv("UPSTAGE_API_KEY") != "" {
			provider = "solar"
		}
	}
	if provider == "solar" || provider == "upstage" {
		if os.Getenv("UPSTAGE_API_KEY") == "" {
			http.Error(w, `{"error":"config_error","detail":"UPSTAGE_API_KEY not set"}`, http.StatusInternalServerError)
			return
		}
		solarReq := AuditGateRequest{
			I: req.I, A: req.A,
			P: req.P, T: *req.T,
			SessionContext: req.SessionContext,
			Provider:       "solar",
		}
		if solarReq.P == nil {
			solarReq.P = []string{}
		}
		start := time.Now()
		resp, err := solarPCheck(solarReq)
		durationMs := time.Since(start).Milliseconds()
		if err != nil {
			log.Printf("[AUDIT_GATE] gate=p solar_error duration_ms=%d: %v", durationMs, err)
			http.Error(w, `{"error":"solar_gate_error","detail":"`+strings.ReplaceAll(err.Error(), `"`, `'`)+`"}`, http.StatusInternalServerError)
			return
		}
		log.Printf("[AUDIT_GATE] gate=p verdict=%s score=%d duration_ms=%d provider=solar ip=%s",
			resp.Verdict, resp.Score, durationMs, authClientIP(r))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	upstream, gerr := buildUpstreamRequest(w, req.Provider, req.ClaudeModel, req.OpenAIModel, req.GeminiModel)
	if gerr != nil {
		return // buildUpstreamRequest already wrote the error response
	}
	upstream.I = req.I
	upstream.A = req.A
	upstream.P = req.P
	if upstream.P == nil {
		upstream.P = []string{}
	}
	upstream.T = *req.T
	upstream.SessionContext = req.SessionContext
	upstream.Grammar = req.Grammar

	start := time.Now()
	resp, callErr := callPCheck(r.Context(), upstream)
	durationMs := time.Since(start).Milliseconds()

	if callErr != nil {
		if e, ok := callErr.(*auditGateError); ok {
			log.Printf("[AUDIT_GATE] gate=p status=%d code=%s duration_ms=%d provider=%s ip=%s",
				e.HTTPStatus, e.Code, durationMs, upstream.Provider, authClientIP(r))
			writeAuditGateError(w, e)
			return
		}
		log.Printf("[AUDIT_GATE] gate=p unexpected_error duration_ms=%d: %v", durationMs, callErr)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}

	log.Printf("[AUDIT_GATE] gate=p verdict=%s score=%d duration_ms=%d provider=%s ip=%s",
		resp.Verdict, resp.Score, durationMs, upstream.Provider, authClientIP(r))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func handleOCheck(w http.ResponseWriter, r *http.Request) {
	var req callerOCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_request_body","detail":"`+strings.ReplaceAll(err.Error(), `"`, `'`)+`"}`, http.StatusBadRequest)
		return
	}
	if req.O == nil {
		http.Error(w, `{"error":"missing_field","detail":"'o' (output value) is required"}`, http.StatusBadRequest)
		return
	}
	if req.B == nil {
		http.Error(w, `{"error":"missing_field","detail":"'b' (output type) is required"}`, http.StatusBadRequest)
		return
	}
	if req.T == nil {
		http.Error(w, `{"error":"missing_field","detail":"'t' (threshold) is required"}`, http.StatusBadRequest)
		return
	}
	if *req.T < 0 || *req.T > 100 {
		http.Error(w, `{"error":"invalid_threshold","detail":"'t' must be in [0,100]"}`, http.StatusBadRequest)
		return
	}

	// Solar-native path: bypass GEM2 proxy, call Solar directly.
	provider := req.Provider
	if provider == "" {
		if os.Getenv("UPSTAGE_API_KEY") != "" {
			provider = "solar"
		}
	}
	if provider == "solar" || provider == "upstage" {
		if os.Getenv("UPSTAGE_API_KEY") == "" {
			http.Error(w, `{"error":"config_error","detail":"UPSTAGE_API_KEY not set"}`, http.StatusInternalServerError)
			return
		}
		solarReq := AuditGateRequest{
			O: req.O, B: req.B,
			P: req.P, T: *req.T,
			SessionContext: req.SessionContext,
			Provider:       "solar",
		}
		if solarReq.P == nil {
			solarReq.P = []string{}
		}
		start := time.Now()
		resp, err := solarOCheck(solarReq)
		durationMs := time.Since(start).Milliseconds()
		if err != nil {
			log.Printf("[AUDIT_GATE] gate=o solar_error duration_ms=%d: %v", durationMs, err)
			http.Error(w, `{"error":"solar_gate_error","detail":"`+strings.ReplaceAll(err.Error(), `"`, `'`)+`"}`, http.StatusInternalServerError)
			return
		}
		log.Printf("[AUDIT_GATE] gate=o verdict=%s score=%d duration_ms=%d provider=solar ip=%s",
			resp.Verdict, resp.Score, durationMs, authClientIP(r))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	upstream, gerr := buildUpstreamRequest(w, req.Provider, req.ClaudeModel, req.OpenAIModel, req.GeminiModel)
	if gerr != nil {
		return
	}
	upstream.O = req.O
	upstream.B = req.B
	upstream.P = req.P
	if upstream.P == nil {
		upstream.P = []string{}
	}
	upstream.T = *req.T
	upstream.SessionContext = req.SessionContext
	upstream.Grammar = req.Grammar

	start := time.Now()
	resp, callErr := callOCheck(r.Context(), upstream)
	durationMs := time.Since(start).Milliseconds()

	if callErr != nil {
		if e, ok := callErr.(*auditGateError); ok {
			log.Printf("[AUDIT_GATE] gate=o status=%d code=%s duration_ms=%d provider=%s ip=%s",
				e.HTTPStatus, e.Code, durationMs, upstream.Provider, authClientIP(r))
			writeAuditGateError(w, e)
			return
		}
		log.Printf("[AUDIT_GATE] gate=o unexpected_error duration_ms=%d: %v", durationMs, callErr)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}

	log.Printf("[AUDIT_GATE] gate=o verdict=%s score=%d duration_ms=%d provider=%s ip=%s",
		resp.Verdict, resp.Score, durationMs, upstream.Provider, authClientIP(r))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// buildUpstreamRequest injects the gem2_api_key + the LLM provider key from
// server env, and sets the provider default. If any required env var is
// missing, writes a 500 to the caller and returns a non-nil sentinel error.
func buildUpstreamRequest(w http.ResponseWriter, provider, claudeModel, openaiModel, geminiModel string) (AuditGateRequest, error) {
	gem2Key := os.Getenv("GEM2_API_KEY")
	if gem2Key == "" {
		http.Error(w, `{"error":"config_error","detail":"GEM2_API_KEY not configured on server"}`, http.StatusInternalServerError)
		return AuditGateRequest{}, errMissingKey
	}

	if provider == "" {
		provider = "gemini" // server-side default per WP-AO-25 spec
	}

	upstream := AuditGateRequest{
		Gem2APIKey: gem2Key,
		Provider:   provider,
		ClaudeModel: claudeModel,
		OpenAIModel: openaiModel,
		GeminiModel: geminiModel,
	}

	switch provider {
	case "gemini":
		k := os.Getenv("GEMINI_API_KEY")
		if k == "" {
			http.Error(w, `{"error":"config_error","detail":"GEMINI_API_KEY not configured for provider=gemini"}`, http.StatusInternalServerError)
			return AuditGateRequest{}, errMissingKey
		}
		upstream.GeminiAPIKey = k
	case "claude":
		k := os.Getenv("ANTHROPIC_API_KEY")
		if k == "" {
			http.Error(w, `{"error":"config_error","detail":"ANTHROPIC_API_KEY not configured for provider=claude"}`, http.StatusInternalServerError)
			return AuditGateRequest{}, errMissingKey
		}
		upstream.AnthropicAPIKey = k
	case "openai":
		k := os.Getenv("OPENAI_API_KEY")
		if k == "" {
			http.Error(w, `{"error":"config_error","detail":"OPENAI_API_KEY not configured for provider=openai"}`, http.StatusInternalServerError)
			return AuditGateRequest{}, errMissingKey
		}
		upstream.OpenAIAPIKey = k
	default:
		http.Error(w, `{"error":"invalid_provider","detail":"provider must be one of {gemini, claude, openai}"}`, http.StatusBadRequest)
		return AuditGateRequest{}, errMissingKey
	}

	return upstream, nil
}

// errMissingKey is a sentinel — handlers check for non-nil and return early
// since buildUpstreamRequest already wrote the response.
var errMissingKey = &configError{msg: "key or provider missing"}

type configError struct{ msg string }

func (c *configError) Error() string { return c.msg }
