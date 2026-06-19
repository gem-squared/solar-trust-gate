package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ── Retrieval-Augmented L1/L2 Audit ──────────────────────────────
//
// WP-AO-54 — populates the audit-gate p[] array with retrieved evidence
// BEFORE callPCheck / callOCheck fires. The audit-gate LLM then reasons
// over rules + evidence in one shot, citing named ledger rows and named
// regulations in its reasons[] response.
//
// L1: ledgerCheckForStage — queries the project SQLite DB (WP-AO-53) for
//     rows that match the runtime input's keys. Returns "[Ledger Evidence:<table>] ..."
//     strings ready to append to l1Req.P.
//
// L2: complianceCheckForStage — loads a per-workflow compliance corpus
//     (JSON of {id, stage, text} regulation snippets), scores each via
//     pure-Go unique-token-overlap against the output, returns top-k
//     formatted as "[Compliance <id>] ..." strings ready for l2Req.P.
//
// Both retrievers are non-fatal: on DB/corpus absence they return nil and
// log a warning. The audit proceeds with original rules-only P[]. Audit
// gates still fire; they just have less evidence to reason over.

const (
	retrievalKDefault = 3
	retrievalKMin     = 0
	retrievalKMax     = 5
	evidenceFieldCap  = 200 // per-row formatted-string char cap
	complianceCap     = 250 // per-snippet text char cap
)

// retrievalK reads GEM2_RETRIEVAL_K env var. 0 disables retrieval entirely;
// default 3; max 5. Out-of-range values clamp to the boundary.
func retrievalK() int {
	v := os.Getenv("GEM2_RETRIEVAL_K")
	if v == "" {
		return retrievalKDefault
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("[RETRIEVAL] invalid GEM2_RETRIEVAL_K=%q, using default %d", v, retrievalKDefault)
		return retrievalKDefault
	}
	if n < retrievalKMin {
		return retrievalKMin
	}
	if n > retrievalKMax {
		return retrievalKMax
	}
	return n
}

// ─── L1: ledger evidence ────────────────────────────────────────────

// ledgerCheckForStage queries the project SQLite ledger for top-k rows
// matching the input. Returns formatted evidence strings ready for l1Req.P.
// k=0 disables retrieval; nil/empty returns mean no evidence (audit proceeds
// with original P_pre rules only).
func ledgerCheckForStage(ctx context.Context, workflowSlug, stageSlug string, input any, k int) []string {
	if k <= 0 {
		return nil
	}
	inputMap, ok := input.(map[string]any)
	if !ok || len(inputMap) == 0 {
		return nil
	}

	// Load this stage's CESpec to learn which tables are configured as
	// reference data — that's our candidate-table set.
	spec, err := loadCESpec(workflowSlug, stageSlug)
	if err != nil || spec == nil || len(spec.ReferenceData) == 0 {
		return nil
	}

	// Open the project DB (WP-AO-53). project_slug == workflow_slug for demo.
	db, err := openProjectDB(workflowSlug)
	if err != nil {
		log.Printf("[LEDGER-EVI] openProjectDB(%s) failed: %v", workflowSlug, err)
		return nil
	}

	type candidate struct {
		table   string
		whereK  []string // intersection of input keys × table columns
	}
	var candidates []candidate
	for tableName := range spec.ReferenceData {
		if !validTableName(tableName) {
			continue
		}
		cols, cerr := tableColumns(workflowSlug, tableName)
		if cerr != nil || len(cols) == 0 {
			continue
		}
		colSet := make(map[string]struct{}, len(cols))
		for _, c := range cols {
			colSet[c] = struct{}{}
		}
		var match []string
		for k := range inputMap {
			if _, ok := colSet[k]; ok {
				match = append(match, k)
			}
		}
		if len(match) > 0 {
			candidates = append(candidates, candidate{table: tableName, whereK: match})
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	// Prefer tables with more matching keys (more relevant retrieval).
	sort.Slice(candidates, func(i, j int) bool {
		if len(candidates[i].whereK) != len(candidates[j].whereK) {
			return len(candidates[i].whereK) > len(candidates[j].whereK)
		}
		return candidates[i].table < candidates[j].table
	})

	var out []string
	remaining := k
	for _, cand := range candidates {
		if remaining <= 0 {
			break
		}
		sort.Strings(cand.whereK)
		conds := make([]string, 0, len(cand.whereK))
		args := make([]any, 0, len(cand.whereK))
		for _, key := range cand.whereK {
			conds = append(conds, fmt.Sprintf("%s = ?", quoteIdent(key)))
			args = append(args, normalizeWhereArg(inputMap[key]))
		}
		whereSQL := "WHERE " + strings.Join(conds, " AND ")
		sqlStr := fmt.Sprintf("SELECT * FROM %s %s LIMIT %d", quoteIdent(cand.table), whereSQL, remaining)
		rows, qerr := db.QueryContext(ctx, sqlStr, args...)
		if qerr != nil {
			log.Printf("[LEDGER-EVI] query %s failed: %v", cand.table, qerr)
			continue
		}
		fetched, serr := scanAllRows(rows)
		_ = rows.Close()
		if serr != nil {
			log.Printf("[LEDGER-EVI] scan %s failed: %v", cand.table, serr)
			continue
		}
		for _, row := range fetched {
			if remaining <= 0 {
				break
			}
			out = append(out, formatLedgerRow(cand.table, row))
			remaining--
		}
	}
	if len(out) == 0 {
		log.Printf("[LEDGER-EVI] %s/%s no matching rows across %d candidate tables", workflowSlug, stageSlug, len(candidates))
	} else {
		log.Printf("[LEDGER-EVI] %s/%s retrieved %d evidence rows", workflowSlug, stageSlug, len(out))
	}
	return out
}

// formatLedgerRow turns a SQLite row into a compact evidence string.
// "[Ledger Evidence:policies] policy_no=HIC-2024-00123 status=active product=COMP-HEALTH-GOLD"
// Skips internal _comment / _scenario / _note fields that exist in the JSON sources.
func formatLedgerRow(table string, row map[string]any) string {
	keys := make([]string, 0, len(row))
	for k := range row {
		if strings.HasPrefix(k, "_") {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := row[k]
		if v == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%v", k, compactValue(v)))
	}
	s := fmt.Sprintf("[Ledger Evidence:%s] %s", table, strings.Join(parts, " "))
	if len(s) > evidenceFieldCap {
		s = s[:evidenceFieldCap-1] + "…"
	}
	return s
}

func compactValue(v any) string {
	switch t := v.(type) {
	case string:
		if len(t) > 40 {
			return t[:39] + "…"
		}
		return t
	case []any, map[string]any:
		b, _ := json.Marshal(v)
		s := string(b)
		if len(s) > 40 {
			s = s[:39] + "…"
		}
		return s
	default:
		return fmt.Sprintf("%v", v)
	}
}

// ─── L2: compliance evidence ────────────────────────────────────────

type ComplianceSnippet struct {
	ID    string `json:"id"`
	Stage string `json:"stage"`
	Text  string `json:"text"`
}

// loadComplianceCorpus prefers the workspace copy at
// .gem-squared/workspace/{projectSlug}/compliance.json (written by
// demo_bootstrap). Falls back to the embedded copy. Empty/missing → nil.
func loadComplianceCorpus(projectSlug string) []ComplianceSnippet {
	wsPath := filepath.Join(baseDir, ".gem-squared", "workspace", projectSlug, "compliance.json")
	if data, err := os.ReadFile(wsPath); err == nil {
		var corpus []ComplianceSnippet
		if jerr := json.Unmarshal(data, &corpus); jerr == nil {
			return corpus
		}
	}
	// Fallback: merge embedded compliance/*.json from demo_bootstrap.go's
	// demoAssetsFS. WP-AO-65 split the flat compliance.json into per-stage
	// files; loadComplianceCorpus reconstructs the merged corpus on demand
	// so the existing scorer logic doesn't have to change.
	merged, _, mErr := mergeEmbeddedCompliance()
	if mErr != nil {
		return nil
	}
	var corpus []ComplianceSnippet
	if jerr := json.Unmarshal(merged, &corpus); jerr != nil {
		return nil
	}
	return corpus
}

// complianceCheckForStage scores corpus snippets against the output's
// keys+values via unique-token-overlap. Returns top-k formatted strings
// ready for l2Req.P. k=0 disables retrieval.
func complianceCheckForStage(ctx context.Context, workflowSlug, stageSlug string, output any, k int) []string {
	if k <= 0 {
		return nil
	}
	corpus := loadComplianceCorpus(workflowSlug)
	if len(corpus) == 0 {
		return nil
	}

	// Filter: stage-bound OR workflow-wide ("*")
	var candidates []ComplianceSnippet
	for _, s := range corpus {
		if s.Stage == stageSlug || s.Stage == "*" {
			candidates = append(candidates, s)
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	outputTokens := tokenizeEvidence(jsonFlatten(output))
	if len(outputTokens) == 0 {
		// Even with no usable output tokens, return the top stage-bound rules
		// (better than nothing — at least the audit cites them).
		var fallback []string
		for i, s := range candidates {
			if i >= k {
				break
			}
			fallback = append(fallback, formatCompliance(s))
		}
		return fallback
	}

	type scored struct {
		snippet ComplianceSnippet
		score   int
	}
	var ranked []scored
	for _, s := range candidates {
		srcTokens := tokenizeEvidence(s.Text + " " + s.ID)
		ranked = append(ranked, scored{snippet: s, score: tokenOverlap(srcTokens, outputTokens)})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		// Tiebreak: stage-specific beats workflow-wide (more targeted)
		if ranked[i].snippet.Stage != ranked[j].snippet.Stage {
			if ranked[i].snippet.Stage == "*" {
				return false
			}
			if ranked[j].snippet.Stage == "*" {
				return true
			}
		}
		return ranked[i].snippet.ID < ranked[j].snippet.ID
	})

	out := make([]string, 0, k)
	for i, r := range ranked {
		if i >= k {
			break
		}
		out = append(out, formatCompliance(r.snippet))
	}
	log.Printf("[COMPLIANCE-EVI] %s/%s ranked %d → returning %d", workflowSlug, stageSlug, len(ranked), len(out))
	return out
}

func formatCompliance(s ComplianceSnippet) string {
	text := s.Text
	if len(text) > complianceCap {
		text = text[:complianceCap-1] + "…"
	}
	return fmt.Sprintf("[Compliance %s] %s", s.ID, text)
}

// ─── tokenization helpers ───────────────────────────────────────────

var stopwords = map[string]struct{}{
	"the": {}, "and": {}, "for": {}, "with": {}, "from": {}, "this": {}, "that": {},
	"per": {}, "any": {}, "all": {}, "must": {}, "not": {}, "into": {}, "are": {},
	"its": {}, "has": {}, "have": {}, "was": {}, "were": {}, "been": {}, "shall": {},
	"will": {}, "would": {}, "should": {}, "could": {}, "out": {}, "off": {}, "via": {},
	"each": {}, "such": {}, "than": {}, "then": {}, "but": {}, "etc": {},
	"only": {}, "also": {}, "now": {}, "may": {}, "their": {}, "they": {}, "them": {},
}

// tokenize lowercases, splits on non-alphanumeric, dedupes, filters short + stopwords.
func tokenizeEvidence(s string) map[string]struct{} {
	out := make(map[string]struct{})
	var cur strings.Builder
	flush := func() {
		w := cur.String()
		cur.Reset()
		if len(w) < 3 {
			return
		}
		if _, isStop := stopwords[w]; isStop {
			return
		}
		out[w] = struct{}{}
	}
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

// jsonFlatten serializes any Go value to a JSON string so we can tokenize
// both keys and values uniformly. Falls back to fmt.Sprint on marshal error.
func jsonFlatten(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func tokenOverlap(a, b map[string]struct{}) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	// Iterate the smaller set
	small, large := a, b
	if len(b) < len(a) {
		small, large = b, a
	}
	n := 0
	for k := range small {
		if _, ok := large[k]; ok {
			n++
		}
	}
	return n
}
