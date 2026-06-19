package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// ── Vultr Tool-Call Loop ─────────────────────────────────────────
//
// WP-AO-53 Unit 4 — multi-turn function-calling loop for the CE Executor.
// Uses Vultr's OpenAI-compatible /v1/chat/completions endpoint with
// `tools[]` + `tool_choice: "auto"`. Loops until the LLM emits a final
// `content` (final JSON answer) or hits the iteration cap.
//
// DeepSeek-V3 and most Vultr-hosted instruction models support this format.
// Tool calls are stripped from messages on each turn and executed against
// the registered handlers in ce_tools.go. The tool-call history is also
// returned so the calling HTTP handler can include it in the response
// envelope (consumed by workflow_runner.go to emit per-tool trace events).

const (
	toolLoopMaxIterations = 8
	toolLoopFinalTemp     = 0.1
	toolLoopMaxTokens     = 8192
)

// ToolCallTrace is one tool execution record. Returned in the CE invoke
// envelope so callers can render the trace.
type ToolCallTrace struct {
	Tool       string      `json:"tool"`
	Args       interface{} `json:"args,omitempty"`
	Summary    string      `json:"summary"`
	LatencyMs  int64       `json:"latency_ms"`
	Error      string      `json:"error,omitempty"`
}

// vultrToolCallLoop runs the multi-turn loop. Returns final content (raw JSON
// string the LLM produced), the list of tool calls performed, and any error.
//
// projectSlug discriminates which DB the tool handlers run against. tools is
// the per-project tool set built by toolsForProject(slug). The caller is
// responsible for building tools BEFORE the loop; we don't refresh it per turn.
func vultrToolCallLoop(
	ctx context.Context,
	apiKey, model string,
	systemPrompt, userPrompt string,
	tools []ToolDef,
	projectSlug string,
) (string, []ToolCallTrace, error) {
	endpoint := "https://api.vultrinference.com/v1/chat/completions"

	toolByName := make(map[string]ToolDef, len(tools))
	for _, t := range tools {
		toolByName[t.Name] = t
	}

	toolsPayload := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		// OpenAI / DeepSeek-V3 function-call wrapper shape
		var paramsObj any
		_ = json.Unmarshal(t.Parameters, &paramsObj)
		toolsPayload = append(toolsPayload, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  paramsObj,
			},
		})
	}

	// Initial conversation
	messages := []map[string]any{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": userPrompt},
	}

	var trace []ToolCallTrace

	for iter := 0; iter < toolLoopMaxIterations; iter++ {
		body := map[string]any{
			"model":       model,
			"messages":    messages,
			"temperature": toolLoopFinalTemp,
			"max_tokens":  toolLoopMaxTokens,
		}
		if len(toolsPayload) > 0 {
			body["tools"] = toolsPayload
			body["tool_choice"] = "auto"
		}

		data, _ := json.Marshal(body)
		log.Printf("[VULTR-TOOLS] iter=%d model=%s msgs=%d tools=%d", iter, model, len(messages), len(toolsPayload))

		req, _ := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)

		client := &http.Client{Timeout: 180 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return "", trace, fmt.Errorf("vultr tools request iter=%d: %w", iter, err)
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != 200 {
			log.Printf("[VULTR-TOOLS] iter=%d HTTP %d: %s", iter, resp.StatusCode, truncateBytes(respBody, 500))
			return "", trace, fmt.Errorf("vultr HTTP %d: %s", resp.StatusCode, truncateBytes(respBody, 500))
		}

		var parsed struct {
			Choices []struct {
				Message struct {
					Role      string          `json:"role"`
					Content   string          `json:"content"`
					ToolCalls []rawToolCall   `json:"tool_calls,omitempty"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(respBody, &parsed); err != nil {
			return "", trace, fmt.Errorf("vultr response parse: %w", err)
		}
		if len(parsed.Choices) == 0 {
			return "", trace, fmt.Errorf("vultr returned 0 choices")
		}
		msg := parsed.Choices[0].Message

		// If LLM emitted tool_calls, execute them and continue the loop.
		if len(msg.ToolCalls) > 0 {
			// Echo the assistant's tool_call message back into the conversation
			// (required by OpenAI/DeepSeek tool-call protocol — the tool result
			// messages reference the tool_call_id from this turn).
			assistantMsg := map[string]any{
				"role":       "assistant",
				"content":    msg.Content, // may be empty string
				"tool_calls": rawToolCallsToWire(msg.ToolCalls),
			}
			messages = append(messages, assistantMsg)

			for _, tc := range msg.ToolCalls {
				start := time.Now()
				summary := ""
				errStr := ""
				var resultPayload any

				tdef, ok := toolByName[tc.Function.Name]
				if !ok {
					errStr = "unknown tool: " + tc.Function.Name
					resultPayload = map[string]string{"error": errStr}
				} else {
					var argsMap map[string]any
					if tc.Function.Arguments != "" {
						if jerr := json.Unmarshal([]byte(tc.Function.Arguments), &argsMap); jerr != nil {
							errStr = "args parse: " + jerr.Error()
						}
					}
					if errStr == "" {
						out, herr := tdef.Handler(ctx, projectSlug, argsMap)
						if herr != nil {
							errStr = herr.Error()
							resultPayload = map[string]string{"error": herr.Error()}
						} else {
							resultPayload = out
							summary = renderToolSummary(tc.Function.Name, out)
						}
					}
				}

				latency := time.Since(start).Milliseconds()
				trace = append(trace, ToolCallTrace{
					Tool:      tc.Function.Name,
					Args:      parseArgsForTrace(tc.Function.Arguments),
					Summary:   summary,
					LatencyMs: latency,
					Error:     errStr,
				})

				resultJSON, _ := json.Marshal(resultPayload)
				messages = append(messages, map[string]any{
					"role":         "tool",
					"tool_call_id": tc.ID,
					"name":         tc.Function.Name,
					"content":      string(resultJSON),
				})
			}
			continue // loop to next iteration so the LLM can react to tool results
		}

		// No tool_calls — this is the final answer. Return content.
		if msg.Content == "" {
			return "", trace, fmt.Errorf("vultr returned empty content (iter=%d, finish=%s)", iter, parsed.Choices[0].FinishReason)
		}
		log.Printf("[VULTR-TOOLS] complete iter=%d trace=%d", iter+1, len(trace))
		return msg.Content, trace, nil
	}

	return "", trace, fmt.Errorf("tool-call loop exceeded %d iterations without final content", toolLoopMaxIterations)
}

// rawToolCall mirrors the OpenAI/DeepSeek tool_call shape returned by Vultr.
type rawToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// rawToolCallsToWire re-serializes tool calls for echoing into messages[].
// The wire shape is identical to what Vultr returned; we just re-marshal.
func rawToolCallsToWire(in []rawToolCall) []map[string]any {
	out := make([]map[string]any, len(in))
	for i, tc := range in {
		out[i] = map[string]any{
			"id":   tc.ID,
			"type": tc.Type,
			"function": map[string]any{
				"name":      tc.Function.Name,
				"arguments": tc.Function.Arguments,
			},
		}
	}
	return out
}

// parseArgsForTrace tries to JSON-decode tool arguments for the trace payload.
// On failure returns the raw string so it's never lost.
func parseArgsForTrace(raw string) any {
	if raw == "" {
		return nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
		return parsed
	}
	return raw
}
