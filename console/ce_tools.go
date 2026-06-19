package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ── CE Tool Registry ─────────────────────────────────────────────
//
// WP-AO-53 Unit 3 — function-call tools exposed to the CE Executor.
//
// Tools available to a CE invocation depend on what tables the project's
// SQLite DB holds (U2). Static tools (now_utc8) are always available;
// per-table tools (query_<table>, count_<table>) are emitted dynamically.
//
// Tool params follow OpenAI / DeepSeek-V3 JSON-schema shape so they can
// be passed through to vultrChatWithTools (U4) without further translation.

// ToolDef is the public envelope for one callable tool. Handler runs against
// the project DB (resolved lazily inside the handler from projectSlug).
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // JSON Schema for args
	Handler     func(ctx context.Context, projectSlug string, args map[string]any) (any, error) `json:"-"`
}

// nowUTC8Schema declares the params for the no-arg now_utc8 tool.
var nowUTC8Schema = json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`)

// staticTools are always registered regardless of project DB state.
func staticTools() []ToolDef {
	return []ToolDef{
		{
			Name:        "now_utc8",
			Description: "Returns the current server-side wall-clock time in UTC+8 (Singapore/Beijing). Format: ISO 8601 with explicit offset, e.g. \"2026-05-18T16:00:00+08:00\". Use this for any temporal check (claim_date == today, age computation, submission lag).",
			Parameters:  nowUTC8Schema,
			Handler: func(ctx context.Context, projectSlug string, args map[string]any) (any, error) {
				utc8 := time.FixedZone("UTC+8", 8*60*60)
				return map[string]any{
					"now": time.Now().In(utc8).Format("2006-01-02T15:04:05-07:00"),
				}, nil
			},
		},
	}
}

// toolsForProject returns the full set of tools available to one CE invocation:
// staticTools + one `query_<table>` and one `count_<table>` per registered table.
// The list is sorted (deterministic order for the LLM prompt).
func toolsForProject(projectSlug string) ([]ToolDef, error) {
	out := staticTools()
	tables, err := listProjectTables(projectSlug)
	if err != nil {
		return out, fmt.Errorf("list tables: %w", err)
	}
	for _, t := range tables {
		cols, cerr := tableColumns(projectSlug, t)
		if cerr != nil {
			// Skip table — register no tools for it; LLM doesn't see it.
			continue
		}
		out = append(out, makeQueryTool(t, cols))
		out = append(out, makeCountTool(t, cols))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// makeQueryTool returns a ToolDef for `query_<table>(where, limit)`.
// Implementation: parameterized SELECT * FROM <table> WHERE k1=? AND k2=? ...
// Column whitelist enforced — caller cannot inject arbitrary identifiers.
func makeQueryTool(table string, cols []string) ToolDef {
	colSet := make(map[string]struct{}, len(cols))
	for _, c := range cols {
		colSet[c] = struct{}{}
	}

	whereSchema := buildWhereSchema(cols)
	paramsSchema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"where": whereSchema,
			"limit": map[string]any{
				"type":        "integer",
				"description": "Max rows to return (default 100, max 500).",
				"default":     100,
				"minimum":     1,
				"maximum":     500,
			},
		},
		"additionalProperties": false,
	})

	desc := fmt.Sprintf(
		"Query the %q table. Optional `where` filters rows by exact-match on whitelisted columns: %s. Returns up to `limit` rows as an array of objects.",
		table, strings.Join(cols, ", "),
	)

	return ToolDef{
		Name:        "query_" + table,
		Description: desc,
		Parameters:  json.RawMessage(paramsSchema),
		Handler: func(ctx context.Context, projectSlug string, args map[string]any) (any, error) {
			db, err := openProjectDB(projectSlug)
			if err != nil {
				return nil, err
			}
			whereSQL, whereArgs, werr := buildWhereClause(args["where"], colSet)
			if werr != nil {
				return nil, werr
			}
			limit := 100
			if lv, ok := args["limit"].(float64); ok {
				limit = int(lv)
			}
			if limit < 1 {
				limit = 100
			}
			if limit > 500 {
				limit = 500
			}
			sqlStr := fmt.Sprintf("SELECT * FROM %s %s LIMIT ?", quoteIdent(table), whereSQL)
			rows, err := db.QueryContext(ctx, sqlStr, append(whereArgs, limit)...)
			if err != nil {
				return nil, fmt.Errorf("query %s: %w", table, err)
			}
			defer rows.Close()
			return scanAllRows(rows)
		},
	}
}

// makeCountTool returns a ToolDef for `count_<table>(where)`.
func makeCountTool(table string, cols []string) ToolDef {
	colSet := make(map[string]struct{}, len(cols))
	for _, c := range cols {
		colSet[c] = struct{}{}
	}
	whereSchema := buildWhereSchema(cols)
	paramsSchema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"where": whereSchema,
		},
		"additionalProperties": false,
	})

	desc := fmt.Sprintf(
		"Count rows in the %q table matching `where` (exact-match on whitelisted columns: %s). Returns {count: int}.",
		table, strings.Join(cols, ", "),
	)

	return ToolDef{
		Name:        "count_" + table,
		Description: desc,
		Parameters:  json.RawMessage(paramsSchema),
		Handler: func(ctx context.Context, projectSlug string, args map[string]any) (any, error) {
			db, err := openProjectDB(projectSlug)
			if err != nil {
				return nil, err
			}
			whereSQL, whereArgs, werr := buildWhereClause(args["where"], colSet)
			if werr != nil {
				return nil, werr
			}
			sqlStr := fmt.Sprintf("SELECT COUNT(*) FROM %s %s", quoteIdent(table), whereSQL)
			var n int
			row := db.QueryRowContext(ctx, sqlStr, whereArgs...)
			if err := row.Scan(&n); err != nil {
				return nil, fmt.Errorf("count %s: %w", table, err)
			}
			return map[string]any{"count": n}, nil
		},
	}
}

// buildWhereSchema returns a JSON-schema object with one property per column
// so the LLM sees which keys are valid for `where`.
func buildWhereSchema(cols []string) map[string]any {
	props := map[string]any{}
	for _, c := range cols {
		props[c] = map[string]any{
			"description": fmt.Sprintf("Exact-match filter on column %q.", c),
		}
	}
	return map[string]any{
		"type":                 "object",
		"description":          "Exact-match WHERE filter. Keys must be whitelisted column names; values are compared with =.",
		"properties":           props,
		"additionalProperties": false,
	}
}

// buildWhereClause builds " WHERE k1=? AND k2=? ..." from a map[string]any with
// column-whitelist enforcement. Returns (sql, args, error). Empty filter → "".
func buildWhereClause(raw any, colSet map[string]struct{}) (string, []any, error) {
	if raw == nil {
		return "", nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return "", nil, fmt.Errorf("where must be an object")
	}
	if len(m) == 0 {
		return "", nil, nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		if _, ok := colSet[k]; !ok {
			return "", nil, fmt.Errorf("where references unknown column %q", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic clause order
	conds := make([]string, 0, len(keys))
	args := make([]any, 0, len(keys))
	for _, k := range keys {
		conds = append(conds, fmt.Sprintf("%s = ?", quoteIdent(k)))
		args = append(args, normalizeWhereArg(m[k]))
	}
	return "WHERE " + strings.Join(conds, " AND "), args, nil
}

// normalizeWhereArg coerces JSON-decoded `any` (bool, float64, string, nil)
// into a value SQLite's driver accepts.
func normalizeWhereArg(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case bool:
		if t {
			return 1
		}
		return 0
	case float64:
		// SQLite columns might be INTEGER or REAL — driver handles both.
		if t == float64(int64(t)) {
			return int64(t)
		}
		return t
	case string:
		return t
	default:
		// Fall back to string-coerce
		return fmt.Sprintf("%v", v)
	}
}

// scanAllRows reads every row from rs into []map[string]any.
func scanAllRows(rs *sql.Rows) ([]map[string]any, error) {
	cols, err := rs.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			row[c] = decodeSQLiteValue(vals[i])
		}
		out = append(out, row)
	}
	return out, rs.Err()
}

// decodeSQLiteValue normalizes SQLite-driver-returned types into JSON-friendly Go.
// modernc.org/sqlite returns int64 / float64 / string / []byte / time.Time / nil.
func decodeSQLiteValue(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
		// Try JSON-decoded object/array first (we stored those as JSON strings).
		s := string(t)
		if (strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")) ||
			(strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")) {
			var parsed any
			if err := json.Unmarshal(t, &parsed); err == nil {
				return parsed
			}
		}
		return s
	case string:
		if (strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}")) ||
			(strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]")) {
			var parsed any
			if err := json.Unmarshal([]byte(t), &parsed); err == nil {
				return parsed
			}
		}
		return t
	case int64:
		return t
	case float64:
		return t
	case bool:
		return t
	case time.Time:
		return t.Format(time.RFC3339)
	default:
		return v
	}
}

// renderToolSummary produces a short string used in trace events: "1 row · 42ms"
// or "count: 5" or "now: 2026-05-18T16:00:00+08:00". Keeps the SSE payload light.
func renderToolSummary(toolName string, result any) string {
	switch {
	case strings.HasPrefix(toolName, "query_"):
		if rows, ok := result.([]map[string]any); ok {
			return strconv.Itoa(len(rows)) + " row" + plural(len(rows))
		}
	case strings.HasPrefix(toolName, "count_"):
		if m, ok := result.(map[string]any); ok {
			if c, ok := m["count"]; ok {
				return "count: " + fmt.Sprintf("%v", c)
			}
		}
	case toolName == "now_utc8":
		if m, ok := result.(map[string]any); ok {
			if n, ok := m["now"]; ok {
				return "now: " + fmt.Sprintf("%v", n)
			}
		}
	}
	return "ok"
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
