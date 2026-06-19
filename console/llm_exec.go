package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

type ExecResult struct {
	OutputB     map[string]any `json:"output_b"`
	Model       string         `json:"model"`
	Provider    string         `json:"provider"`
	DurationMs  float64        `json:"duration_ms"`
	RawResponse string         `json:"raw_response"`
}

func ExecuteStep(step *WorkflowStep, inputA map[string]any, provider string) (*ExecResult, error) {
	prompt := buildStepPrompt(step, inputA)
	start := time.Now()

	var raw string
	var model string
	var err error

	switch provider {
	case "solar", "upstage":
		model = envOr("UPSTAGE_MODEL", "solar-pro3")
		raw, err = solarChat(model, prompt, 90*time.Second)
	case "ollama":
		model = envOr("OLLAMA_MODEL", "llama3.1:8b")
		raw, err = ollamaGenerate(model, prompt)
	case "vultr":
		model = envOr("VULTR_MODEL", "llama-3.3-70b-instruct")
		raw, err = openAICompatibleChat(
			envOr("VULTR_INFERENCE_URL", "https://api.vultrinference.com/v1"),
			os.Getenv("VULTR_API_KEY"),
			model, prompt, 60*time.Second,
		)
	case "featherless":
		model = envOr("FEATHERLESS_MODEL", "meta-llama/Llama-3.1-70B-Instruct")
		raw, err = openAICompatibleChat(
			envOr("FEATHERLESS_URL", "https://api.featherless.ai/v1"),
			os.Getenv("FEATHERLESS_API_KEY"),
			model, prompt, 60*time.Second,
		)
	default:
		raw, err = premiumExecute(provider, prompt)
		model = provider
	}

	if err != nil {
		return nil, fmt.Errorf("%s: %w", provider, err)
	}

	elapsed := time.Since(start).Milliseconds()

	outputB, parseErr := parseJSONFromLLM(raw)
	if parseErr != nil {
		return &ExecResult{
			RawResponse: raw,
			Model:       model,
			Provider:    provider,
			DurationMs:  float64(elapsed),
		}, fmt.Errorf("JSON parse failed: %w (raw: %.200s)", parseErr, raw)
	}

	return &ExecResult{
		OutputB:     outputB,
		Model:       model,
		Provider:    provider,
		DurationMs:  float64(elapsed),
		RawResponse: raw,
	}, nil
}

func buildStepPrompt(step *WorkflowStep, inputA map[string]any) string {
	inputJSON, _ := json.MarshalIndent(inputA, "", "  ")

	var outputSchema strings.Builder
	for _, f := range step.OutputFields {
		req := "required"
		if f.Nullable {
			req = "nullable"
		}
		outputSchema.WriteString(fmt.Sprintf("  %s: %s (%s)\n", f.Name, f.Type, req))
	}

	return fmt.Sprintf(`You are an AI executing step "%s" of the Loan Approval Pipeline.

## Your Task
Execute the processing logic below on the given input, and produce a JSON output matching the output schema exactly.

## Processing Logic
%s

## Input A
%s

## Output Schema B
%s

## Instructions
1. Follow the processing logic step by step.
2. Output ONLY valid JSON — no markdown, no explanation, no code fences.
3. Every field in the output schema must be present.
4. Use the exact field names shown.
5. For datetime fields, use ISO 8601 format (e.g., "2026-05-14T12:00:00Z").
6. For calculations, show your work mentally but only output the final JSON.
7. Be precise with arithmetic — the output will be verified against exact formulas.

Output the JSON now:`,
		step.Name,
		step.FunctionDescription,
		string(inputJSON),
		outputSchema.String(),
	)
}

// ── Ollama ──────────────────────────────────────────────────────────

func ollamaGenerate(model, prompt string) (string, error) {
	url := envOr("OLLAMA_URL", "http://localhost:11434") + "/api/generate"
	body := map[string]any{
		"model":  model,
		"prompt": prompt,
		"stream": false,
		"options": map[string]any{
			"temperature": 0.1,
			"num_predict": 4096,
		},
	}
	data, _ := json.Marshal(body)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("ollama request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("ollama HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("ollama response parse: %w", err)
	}

	if response, ok := result["response"].(string); ok {
		return response, nil
	}
	return string(respBody), nil
}

// ── OpenAI-compatible (Vultr Serverless, Featherless) ───────────────

func openAICompatibleChat(baseURL, apiKey, model, prompt string, timeout time.Duration) (string, error) {
	url := strings.TrimRight(baseURL, "/") + "/chat/completions"
	body := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature": 0.1,
		"max_tokens":  4096,
	}
	data, _ := json.Marshal(body)

	req, _ := http.NewRequest("POST", url, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("response parse: %w", err)
	}

	choices, ok := result["choices"].([]any)
	if !ok || len(choices) == 0 {
		return string(respBody), nil
	}
	choice := choices[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	return msg["content"].(string), nil
}

// ── Vultr Freeform Call — markdown-or-plaintext output, NO response_format ──
//
// WP-AO-42: skill execution (plan-work, proceed-work, verify-work) needs
// markdown-shaped output. vultrSheepCall pins response_format=json_object
// which forces hybrid garbage when the prompt asks for markdown. This
// variant omits response_format so the LLM is free to emit markdown.
// Used by agentGenerate's Vultr branch. vultrSheepCall stays JSON-locked
// for CE invocation + sample-input generation which DO need JSON.
func vultrFreeformCall(apiKey, model, systemPrompt, userPrompt string) (string, error) {
	endpoint := "https://api.vultrinference.com/v1/chat/completions"

	messages := []map[string]string{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": userPrompt},
	}

	body := map[string]any{
		"model":       model,
		"messages":    messages,
		"temperature": 0.2,
		"max_tokens":  8192,
		// NB: NO response_format here — caller will parse the raw text/markdown.
	}
	data, _ := json.Marshal(body)

	log.Printf("[VULTR-FREEFORM] calling %s (system: %d chars, user: %d chars)", model, len(systemPrompt), len(userPrompt))
	callStart := time.Now()

	req, _ := http.NewRequest("POST", endpoint, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 180 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[VULTR-FREEFORM] request error after %v: %v", time.Since(callStart), err)
		return "", fmt.Errorf("vultr freeform request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		log.Printf("[VULTR-FREEFORM] error %d after %v: %s", resp.StatusCode, time.Since(callStart), truncateBytes(respBody, 500))
		return "", fmt.Errorf("vultr freeform HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("vultr freeform response parse: %w", err)
	}
	choices, ok := result["choices"].([]any)
	if !ok || len(choices) == 0 {
		return string(respBody), fmt.Errorf("vultr freeform: no choices in response")
	}
	choice := choices[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	content, _ := msg["content"].(string)

	log.Printf("[VULTR-FREEFORM] %s complete in %v (%d chars)", model, time.Since(callStart), len(content))
	return content, nil
}

// ── Vultr Sheep Call (system + user messages, JSON response format) ──

func vultrSheepCall(apiKey, model, systemPrompt, userPrompt string) (string, error) {
	endpoint := "https://api.vultrinference.com/v1/chat/completions"

	messages := []map[string]string{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": userPrompt},
	}

	body := map[string]any{
		"model":           model,
		"messages":        messages,
		"temperature":     0.1,
		"max_tokens":      8192,
		"response_format": map[string]string{"type": "json_object"},
	}
	data, _ := json.Marshal(body)

	log.Printf("[VULTR-SHEEP] calling %s (system: %d chars, user: %d chars)", model, len(systemPrompt), len(userPrompt))
	callStart := time.Now()

	req, _ := http.NewRequest("POST", endpoint, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 180 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[VULTR-SHEEP] request error after %v: %v", time.Since(callStart), err)
		return "", fmt.Errorf("vultr request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		log.Printf("[VULTR-SHEEP] error %d after %v: %s", resp.StatusCode, time.Since(callStart), truncateBytes(respBody, 500))
		return "", fmt.Errorf("vultr HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("vultr response parse: %w", err)
	}

	choices, ok := result["choices"].([]any)
	if !ok || len(choices) == 0 {
		return string(respBody), fmt.Errorf("vultr: no choices in response")
	}
	choice := choices[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	content, _ := msg["content"].(string)

	log.Printf("[VULTR-SHEEP] %s complete in %v (%d chars)", model, time.Since(callStart), len(content))
	return content, nil
}

// wolfiGenerate implements the 3-tier Wolfi fallback:
// Tier 1: gemini-2.5-pro → Tier 2: gemini-2.5-flash → Tier 3: DeepSeek-V3.2-NVFP4 on Vultr
func wolfiGenerate(prompt string) (string, error) {
	geminiKey := os.Getenv("GEMINI_API_KEY")
	vultrKey := os.Getenv("VULTR_INFERENCE_API_KEY")

	// Tier 1: gemini-2.5-pro
	if geminiKey != "" {
		raw, err := geminiGenerateStream(geminiKey, "gemini-2.5-pro", prompt, nil)
		if err == nil && strings.TrimSpace(raw) != "" {
			return raw, nil
		}
		log.Printf("[WOLFI] Tier 1 (gemini-2.5-pro) failed: %v — falling back to Tier 2", err)

		// Tier 2: gemini-2.5-flash
		raw, err = geminiGenerateStream(geminiKey, "gemini-2.5-flash", prompt, nil)
		if err == nil && strings.TrimSpace(raw) != "" {
			return raw, nil
		}
		log.Printf("[WOLFI] Tier 2 (gemini-2.5-flash) failed: %v — falling back to Tier 3", err)
	}

	// Tier 3: DeepSeek-V3.2-NVFP4 on Vultr
	if vultrKey != "" {
		raw, err := vultrSheepCall(vultrKey, "DeepSeek-V3.2-NVFP4",
			"You are Wolfi, an AI orchestrator and auditor. Respond precisely and concisely.", prompt)
		if err == nil && strings.TrimSpace(raw) != "" {
			return raw, nil
		}
		log.Printf("[WOLFI] Tier 3 (DeepSeek-V3.2-NVFP4) failed: %v", err)
	}

	return "", fmt.Errorf("all 3 Wolfi tiers failed (gemini-pro, gemini-flash, deepseek-v3.2)")
}

// ── Premium providers (Claude/OpenAI via their native APIs) ─────────

func premiumExecute(provider, prompt string) (string, error) {
	p := getProvider(provider)
	if p == nil {
		return "", fmt.Errorf("unknown provider: %s", provider)
	}

	switch p.upstreamProvider {
	case "claude":
		return claudeGenerate(p.apiKey, p.model, prompt)
	case "openai":
		return openAICompatibleChat(
			"https://api.openai.com/v1",
			p.apiKey, p.model, prompt, 60*time.Second,
		)
	case "gemini":
		return geminiGenerate(p.apiKey, p.model, prompt)
	default:
		return "", fmt.Errorf("unsupported upstream: %s", p.upstreamProvider)
	}
}

func claudeGenerate(apiKey, model, prompt string) (string, error) {
	url := "https://api.anthropic.com/v1/messages"
	body := map[string]any{
		"model":      model,
		"max_tokens": 4096,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	data, _ := json.Marshal(body)

	req, _ := http.NewRequest("POST", url, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("claude request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("claude HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]any
	json.Unmarshal(respBody, &result)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		return string(respBody), nil
	}
	block := content[0].(map[string]any)
	text, _ := block["text"].(string)
	return text, nil
}

// geminiGenerate calls Gemini using streamGenerateContent for resilience against
// thinking-model timeouts. Accumulates all chunks and returns full text.
// Optional onChunk callback receives partial text as it arrives.
func geminiGenerate(apiKey, model, prompt string) (string, error) {
	return geminiGenerateStream(apiKey, model, prompt, nil)
}

func geminiGenerateStream(apiKey, model, prompt string, onChunk func(string)) (string, error) {
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:streamGenerateContent?alt=sse&key=%s", model, apiKey)
	body := map[string]any{
		"contents": []map[string]any{
			{"parts": []map[string]string{{"text": prompt}}},
		},
		"generationConfig": map[string]any{
			"temperature": 0.1,
			"maxOutputTokens": 8192,
		},
	}
	data, _ := json.Marshal(body)

	log.Printf("[GEMINI] streaming %s (prompt: %d chars)", model, len(prompt))
	callStart := time.Now()

	client := &http.Client{Timeout: 180 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		log.Printf("[GEMINI] request error after %v: %v", time.Since(callStart), err)
		return "", fmt.Errorf("gemini request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[GEMINI] error %d after %v: %s", resp.StatusCode, time.Since(callStart), truncateBytes(respBody, 500))
		return "", fmt.Errorf("gemini HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var fullText strings.Builder
	chunkCount := 0
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		jsonData := line[6:]
		if jsonData == "[DONE]" {
			break
		}

		var chunk map[string]any
		if err := json.Unmarshal([]byte(jsonData), &chunk); err != nil {
			continue
		}

		candidates, _ := chunk["candidates"].([]any)
		if len(candidates) == 0 {
			continue
		}
		cand, _ := candidates[0].(map[string]any)
		content, _ := cand["content"].(map[string]any)
		parts, _ := content["parts"].([]any)
		for _, p := range parts {
			part, _ := p.(map[string]any)
			if text, ok := part["text"].(string); ok && text != "" {
				fullText.WriteString(text)
				chunkCount++
				if onChunk != nil {
					onChunk(text)
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[GEMINI] stream read error after %v: %v", time.Since(callStart), err)
		if fullText.Len() > 0 {
			log.Printf("[GEMINI] returning partial result (%d chars from %d chunks)", fullText.Len(), chunkCount)
			return fullText.String(), nil
		}
		return "", fmt.Errorf("gemini stream read: %w", err)
	}

	log.Printf("[GEMINI] stream complete in %v (%d chunks, %d chars)", time.Since(callStart), chunkCount, fullText.Len())
	return fullText.String(), nil
}

func truncateBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}

// ── JSON extraction ─────────────────────────────────────────────────

func parseJSONFromLLM(raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)

	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		var inside bool
		var jsonLines []string
		for _, line := range lines {
			if strings.HasPrefix(line, "```") {
				if inside {
					break
				}
				inside = true
				continue
			}
			if inside {
				jsonLines = append(jsonLines, line)
			}
		}
		if len(jsonLines) > 0 {
			raw = strings.Join(jsonLines, "\n")
		}
	}

	start := strings.IndexByte(raw, '{')
	if start == -1 {
		return nil, fmt.Errorf("no JSON object found")
	}

	depth := 0
	end := -1
	for i := start; i < len(raw); i++ {
		switch raw[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i + 1
				break
			}
		}
		if end > 0 {
			break
		}
	}

	if end == -1 {
		return nil, fmt.Errorf("unclosed JSON object")
	}

	jsonStr := raw[start:end]
	var result map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return result, nil
}

// ── Solar / Upstage ──────────────────────────────────────────────────
// OpenAI-compatible endpoint at api.upstage.ai/v1.
// solarChat sends a single user-turn prompt and returns the text completion.
// solarChatMessages sends a full []messages slice (system + user).

const solarBaseURL = "https://api.upstage.ai/v1"

func solarAPIKey() string { return os.Getenv("UPSTAGE_API_KEY") }

func solarChat(model, prompt string, timeout time.Duration) (string, error) {
	return openAICompatibleChat(solarBaseURL, solarAPIKey(), model, prompt, timeout)
}

func solarChatMessages(model string, messages []map[string]string, timeout time.Duration) (string, error) {
	apiKey := solarAPIKey()
	if apiKey == "" {
		return "", fmt.Errorf("UPSTAGE_API_KEY not set")
	}
	url := solarBaseURL + "/chat/completions"
	body := map[string]any{
		"model":           model,
		"messages":        messages,
		"temperature":     0.1,
		"max_tokens":      4096,
		"response_format": map[string]string{"type": "json_object"},
	}
	data, _ := json.Marshal(body)

	req, _ := http.NewRequest("POST", url, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("solar request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("solar HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("solar response parse: %w", err)
	}
	choices, ok := result["choices"].([]any)
	if !ok || len(choices) == 0 {
		return string(respBody), nil
	}
	choice := choices[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	content, _ := msg["content"].(string)
	return content, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
