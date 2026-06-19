package main

// WP-01 — GET /api/audit-log query endpoint for the layer_audit_log table.
//
// Lets the canvas UI surface a "regulator-readable" view of every L0/L1/L2/L3
// verdict logged during workflow runs. Filters by workflow_slug (which keys
// the per-project SQLite file), layer, verdict, run_id, with a row limit.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// auditLogRow mirrors one layer_audit_log row.
type auditLogRow struct {
	ID          int64   `json:"id"`
	TS          string  `json:"ts"`
	RunID       string  `json:"run_id"`
	StageID     int     `json:"stage_id"`
	CESlug      string  `json:"ce_slug"`
	Layer       string  `json:"layer"`
	Verdict     string  `json:"verdict"`
	RiskScore   float64 `json:"risk_score"`
	MatchedRule string  `json:"matched_rule"`
	FlagsJSON   string  `json:"flags_json"`
	DenyMessage string  `json:"deny_message"`
	PayloadJSON string  `json:"payload_json,omitempty"`
}

// handleAuditLog — GET /api/audit-log?workflow_slug=...&layer=...&verdict=...&run_id=...&limit=...
//
// workflow_slug:  required (defaults to "untitled" if blank — matches RunWorkflow fallback)
// layer:          L0 | L1 | L2 | L3 | all  (default all)
// verdict:        ALLOW | LOG | DENY | SUCCESS | FAILURE | REVIEW | all  (default all)
// run_id:         optional — filter to one specific run
// limit:          default 200, max 1000
//
// Response:
// {
//   "workflow_slug": "...",
//   "filters":       {layer, verdict, run_id, limit},
//   "summary":       {"L0": {"ALLOW": 16, "DENY": 1}, "L3": {...}, ...},
//   "count":         28,
//   "rows":          [auditLogRow, ...]
// }
func handleAuditLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	wfSlug := strings.TrimSpace(q.Get("workflow_slug"))
	if wfSlug == "" {
		wfSlug = "untitled"
	}
	layer := strings.ToUpper(strings.TrimSpace(q.Get("layer")))
	verdict := strings.ToUpper(strings.TrimSpace(q.Get("verdict")))
	runID := strings.TrimSpace(q.Get("run_id"))
	limit := 200
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 1000 {
		limit = 1000
	}

	db, err := openProjectDB(wfSlug)
	if err != nil {
		writeAuditLogError(w, http.StatusInternalServerError, "open_db", err.Error())
		return
	}

	// Ensure the table exists so we can query an empty result on fresh installs
	// without 500ing. Idempotent.
	if _, err := db.Exec(layerAuditLogDDL); err != nil {
		writeAuditLogError(w, http.StatusInternalServerError, "ensure_table", err.Error())
		return
	}

	// Build WHERE clause from filters.
	var conds []string
	var args []interface{}
	if layer != "" && layer != "ALL" {
		conds = append(conds, "layer = ?")
		args = append(args, layer)
	}
	if verdict != "" && verdict != "ALL" {
		conds = append(conds, "verdict = ?")
		args = append(args, verdict)
	}
	if runID != "" {
		conds = append(conds, "run_id = ?")
		args = append(args, runID)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	// Summary counts (always over ALL rows, ignoring layer/verdict filters)
	summary, err := auditLogSummary(db, runID)
	if err != nil {
		writeAuditLogError(w, http.StatusInternalServerError, "summary", err.Error())
		return
	}

	// Row fetch — payload_json is potentially huge; trim it on the wire to 4 KB.
	rowsSQL := fmt.Sprintf(`SELECT id, ts, run_id, COALESCE(stage_id,0), COALESCE(ce_slug,''),
	                              layer, verdict, COALESCE(risk_score,0),
	                              COALESCE(matched_rule,''), COALESCE(flags_json,''),
	                              COALESCE(deny_message,''),
	                              substr(COALESCE(payload_json,''), 1, 4096)
	                       FROM layer_audit_log %s
	                       ORDER BY ts DESC, id DESC
	                       LIMIT ?`, where)
	args = append(args, limit)
	rowsCur, err := db.Query(rowsSQL, args...)
	if err != nil {
		writeAuditLogError(w, http.StatusInternalServerError, "query", err.Error())
		return
	}
	defer rowsCur.Close()
	var rows []auditLogRow
	for rowsCur.Next() {
		var r auditLogRow
		if err := rowsCur.Scan(&r.ID, &r.TS, &r.RunID, &r.StageID, &r.CESlug, &r.Layer, &r.Verdict, &r.RiskScore, &r.MatchedRule, &r.FlagsJSON, &r.DenyMessage, &r.PayloadJSON); err != nil {
			continue
		}
		rows = append(rows, r)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"workflow_slug": wfSlug,
		"filters": map[string]interface{}{
			"layer":   firstNonEmpty(layer, "all"),
			"verdict": firstNonEmpty(verdict, "all"),
			"run_id":  runID,
			"limit":   limit,
		},
		"summary": summary,
		"count":   len(rows),
		"rows":    rows,
	})
}

// auditLogSummary returns nested map: layer → verdict → count.
// If runID is non-empty, restricts to that run.
func auditLogSummary(db *sql.DB, runID string) (map[string]map[string]int, error) {
	q := `SELECT layer, verdict, COUNT(*) FROM layer_audit_log`
	var args []interface{}
	if runID != "" {
		q += ` WHERE run_id = ?`
		args = append(args, runID)
	}
	q += ` GROUP BY layer, verdict`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]int{}
	for rows.Next() {
		var layer, verdict string
		var n int
		if err := rows.Scan(&layer, &verdict, &n); err != nil {
			continue
		}
		if _, ok := out[layer]; !ok {
			out[layer] = map[string]int{}
		}
		out[layer][verdict] = n
	}
	return out, nil
}

func writeAuditLogError(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "detail": detail})
}

func firstNonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
