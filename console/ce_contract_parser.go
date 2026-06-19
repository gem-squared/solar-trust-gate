package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ── TPMN Contract Parser ───────────────────────────────────────────
//
// Parses TPMN contracts authored per Docs/contract-authoring-guide.md (the
// post-WP-AO-24 split format) into a CESpec. Rejects contracts authored in
// the old conflated A/F/B/P format by returning a `conflatedContractError`.
//
// Expected contract structure:
//   # {title}
//   **Workflow:** {workflow name}
//   **Domain:** {domain}                  (optional)
//   **Contract:** `{stage_slug}: A → B | P_pre ⊕ P_post`
//
//   ## A: Input
//     ...YAML schema...
//
//   ## P_pre: Preconditions
//     ### Type Alignment
//       - ...
//     ### Format Validation
//       - ...
//     ### Regulation/Compliance Gates
//       - ...
//
//   ## F: Processing Logic
//     1. ...
//
//   ## B: Output
//     ...
//
//   ## P_post: Postconditions
//     ### Correctness
//     ### Gates
//     ### SPT Compliance
//
//   ## Circus Executor
//     **stage_type:** ...
//     **agent_role:** ...
//     **trust_gate_L1:** N // comment
//     **trust_gate_L2:** N // comment

type conflatedContractError struct {
	Detail string
}

func (e *conflatedContractError) Error() string {
	return "conflated contract — " + e.Detail
}

// parseContractFile reads a markdown file and returns the parsed CESpec.
// WP-AO-55 Unit 2: compares filename stem to the parsed stage_slug and logs
// a warning on mismatch. Contract-declared slug still wins (authoring
// authority), but mismatches usually indicate a copy-paste or rename
// oversight worth surfacing in operator logs.
func parseContractFile(path string) (*CESpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read contract %s: %w", path, err)
	}
	spec, err := parseContractMarkdown(string(data))
	if err != nil {
		return nil, err
	}
	spec.SourceFile = path

	// Sanity guard: filename-vs-slug compare.
	// Filename `claim-01-intake.md` → stem `claim-01-intake` → compare against
	// spec.StageSlug. Differ → warn (non-fatal).
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if stem != "" && spec.StageSlug != "" && !strings.EqualFold(stem, spec.StageSlug) {
		log.Printf("[CE-PARSER] WARN filename slug %q differs from contract slug %q (path=%s) — using contract value as source of truth",
			stem, spec.StageSlug, path)
	}

	return spec, nil
}

// parseContractMarkdown is the main parser. Caller is responsible for
// setting SourceFile if needed.
func parseContractMarkdown(content string) (*CESpec, error) {
	requiredHeaders := []string{"## A:", "## P_pre:", "## F:", "## B:", "## P_post:"}
	for _, h := range requiredHeaders {
		if !containsHeader(content, h) {
			if h == "## P_pre:" || h == "## P_post:" {
				return nil, &conflatedContractError{
					Detail: fmt.Sprintf("missing %q — old A/F/B/P format detected. Split per Docs/contract-authoring-guide.md §2.", h),
				}
			}
			return nil, fmt.Errorf("missing required section %q", h)
		}
	}

	spec := &CESpec{}

	// 1. Title (first H1)
	if m := regexp.MustCompile(`(?m)^#\s+(.+)$`).FindStringSubmatch(content); len(m) > 1 {
		spec.ContractTitle = strings.TrimSpace(m[1])
	}

	// 2. Workflow / Domain metadata (lines before the first ## section)
	preBody := preludeOf(content)
	if m := regexp.MustCompile(`(?i)\*\*Workflow:\*\*\s*(.+)`).FindStringSubmatch(preBody); len(m) > 1 {
		spec.WorkflowSlug = kebabize(strings.TrimSpace(m[1]))
	}
	if m := regexp.MustCompile(`(?i)\*\*Domain:\*\*\s*(.+)`).FindStringSubmatch(preBody); len(m) > 1 {
		spec.Domain = strings.TrimSpace(m[1])
	}

	// 3. Contract signature → stage slug
	if m := regexp.MustCompile("`([a-z0-9_-]+):\\s*A").FindStringSubmatch(preBody); len(m) > 1 {
		spec.StageSlug = kebabize(m[1])
	}

	if spec.WorkflowSlug == "" {
		spec.WorkflowSlug = "default"
	}
	if spec.StageSlug == "" {
		// fallback: derive from title
		spec.StageSlug = kebabize(spec.ContractTitle)
	}

	// 4. ## A: block
	aBlock := tpmnExtractSection(content, "## A:", nextHeading(content, "## A:"))
	spec.A = strings.TrimSpace(stripCodeFences(aBlock))
	if spec.A == "" {
		return nil, fmt.Errorf("## A: block is empty")
	}

	// 5. ## P_pre: block with grouped subheadings
	pPreBlock := tpmnExtractSection(content, "## P_pre:", nextHeading(content, "## P_pre:"))
	spec.PPre = flattenGroupedChecklist(pPreBlock)

	// 6. ## F: block — VERBATIM (the system prompt body)
	fBlock := tpmnExtractSection(content, "## F:", nextHeading(content, "## F:"))
	spec.FBlock = strings.TrimSpace(fBlock)
	if spec.FBlock == "" {
		return nil, fmt.Errorf("## F: block is empty")
	}

	// 7. ## B: block
	bBlock := tpmnExtractSection(content, "## B:", nextHeading(content, "## B:"))
	spec.B = strings.TrimSpace(stripCodeFences(bBlock))
	if spec.B == "" {
		return nil, fmt.Errorf("## B: block is empty")
	}

	// 8. ## P_post: block with grouped subheadings
	pPostBlock := tpmnExtractSection(content, "## P_post:", nextHeading(content, "## P_post:"))
	spec.PPost = flattenGroupedChecklist(pPostBlock)

	// 9. ## Circus Executor metadata
	cxBlock := tpmnExtractSection(content, "## Circus Executor", nextHeading(content, "## Circus Executor"))
	spec.StageType = extractKeyValue(cxBlock, "stage_type")
	spec.AgentRole = extractKeyValue(cxBlock, "agent_role")
	// L0/L1/L2/L3 trust-gate thresholds. WP-01 U6: L0/L3 default to 60 when
	// the contract's Circus Executor block doesn't name them (most existing
	// contracts pre-LT integration). 0 explicitly disables that layer.
	if t0 := extractIntValue(cxBlock, "trust_gate_L0"); t0 >= 0 {
		spec.TrustGateL0 = t0
	} else {
		spec.TrustGateL0 = 60
	}
	if t1 := extractIntValue(cxBlock, "trust_gate_L1"); t1 >= 0 {
		spec.TrustGateL1 = t1
	}
	if t2 := extractIntValue(cxBlock, "trust_gate_L2"); t2 >= 0 {
		spec.TrustGateL2 = t2
	}
	if t3 := extractIntValue(cxBlock, "trust_gate_L3"); t3 >= 0 {
		spec.TrustGateL3 = t3
	} else {
		spec.TrustGateL3 = 60
	}
	if vm := extractKeyValue(cxBlock, "vultr_model"); vm != "" {
		spec.VultrModel = vm
	}

	// 10. Auto-extract ReferenceData table names from F-block.
	// WP-AO-65: compact contracts mention SQLite-backed tables in prose
	// like "query_policies(where:...)" and "count_premium_ledger(...)".
	// CE Runtime v2's prefetchEvidence + L1's ledgerCheckForStage gate on
	// len(spec.ReferenceData) — without this extraction those paths skip
	// all DB queries and L1 evidence is empty. Values left as "" (legacy
	// inline-rows JSON unused in v2 — SQLite has the rows now).
	spec.ReferenceData = extractReferenceTables(spec.FBlock)

	return spec, nil
}

// extractReferenceTables scans an F-block for table references and returns
// a deduplicated map of table names → "" (empty string; v2 prefetch only
// uses the keys). Three patterns are matched:
//
//   1. `query_<table>(...)`  → tool-call form, e.g. query_policies(...)
//   2. `count_<table>(...)`  → tool-call form, e.g. count_premium_ledger(...)
//   3. `from \`<table>\``    → SQL-prose form, e.g. `SUM(x) ... from \`claim_utilisation\` where ...`
//
// Pattern 3 was added after WP-AO-65: the eligibility-check contract uses
// `SUM(net_payable) ... from \`claim_utilisation\` where (...)` rather than
// query_claim_utilisation(), which the original regex missed → prefetch
// skipped the table → Vultr emitted null annual_utilised → L2 false-failed.
// Table name must be lowercase ascii+underscores (matches validTableName).
func extractReferenceTables(fBlock string) map[string]string {
	if fBlock == "" {
		return nil
	}
	patterns := []*regexp.Regexp{
		// \b prevents matching inside 'bank_account_no' where 'count_no'
		// appears as a substring → bogus 'no' table capture.
		regexp.MustCompile(`\b(?:query|count)_([a-z][a-z0-9_]*)`),
		regexp.MustCompile("from\\s+`([a-z][a-z0-9_]*)`"),
	}
	out := map[string]string{}
	for _, re := range patterns {
		for _, m := range re.FindAllStringSubmatch(fBlock, -1) {
			if len(m) > 1 && m[1] != "" {
				out[m[1]] = ""
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ── helpers ────────────────────────────────────────────────────────

// containsHeader returns true if `content` has a line that starts with `header`.
func containsHeader(content, header string) bool {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(header))
	return re.MatchString(content)
}

// preludeOf returns the lines BEFORE the first `## ` heading.
func preludeOf(content string) string {
	idx := strings.Index(content, "\n## ")
	if idx < 0 {
		return content
	}
	return content[:idx]
}

// tpmnExtractSection returns the text BETWEEN `start` heading and the offset
// `stopIdx` (exclusive). If stopIdx < 0, returns from start to EOF.
func tpmnExtractSection(content, startHeader string, stopIdx int) string {
	startRe := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(startHeader))
	loc := startRe.FindStringIndex(content)
	if loc == nil {
		return ""
	}
	// skip the heading line itself
	begin := loc[1]
	if nl := strings.IndexByte(content[begin:], '\n'); nl >= 0 {
		begin = begin + nl + 1
	}
	if stopIdx < 0 || stopIdx <= begin {
		return content[begin:]
	}
	return content[begin:stopIdx]
}

// nextHeading returns the byte offset of the NEXT `## ` heading after `header`,
// or -1 if no further heading exists.
func nextHeading(content, header string) int {
	startRe := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(header))
	loc := startRe.FindStringIndex(content)
	if loc == nil {
		return -1
	}
	tail := content[loc[1]:]
	nextRe := regexp.MustCompile(`(?m)^## `)
	nextLoc := nextRe.FindStringIndex(tail)
	if nextLoc == nil {
		return -1
	}
	return loc[1] + nextLoc[0]
}

// stripCodeFences removes leading/trailing ```...``` fences if present.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if firstNL := strings.IndexByte(s, '\n'); firstNL >= 0 {
			s = s[firstNL+1:]
		}
		if idx := strings.LastIndex(s, "```"); idx >= 0 {
			s = s[:idx]
		}
	}
	return strings.TrimSpace(s)
}

// flattenGroupedChecklist walks a P_pre or P_post block looking for
// ### Subgroup heading followed by `- bullet` lines, and emits each bullet
// prefixed with "[Subgroup] ". Used to flatten the human-friendly authoring
// format into the Seq(𝕊) the audit-gate API expects (per WP-AO-24 §6.1/§6.3).
func flattenGroupedChecklist(block string) []string {
	var out []string
	currentGroup := ""
	bulletRe := regexp.MustCompile(`(?m)^\s*-\s+(.+?)\s*$`)
	headingRe := regexp.MustCompile(`(?m)^###\s+(.+?)\s*$`)

	lines := strings.Split(block, "\n")
	for _, line := range lines {
		// Heading?
		if m := headingRe.FindStringSubmatch(line); len(m) > 1 {
			currentGroup = strings.TrimSpace(m[1])
			continue
		}
		// Bullet under the current group?
		if currentGroup == "" {
			continue
		}
		if m := bulletRe.FindStringSubmatch(line); len(m) > 1 {
			rule := strings.TrimSpace(m[1])
			// allow "(none)" — emit as a sentinel so SaaS sees the group
			if rule == "" {
				continue
			}
			out = append(out, fmt.Sprintf("[%s] %s", currentGroup, rule))
		}
	}
	return out
}

// extractKeyValue looks for `**key:** value` or `**key:** value // comment`
// inside the Circus Executor block. Returns trimmed value (without trailing
// // comment). Returns empty string if not found.
func extractKeyValue(block, key string) string {
	pattern := `(?m)^\*\*` + regexp.QuoteMeta(key) + `:\*\*\s*(.+?)\s*$`
	re := regexp.MustCompile(pattern)
	if m := re.FindStringSubmatch(block); len(m) > 1 {
		val := strings.TrimSpace(m[1])
		// Strip italicized // comment ("// ..." or "*// ...*")
		if idx := strings.Index(val, "//"); idx >= 0 {
			val = strings.TrimSpace(val[:idx])
		}
		// Strip surrounding *...* italics if present
		val = strings.Trim(val, "* \t")
		return val
	}
	return ""
}

// extractIntValue parses the integer at the start of a key's value.
// Returns -1 if not found / not parseable.
func extractIntValue(block, key string) int {
	raw := extractKeyValue(block, key)
	if raw == "" {
		return -1
	}
	// Take leading digits
	digits := ""
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			digits += string(r)
		} else {
			break
		}
	}
	if digits == "" {
		return -1
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		return -1
	}
	return n
}

// kebabize converts free text to kebab-case for slug use.
// "Insurance Claims Workflow" → "insurance-claims-workflow"
// "payout_calculation" → "payout-calculation"
func kebabize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// Replace non-alphanumeric runs with single hyphen
	re := regexp.MustCompile(`[^a-z0-9]+`)
	s = re.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 64 {
		s = s[:64]
		s = strings.Trim(s, "-")
	}
	return s
}
