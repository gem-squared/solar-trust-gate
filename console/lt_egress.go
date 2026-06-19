package main

// WP-01 U3 — L3 egress inspection (port from demo-advanced/console/workflow_gates.go:313-957).
//
// L3 is the egress mirror of L0. JSON output → mask PII → render to ≤300-char
// NL summary (Gemini or Vultr via ltRenderJSONasNL) → regex pre-scan → Lobster
// Trap on rendered NL + raw JSON. Catches what L1/L2 schema audits don't:
// unmasked PII in user-facing fields, credential leak, wire-fraud language,
// upstream prompt-injection bleed.
//
// Fail-open: any LLM error → fall back to deterministic JSON-flatten + regex.

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ── l3Pattern + 14 egress patterns (verbatim from demo-advanced) ────

type l3Pattern struct {
	re   *regexp.Regexp
	rule string
}

var l3EgressPatterns = []l3Pattern{
	// Wire-fraud / fraudulent transfer instructions in disbursement output.
	{regexp.MustCompile(`(?i)\b(send|transfer|wire|disburse)\b.{0,80}\$\d`), "block_wire_fraud"},
	{regexp.MustCompile(`(?i)\b(offshore|untraceable|anonymous|shell\s+(company|account)|beneficiary)\b.{0,40}\b(account|routing|holding|recipient)\b`), "block_wire_fraud"},
	{regexp.MustCompile(`(?i)\b(routing|aba)\s*(number|#)?\s*:?\s*\d{9}\b.{0,80}\b(account|recipient|beneficiary)\b`), "block_wire_fraud"},
	// Credential-leak patterns in egress.
	{regexp.MustCompile(`(?i)\b(api\s*key|token|password|secret|bearer)\s*[:=]?\s*[a-zA-Z0-9_\-]{16,}`), "block_credential_leak"},
	{regexp.MustCompile(`-----BEGIN\s+(RSA|EC|DSA|OPENSSH|PRIVATE)\s+(PRIVATE\s+)?KEY-----`), "block_credential_leak"},
	{regexp.MustCompile(`\beyJ[a-zA-Z0-9_\-]{20,}\.[a-zA-Z0-9_\-]{20,}\.[a-zA-Z0-9_\-]{20,}\b`), "block_credential_leak"},
	{regexp.MustCompile(`(?i)\b(mysql|postgres|postgresql|mongodb|redis)://[^\s]{8,}`), "block_credential_leak"},
	{regexp.MustCompile(`(?i)\bsk-[a-zA-Z0-9_\-]{16,}\b`), "block_credential_leak"},               // OpenAI / Anthropic
	{regexp.MustCompile(`\b(ghp|gho|ghu|ghr|ghs)_[A-Za-z0-9]{20,}\b`), "block_credential_leak"},   // GitHub PAT
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), "block_credential_leak"},                          // AWS Access Key ID
	{regexp.MustCompile(`(?i)\b(sk|pk)_(live|test)_[a-zA-Z0-9]{16,}\b`), "block_credential_leak"}, // Stripe
	// Exfiltration channel signature.
	{regexp.MustCompile(`(?i)\b(send|leak|exfil|dump|forward|email)\b.{0,40}\b(to|at)\s+[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`), "block_data_exfiltration"},
}

func l3EgressPreScan(nl string) (rule string, matched bool) {
	for _, p := range l3EgressPatterns {
		if p.re.MatchString(nl) {
			return p.rule, true
		}
	}
	return "", false
}

// ── scrubExpectedBankingFields ───────────────────────────────────
// Masks the legitimate disbursement_account container so the L3 credential-leak
// regex doesn't trigger on routing+account numbers that are part of normal
// claim payout output.
func scrubExpectedBankingFields(v interface{}) interface{} {
	m, ok := v.(map[string]interface{})
	if !ok {
		return v
	}
	out := make(map[string]interface{}, len(m))
	for k, vv := range m {
		if k == "disbursement_account" || k == "payment_details" || k == "bank_account_no" {
			out[k] = "<bank routing+account redacted from L3 scan>"
			continue
		}
		if sub, ok := vv.(map[string]interface{}); ok {
			out[k] = scrubExpectedBankingFields(sub)
		} else {
			out[k] = vv
		}
	}
	return out
}

// ── l3DecodeStego ────────────────────────────────────────────────
// Decode base64 / hex tokens so encoded credentials are caught.
var (
	base64TokenRe = regexp.MustCompile(`\b[A-Za-z0-9+/]{16,}={0,2}\b`)
	hexTokenRe    = regexp.MustCompile(`\b[0-9A-Fa-f]{20,}\b`)
)

func l3DecodeStego(s string) string {
	out := s
	for _, m := range base64TokenRe.FindAllString(s, -1) {
		if len(m)%4 != 0 {
			continue
		}
		if d, err := base64.StdEncoding.DecodeString(m); err == nil && isPrintableUTF8(string(d)) {
			out = out + "\n[B64_DECODED] " + string(d)
		}
	}
	for _, m := range hexTokenRe.FindAllString(s, -1) {
		if len(m)%2 != 0 {
			continue
		}
		if d, err := hex.DecodeString(m); err == nil && isPrintableUTF8(string(d)) {
			out = out + "\n[HEX_DECODED] " + string(d)
		}
	}
	return out
}

func isPrintableUTF8(s string) bool {
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if r < 0x20 || r > 0x7E {
			if r < 0xA0 {
				return false
			}
		}
	}
	return len(s) > 0
}

// ── jsonToNL — deterministic fallback flattening ─────────────────
func jsonToNL(v interface{}) string {
	var b strings.Builder
	flattenJSON(v, &b)
	return strings.TrimSpace(b.String())
}

func flattenJSON(v interface{}, b *strings.Builder) {
	switch t := v.(type) {
	case string:
		b.WriteString(t)
		b.WriteString(" ")
	case float64:
		b.WriteString(strings.TrimRight(strings.TrimRight(formatFloat(t), "0"), "."))
		b.WriteString(" ")
	case bool:
		if t {
			b.WriteString("true ")
		} else {
			b.WriteString("false ")
		}
	case map[string]interface{}:
		for k, vv := range t {
			b.WriteString(k)
			b.WriteString(": ")
			flattenJSON(vv, b)
		}
	case []interface{}:
		for _, vv := range t {
			flattenJSON(vv, b)
		}
	}
}

func formatFloat(f float64) string {
	if f == float64(int64(f)) {
		return strings.TrimRight(strings.TrimRight(jsonNum(f), "0"), ".")
	}
	return jsonNum(f)
}
func jsonNum(f float64) string {
	bs, _ := json.Marshal(f)
	return string(bs)
}

// ── l3EgressInspect — orchestrator (port of workflow_gates.go:911) ─
// Returns map result with verdict, risk_score, matched_rule, flags, nl_preview,
// lt_raw.
//
// 2026-05-19 perf evolution:
//   - First fix dropped the LLM render entirely (Gemini render was 34s/call).
//   - Second fix (this) restores the render BUT forces it through Vultr (much
//     faster on this prompt per David's finding). Canonicalize still routes
//     via LT_LLM_PROVIDER (Gemini default). L3 latency now ~10-15s/node.
//
// Two LLM calls again, but parallelized: vultrRenderJSONasNL and the L3
// canonicalize inside ltInspectWithLLM run concurrently via sync.WaitGroup.
// max(render, canonicalize) ≈ canonicalize ≈ 10s, since Vultr render is
// ~3-5s while Gemini canonicalize is ~10s.
func l3EgressInspect(finalOutput interface{}, enableLLM bool) map[string]interface{} {
	t0 := time.Now()
	scrubbed := scrubExpectedBankingFields(finalOutput)
	rawJSON, _ := json.Marshal(scrubbed)
	log.Printf("[L3-EGRESS] start enableLLM=%v rawJSON=%d chars", enableLLM, len(rawJSON))

	// Run render (Vultr) and canonicalize (provider-routed) in parallel.
	// Render is non-blocking for the verdict: it only enriches the popup
	// preview. Canonicalize feeds the deterministic regex DPI as scan content.
	var renderedNL string
	var ci CanonicalIntent
	if enableLLM {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			renderedNL = vultrRenderJSONasNL(scrubbed)
		}()
		go func() {
			defer wg.Done()
			// Canonicalize the raw JSON (the render isn't ready yet — they're
			// parallel). Synthetic intent labels still come back useful for the
			// downstream regex scan.
			ci = ltSemanticCanonicalize(string(rawJSON))
		}()
		wg.Wait()
	}

	// Build scan content: rendered NL (when present) + raw JSON + canonical
	// intent labels (when present). Deterministic jsonToNL fallback if the
	// render LLM was unavailable.
	scanContent := string(rawJSON)
	if renderedNL != "" {
		scanContent = renderedNL + " " + scanContent
	} else {
		scanContent = jsonToNL(scrubbed) + " " + scanContent
	}
	if extra := canonicalIntentScanText(ci); extra != "" {
		scanContent += "\n\n[LLM_CANONICAL_INTENT]\n" + extra
	}
	scanContent = l3DecodeStego(scanContent)
	preview := scanContent
	if len(preview) > 300 {
		preview = preview[:300] + "..."
	}
	if egressRule, matched := l3EgressPreScan(scanContent); matched {
		log.Printf("[L3-EGRESS] DENY via pre-scan rule=%s elapsed=%dms", egressRule, time.Since(t0).Milliseconds())
		return map[string]interface{}{
			"verdict":      "DENY",
			"risk_score":   0.85,
			"matched_rule": egressRule,
			"flags":        []string{"egress pre-scan"},
			"deny_message": "[L3 EGRESS] Blocked: " + egressRule,
			"nl_preview":   preview,
			"rendered_nl":  renderedNL,
		}
	}
	// Pure-pattern ltInspect — LLM canonicalize already happened in the parallel
	// goroutine above, so no need to fire a second LLM call here.
	lt := ltInspect(scanContent)
	log.Printf("[L3-EGRESS] %s rule=%s risk=%.2f elapsed=%dms render=%d intent_labels=%d",
		lt.Verdict, lt.MatchedRule, lt.RiskScore, time.Since(t0).Milliseconds(),
		len(renderedNL), len(ci.IntentLabels))
	if len(ci.IntentLabels) > 0 {
		lt.Flags = append(lt.Flags, "llm_intent:"+strings.Join(ci.IntentLabels, ","))
	}
	resp := map[string]interface{}{
		"verdict":      lt.Verdict,
		"risk_score":   lt.RiskScore,
		"matched_rule": lt.MatchedRule,
		"flags":        lt.Flags,
		"deny_message": lt.DenyMessage,
		"nl_preview":   preview,
		"rendered_nl":  renderedNL,
	}
	if lt.Raw != nil {
		resp["lt_raw"] = json.RawMessage(lt.Raw)
	}
	return resp
}
