package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

type GEM2AuditRequest struct {
	Content  string `json:"content"`
	Provider string `json:"provider,omitempty"`
}

type SPTFlag struct {
	Type  string `json:"type"`
	Claim string `json:"claim"`
}

type EvalItem struct {
	Name   string  `json:"name"`
	Score  float64 `json:"score"`
	Status string  `json:"status"`
}

type EEFData struct {
	Flagged                bool    `json:"flagged"`
	ExtrapolationPossibility float64 `json:"extrapolation_possibility"`
}

type GEM2Result struct {
	TruthScore      int             `json:"truth_score"`
	EpistemicScore  float64         `json:"epistemic_score"`
	Verdict         string          `json:"verdict"`
	SPTFlags        []SPTFlag       `json:"spt_flags"`
	EEF             EEFData         `json:"eef"`
	EvalItems       []EvalItem      `json:"evaluation_items"`
	ProviderName    string          `json:"provider_name"`
	DurationMs      float64         `json:"duration_ms"`
	Raw             json.RawMessage `json:"raw,omitempty"`
}

type providerConfig struct {
	name             string // display name shown in UI dropdown
	upstreamProvider string // "gemini", "claude", or "openai" — what gem2-tpmn-checker expects
	apiKeyField      string // JSON field for the LLM API key
	apiKey           string
	modelField       string // "gemini_model", "claude_model", or "openai_model"
	model            string
}

func getProviders() []providerConfig {
	var providers []providerConfig

	if key := os.Getenv("GEMINI_API_KEY"); key != "" {
		providers = append(providers,
			providerConfig{"gemini-flash", "gemini", "gemini_api_key", key, "gemini_model", "gemini-2.5-flash"},
			providerConfig{"gemini-pro", "gemini", "gemini_api_key", key, "gemini_model", "gemini-2.5-pro"},
		)
	}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		providers = append(providers,
			providerConfig{"claude-haiku", "claude", "anthropic_api_key", key, "claude_model", "claude-haiku-4-5"},
			providerConfig{"claude-sonnet", "claude", "anthropic_api_key", key, "claude_model", "claude-sonnet-4-6"},
			providerConfig{"claude-opus", "claude", "anthropic_api_key", key, "claude_model", "claude-opus-4-6"},
		)
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		providers = append(providers,
			providerConfig{"gpt-4o-mini", "openai", "openai_api_key", key, "openai_model", "gpt-4o-mini"},
			providerConfig{"gpt-4o", "openai", "openai_api_key", key, "openai_model", "gpt-4o"},
			providerConfig{"o3", "openai", "openai_api_key", key, "openai_model", "o3"},
		)
	}
	return providers
}

func getProvider(name string) *providerConfig {
	for _, p := range getProviders() {
		if p.name == name {
			return &p
		}
	}
	providers := getProviders()
	if len(providers) > 0 {
		return &providers[0]
	}
	return nil
}

func gem2Audit(content string, providerName string) (*GEM2Result, error) {
	provider := getProvider(providerName)
	if provider == nil {
		return nil, fmt.Errorf("no provider available")
	}

	gem2Key := os.Getenv("GEM2_API_KEY")
	if gem2Key == "" {
		return nil, fmt.Errorf("GEM2_API_KEY not set")
	}

	gem2URL := os.Getenv("GEM2_API_URL")
	if gem2URL == "" {
		gem2URL = "https://gem2-tpmn-checker.fly.dev"
	}

	payload := map[string]string{
		"content":            content,
		"session_context":    "console-demo",
		"gem2_api_key":       gem2Key,
		provider.apiKeyField: provider.apiKey,
		"provider":           provider.upstreamProvider,
		provider.modelField:  provider.model,
	}

	body, _ := json.Marshal(payload)
	start := time.Now()

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Post(gem2URL+"/api/v1/truth-filter", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("GEM² API error: %w", err)
	}
	defer resp.Body.Close()

	elapsed := time.Since(start).Seconds() * 1000

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GEM² API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var data map[string]interface{}
	if err := json.Unmarshal(respBody, &data); err != nil {
		return nil, fmt.Errorf("failed to parse GEM² response: %w", err)
	}

	result := &GEM2Result{
		ProviderName: provider.name,
		DurationMs:   elapsed,
		Raw:          json.RawMessage(respBody),
	}

	if v, ok := data["truth_score"].(float64); ok {
		result.TruthScore = int(v)
	}
	if v, ok := data["epistemic_score"].(float64); ok {
		result.EpistemicScore = v
	}
	if v, ok := data["verdict"].(string); ok {
		result.Verdict = v
	} else {
		if result.TruthScore >= 70 {
			result.Verdict = "ALLOW"
		} else if result.TruthScore >= 40 {
			result.Verdict = "REVIEW"
		} else {
			result.Verdict = "BLOCK"
		}
	}

	if sptRaw, ok := data["spt_issues"].([]interface{}); ok {
		for _, item := range sptRaw {
			if m, ok := item.(map[string]interface{}); ok {
				flag := SPTFlag{}
				if t, ok := m["type"].(string); ok {
					flag.Type = t
				}
				if c, ok := m["claim"].(string); ok {
					flag.Claim = c
				}
				result.SPTFlags = append(result.SPTFlags, flag)
			}
		}
	}

	if eefRaw, ok := data["eef"].(map[string]interface{}); ok {
		if f, ok := eefRaw["flagged"].(bool); ok {
			result.EEF.Flagged = f
		}
		if ep, ok := eefRaw["extrapolation_possibility"].(float64); ok {
			result.EEF.ExtrapolationPossibility = ep
		}
	}

	if evalRaw, ok := data["evaluation_items"].([]interface{}); ok {
		for _, item := range evalRaw {
			if m, ok := item.(map[string]interface{}); ok {
				ei := EvalItem{}
				if n, ok := m["name"].(string); ok {
					ei.Name = n
				}
				if s, ok := m["score"].(float64); ok {
					ei.Score = s
				}
				if st, ok := m["status"].(string); ok {
					ei.Status = st
				}
				result.EvalItems = append(result.EvalItems, ei)
			}
		}
	}

	return result, nil
}

func gem2InterpretProxy(endpoint string, reqBody []byte, providerName string) ([]byte, error) {
	provider := getProvider(providerName)
	if provider == nil {
		return nil, fmt.Errorf("no provider available")
	}

	gem2Key := os.Getenv("GEM2_API_KEY")
	if gem2Key == "" {
		return nil, fmt.Errorf("GEM2_API_KEY not set")
	}

	gem2URL := os.Getenv("GEM2_API_URL")
	if gem2URL == "" {
		gem2URL = "https://gem2-tpmn-checker.fly.dev"
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(reqBody, &payload); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	payload["gem2_api_key"], _ = json.Marshal(gem2Key)
	payload["provider"], _ = json.Marshal(provider.upstreamProvider)
	apiKeyBytes, _ := json.Marshal(provider.apiKey)
	payload[provider.apiKeyField] = apiKeyBytes
	modelBytes, _ := json.Marshal(provider.model)
	payload[provider.modelField] = modelBytes

	body, _ := json.Marshal(payload)

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Post(gem2URL+endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("interpret API error: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("interpret API returned %d: %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}
