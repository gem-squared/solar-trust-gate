package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"
)

// ── WP-AO-63: CE Runtime v2 ──────────────────────────────────────
//
// CE_Runtime_v2 ≜ [
//   L1: audit input against P_pre,
//   ToolPrefetch: deterministic table/tool execution from contract metadata,
//   Optional_Gemini_Orchestrator: only if prefetch plan is ambiguous (DEFERRED),
//   Vultr_CE: text-only LLM receives tool results as evidence,
//             must output pure JSON B,
//   L2: audit output against P_post + evidence
// ]
//
// v1 (vultrToolCallLoop): LLM decides which tools to call. Multi-turn.
// v2 (this file):         Server pre-executes tools deterministically.
//                         Single Vultr call. LLM only formats evidence into JSON B.
//
// Switch via env var GEM2_CE_RUNTIME:
//   "v2" (default) → prefetch + single shot
//   "v1"           → reverts to vultrToolCallLoop

// ceRuntimeVersion returns "v1" or "v2" (default v2 per WP-AO-63).
func ceRuntimeVersion() string {
	switch os.Getenv("GEM2_CE_RUNTIME") {
	case "v1":
		return "v1"
	default:
		return "v2"
	}
}

// prefetchEvidence runs the deterministic tool pre-execution: now_utc8 plus
// a query_<table> per project table whose columns intersect the input's keys.
// Returns trace events (for the UI) and structured payload (for the Vultr prompt).
func prefetchEvidence(ctx context.Context, spec *CESpec, input any) ([]ToolCallTrace, []map[string]any, error) {
	trace := make([]ToolCallTrace, 0, 4)
	structured := make([]map[string]any, 0, 4)

	// 1. now_utc8 — always called so date-based output fields can use today's year.
	nowStart := time.Now()
	utc8 := time.FixedZone("UTC+8", 8*60*60)
	nowISO := time.Now().In(utc8).Format("2006-01-02T15:04:05-07:00")
	nowResult := map[string]any{"now": nowISO}
	trace = append(trace, ToolCallTrace{
		Tool:      "now_utc8",
		Args:      map[string]any{},
		Summary:   "now: " + nowISO,
		LatencyMs: time.Since(nowStart).Milliseconds(),
	})
	structured = append(structured, map[string]any{
		"tool":    "now_utc8",
		"args":    map[string]any{},
		"result":  nowResult,
	})

	// 2. Bail if no input map / no reference data — nothing else to prefetch.
	inputMap, ok := input.(map[string]any)
	if !ok || len(inputMap) == 0 || len(spec.ReferenceData) == 0 {
		log.Printf("[CE-V2-PREFETCH] BAIL stage=%s/%s ok=%v inputType=%T inputLen=%d refDataLen=%d",
			spec.WorkflowSlug, spec.StageSlug, ok, input, len(inputMap), len(spec.ReferenceData))
		return trace, structured, nil
	}
	log.Printf("[CE-V2-PREFETCH] START stage=%s/%s inputKeys=%d refTables=%d",
		spec.WorkflowSlug, spec.StageSlug, len(inputMap), len(spec.ReferenceData))

	// 3. Walk reference tables; for each, find input-key ∩ column-name overlap;
	//    execute query_<table>(where=intersection) for tables with ≥1 match.
	k := retrievalK()
	if k <= 0 {
		k = 3
	}

	type candidate struct {
		table  string
		whereK []string
	}
	var candidates []candidate
	for tableName := range spec.ReferenceData {
		if !validTableName(tableName) {
			log.Printf("[CE-V2-PREFETCH] skip table=%s reason=invalid_name", tableName)
			continue
		}
		cols, cerr := tableColumns(spec.WorkflowSlug, tableName)
		if cerr != nil {
			log.Printf("[CE-V2-PREFETCH] skip table=%s reason=tableColumns_err err=%v", tableName, cerr)
			continue
		}
		if len(cols) == 0 {
			log.Printf("[CE-V2-PREFETCH] skip table=%s reason=no_cols", tableName)
			continue
		}
		colSet := make(map[string]struct{}, len(cols))
		for _, c := range cols {
			colSet[c] = struct{}{}
		}
		var match []string
		for ik := range inputMap {
			if _, ok := colSet[ik]; ok {
				match = append(match, ik)
			}
		}
		log.Printf("[CE-V2-PREFETCH] table=%s cols=%d matchKeys=%v", tableName, len(cols), match)
		if len(match) > 0 {
			candidates = append(candidates, candidate{table: tableName, whereK: match})
		}
	}
	if len(candidates) == 0 {
		log.Printf("[CE-V2-PREFETCH] BAIL no_candidates stage=%s/%s refTables=%d",
			spec.WorkflowSlug, spec.StageSlug, len(spec.ReferenceData))
		return trace, structured, nil
	}
	log.Printf("[CE-V2-PREFETCH] candidates=%d for stage=%s/%s", len(candidates), spec.WorkflowSlug, spec.StageSlug)
	sort.Slice(candidates, func(i, j int) bool {
		if len(candidates[i].whereK) != len(candidates[j].whereK) {
			return len(candidates[i].whereK) > len(candidates[j].whereK)
		}
		return candidates[i].table < candidates[j].table
	})

	db, err := openProjectDB(spec.WorkflowSlug)
	if err != nil {
		log.Printf("[CE-V2-PREFETCH] openProjectDB(%s): %v — continuing with empty prefetch", spec.WorkflowSlug, err)
		return trace, structured, nil
	}

	for _, cand := range candidates {
		callStart := time.Now()
		sort.Strings(cand.whereK)
		conds := make([]string, 0, len(cand.whereK))
		args := make([]any, 0, len(cand.whereK))
		whereArgsForTrace := map[string]any{}
		for _, key := range cand.whereK {
			conds = append(conds, fmt.Sprintf("%s = ?", quoteIdent(key)))
			args = append(args, normalizeWhereArg(inputMap[key]))
			whereArgsForTrace[key] = inputMap[key]
		}
		whereSQL := "WHERE " + strings.Join(conds, " AND ")
		sqlStr := fmt.Sprintf("SELECT * FROM %s %s LIMIT ?", quoteIdent(cand.table), whereSQL)
		args = append(args, k)
		rows, qerr := db.QueryContext(ctx, sqlStr, args...)
		if qerr != nil {
			log.Printf("[CE-V2-PREFETCH] query %s: %v", cand.table, qerr)
			trace = append(trace, ToolCallTrace{
				Tool:      "query_" + cand.table,
				Args:      map[string]any{"where": whereArgsForTrace},
				Error:     qerr.Error(),
				LatencyMs: time.Since(callStart).Milliseconds(),
			})
			continue
		}
		fetched, serr := scanAllRows(rows)
		_ = rows.Close()
		if serr != nil {
			log.Printf("[CE-V2-PREFETCH] scan %s: %v", cand.table, serr)
			trace = append(trace, ToolCallTrace{
				Tool:      "query_" + cand.table,
				Args:      map[string]any{"where": whereArgsForTrace},
				Error:     serr.Error(),
				LatencyMs: time.Since(callStart).Milliseconds(),
			})
			continue
		}
		summary := fmt.Sprintf("%d row", len(fetched))
		if len(fetched) != 1 {
			summary += "s"
		}
		trace = append(trace, ToolCallTrace{
			Tool:      "query_" + cand.table,
			Args:      map[string]any{"where": whereArgsForTrace, "limit": k},
			Summary:   summary,
			LatencyMs: time.Since(callStart).Milliseconds(),
		})
		structured = append(structured, map[string]any{
			"tool":   "query_" + cand.table,
			"args":   map[string]any{"where": whereArgsForTrace},
			"result": map[string]any{"rows": fetched, "count": len(fetched)},
		})
	}

	return trace, structured, nil
}

// buildCEv2Prompts builds the v2 system + user prompts: NO tool-use directive
// (server already ran the tools); evidence inlined; JSON-only output mandate.
func buildCEv2Prompts(spec *CESpec, input any, prefetched []map[string]any) (string, string) {
	systemPrompt := fmt.Sprintf(
		"You are a Contract-Executor for %q. Apply the F: Processing Logic below to the provided input I, using the pre-fetched evidence supplied by the server. The server has already executed the relevant data lookups — DO NOT request or hallucinate additional queries. Use the evidence verbatim.\n\n## F: Processing Logic\n\n%s\n\n## Output schema (B)\n\n%s\n\n## DATE / TIMESTAMP FIELDS — CRITICAL\n\nDerive ALL `*_timestamp`, `*_date`, and date-based reference IDs (e.g. `claim_reference_draft` shaped like `DRAFT-YYYYMMDD-#####`) from the `now_utc8` evidence entry. NEVER copy a year from `policy_no` (e.g. `HIC-2024-00123` contains the policy-purchase year, NOT today's year). The 'YYYY' segment of any reference ID MUST equal the year inside `now_utc8.result.now`.\n\n## OUTPUT FORMAT — CRITICAL\n\nYour response MUST be a single JSON object matching schema B above. Do NOT include any prose, explanations, reasoning steps, or markdown around the JSON. Do NOT wrap it in ```json fences. Do NOT add fields outside schema B. Return JSON ONLY.",
		spec.ContractTitle, spec.FBlock, asString(spec.B))

	iJSON, _ := json.MarshalIndent(input, "", "  ")
	var ub strings.Builder
	ub.WriteString("## Input I\n\n```json\n")
	ub.Write(iJSON)
	ub.WriteString("\n```\n\n## Pre-fetched evidence\n\nThe server executed these tools BEFORE this call and supplies the results here. Use them as ground truth.\n\n```json\n")
	evJSON, _ := json.MarshalIndent(prefetched, "", "  ")
	ub.Write(evJSON)
	ub.WriteString("\n```\n\nProduce the JSON output now.")
	return systemPrompt, ub.String()
}

// runCEv2 is the shared v2 execution: prefetch → Solar Pro 3 → parse JSON.
// Returns the parsed output (or raw text on parse failure), the trace events,
// and the prefetch + LLM call durations.
func runCEv2(ctx context.Context, spec *CESpec, input any) (parsed any, raw string, trace []ToolCallTrace, durations map[string]int64, err error) {
	durations = map[string]int64{}

	prefetchStart := time.Now()
	traceLocal, structured, _ := prefetchEvidence(ctx, spec, input)
	durations["prefetch_ms"] = time.Since(prefetchStart).Milliseconds()

	systemPrompt, userPrompt := buildCEv2Prompts(spec, input, structured)
	model := envOr("UPSTAGE_MODEL", solarAuditModel)
	messages := []map[string]string{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": userPrompt},
	}

	execStart := time.Now()
	rawText, callErr := solarChatMessages(model, messages, 90*time.Second)
	durations["exec_ms"] = time.Since(execStart).Milliseconds()
	if callErr != nil {
		return nil, "", traceLocal, durations, callErr
	}

	stripped := stripCodeFencesForJSON(rawText)
	if jerr := json.Unmarshal([]byte(stripped), &parsed); jerr != nil {
		return nil, rawText, traceLocal, durations, fmt.Errorf("LLM output is not valid JSON: %w", jerr)
	}
	return parsed, rawText, traceLocal, durations, nil
}
