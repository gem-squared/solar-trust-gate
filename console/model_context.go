package main

import (
	"log"
	"strings"
)

// ModelContext captures the input-side budget for an LLM model.
//
// WindowTokens       = total context window the model supports (input + output)
// InputBudgetTokens  = recommended cap for our INPUT portion (≈ 50% of window;
//                       leaves headroom for response + system prompt + tokenizer overhead)
// BytesPerToken      = approximate chars-per-token ratio for budget computation.
//                       Higher for English-heavy LLMs (~4.0), lower for multilingual
//                       (~3.5) since per-token chars drop on non-Latin scripts.
type ModelContext struct {
	WindowTokens      int
	InputBudgetTokens int
	BytesPerToken     float64
}

// modelContextTable — verified context windows.
//
// Gemini: from Google AI documentation (2026-05 spec).
// Claude: from Anthropic API documentation.
// Vultr:  VERIFIED via live GET https://api.vultrinference.com/v1/models (2026-05-16).
//
// Keys match the strings already in use across orchestrator.go / sheep_registry.go.
var modelContextTable = map[string]ModelContext{
	// ── Google Gemini ────────────────────────────────────────────────
	"gemini-2.5-pro":         {WindowTokens: 1_048_576, InputBudgetTokens: 500_000, BytesPerToken: 4.0},
	"gemini-2.5-flash":       {WindowTokens: 1_048_576, InputBudgetTokens: 500_000, BytesPerToken: 4.0},
	"gemini-2.0-flash":       {WindowTokens: 1_048_576, InputBudgetTokens: 500_000, BytesPerToken: 4.0},
	"gemini-2.0-flash-lite":  {WindowTokens: 1_048_576, InputBudgetTokens: 500_000, BytesPerToken: 4.0},

	// ── Anthropic Claude ─────────────────────────────────────────────
	"claude-sonnet-4-6":      {WindowTokens: 200_000, InputBudgetTokens: 100_000, BytesPerToken: 3.8},
	"claude-opus-4-7":        {WindowTokens: 200_000, InputBudgetTokens: 100_000, BytesPerToken: 3.8},
	"claude-haiku-4-5":       {WindowTokens: 200_000, InputBudgetTokens: 100_000, BytesPerToken: 3.8},

	// ── Vultr Serverless Inference (verified 2026-05-16 via /v1/models) ──
	"vultr/MiniMax-M2.7":                                  {WindowTokens: 393_216, InputBudgetTokens: 196_000, BytesPerToken: 3.5},
	"vultr/Kimi-K2.6":                                     {WindowTokens: 262_144, InputBudgetTokens: 131_000, BytesPerToken: 3.5},
	"vultr/DeepSeek-V3.2-NVFP4":                           {WindowTokens: 163_840, InputBudgetTokens: 81_000,  BytesPerToken: 3.5},
	"vultr/Llama-3.1-Nemotron-Safety-Guard-8B-v3":         {WindowTokens: 131_072, InputBudgetTokens: 65_000,  BytesPerToken: 3.5},
	"vultr/Nemotron-3-Nano-Omni-30B-A3B-Reasoning-BF16":   {WindowTokens: 262_144, InputBudgetTokens: 131_000, BytesPerToken: 3.5},
	"vultr/Nemotron-Cascade-2-30B-A3B":                    {WindowTokens: 262_144, InputBudgetTokens: 131_000, BytesPerToken: 3.5},
	"vultr/GLM-5.1-FP8":                                   {WindowTokens: 202_752, InputBudgetTokens: 101_000, BytesPerToken: 3.5},

	// vultr/Qwen3.5-397B-A17B-FP8 was in the sheep_registry but is NOT in the
	// current Vultr catalog (verified missing on 2026-05-16). Falls back to default.
}

// defaultUnknownModel returns the safe budget for any model not in the table.
// 32K input tokens × 4 bytes/token ≈ 128 KB — tight enough to be safe on small
// models, generous enough to fit a typical failure-evidence payload.
var defaultUnknownModel = ModelContext{
	WindowTokens:      32_768,
	InputBudgetTokens: 16_000,
	BytesPerToken:     3.5,
}

// LookupModelContext returns the budget profile for a model, falling back to
// the safe default and logging a one-shot warning when the model is unknown.
func LookupModelContext(model string) ModelContext {
	if ctx, ok := modelContextTable[model]; ok {
		return ctx
	}
	// Try a case-insensitive lookup before giving up — some call paths use
	// inconsistent casing for Vultr model IDs.
	lower := strings.ToLower(model)
	for k, v := range modelContextTable {
		if strings.ToLower(k) == lower {
			return v
		}
	}
	logUnknownModelOnce(model)
	return defaultUnknownModel
}

// InputBudgetBytes returns the recommended INPUT budget for a model, in bytes.
// This is the cap callers should respect when composing fat-context prompts.
func InputBudgetBytes(model string) int {
	ctx := LookupModelContext(model)
	return int(float64(ctx.InputBudgetTokens) * ctx.BytesPerToken)
}

// fileEvidenceBudget returns per-file and total byte caps for the
// "File contents (evidence)" section of a proceed-work Result block.
//
// perFile = min(2 MB, InputBudgetBytes(model) * 0.4 / max(numFiles, 1))
// total   = 20 MB (David's spec — covers even huge codebases)
//
// WP-AO-28 Unit 1.
func fileEvidenceBudget(model string, numFiles int) (perFile, total int) {
	const hardPerFileCap = 2 * 1024 * 1024  // 2 MB
	const hardTotalCap = 20 * 1024 * 1024   // 20 MB
	if numFiles < 1 {
		numFiles = 1
	}
	dynamicPerFile := int(float64(InputBudgetBytes(model)) * 0.4 / float64(numFiles))
	perFile = hardPerFileCap
	if dynamicPerFile < perFile {
		perFile = dynamicPerFile
	}
	if perFile < 4096 {
		// Floor — never go below 4 KB per file; otherwise we re-create the
		// truncation problem we're fixing.
		perFile = 4096
	}
	total = hardTotalCap
	return perFile, total
}

// summaryCap returns the byte cap for the "Summary:" section of a
// proceed-work Result block. Dynamic by model with a 500 KB hard ceiling.
//
// WP-AO-28 Unit 2.
func summaryCap(model string) int {
	const hardCap = 500 * 1024 // 500 KB
	dynamic := int(float64(InputBudgetBytes(model)) * 0.2)
	if dynamic < hardCap {
		return dynamic
	}
	return hardCap
}

// unknownModelLogged tracks which model names we've already warned about, to
// keep the log readable when the same unknown model is queried many times.
var unknownModelLogged = make(map[string]bool)

func logUnknownModelOnce(model string) {
	if unknownModelLogged[model] {
		return
	}
	unknownModelLogged[model] = true
	log.Printf("[MODEL_CONTEXT] unknown model %q — using default budget (%d tokens / %d bytes)",
		model, defaultUnknownModel.InputBudgetTokens,
		int(float64(defaultUnknownModel.InputBudgetTokens)*defaultUnknownModel.BytesPerToken))
}
