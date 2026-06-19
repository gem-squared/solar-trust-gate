package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// ── SQLite Project Store ─────────────────────────────────────────
//
// WP-AO-53 Unit 2 — per-project SQLite database materialized from
// CESpec.reference_data JSON arrays.
//
// Each project gets one file at
//   .gem-squared/workspace/{projectSlug}/data.sqlite
// Tables are auto-DDL'd from the FIRST row's value shapes
// (string→TEXT, number→REAL, bool→INTEGER 0/1, null→TEXT NULL).
// All rows from CESpec.ReferenceData[<table>] are INSERTed. Idempotent:
// if a table already exists with matching columns, INSERT is skipped;
// if shape differs, table is DROPped and recreated (demo-grade — no
// migrations).
//
// Driver: modernc.org/sqlite (pure-Go, no CGO required) so cross-compile
// to linux/amd64 stays clean.

var (
	projectDBs   = make(map[string]*sql.DB)
	projectDBsMu sync.Mutex
)

// openProjectDB returns (creating if needed) the SQLite handle for one project.
// Connections are pooled per-projectSlug.
func openProjectDB(projectSlug string) (*sql.DB, error) {
	projectDBsMu.Lock()
	defer projectDBsMu.Unlock()

	if db, ok := projectDBs[projectSlug]; ok {
		return db, nil
	}

	dir := filepath.Join(baseDir, ".gem-squared", "workspace", projectSlug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir project dir: %w", err)
	}
	dbPath := filepath.Join(dir, "data.sqlite")
	// modernc.org/sqlite uses pragma-friendly DSN. Foreign keys off (demo
	// doesn't need cascading constraints); journal mode WAL for concurrent reads.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(off)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	projectDBs[projectSlug] = db
	log.Printf("[SQLITE] opened project DB %s", dbPath)
	return db, nil
}

// bootstrapProjectDB walks the project's ce-registry, collects every distinct
// (table_name, json_array_string) pair from spec.ReferenceData, and ensures
// each table exists and is populated. Caller-supplied context governs cancel.
// Idempotent — safe to call on every RunWorkflow.
func bootstrapProjectDB(ctx context.Context, projectSlug string) error {
	specs, err := listProjectCESpecs(projectSlug)
	if err != nil {
		return fmt.Errorf("list project specs: %w", err)
	}
	if len(specs) == 0 {
		return nil // empty project — nothing to bootstrap
	}

	db, err := openProjectDB(projectSlug)
	if err != nil {
		return err
	}

	// Collect: tableName → first non-empty JSON array for that table across all specs.
	// Last-write-wins on conflict (demo policy — log a warning).
	type tableSrc struct {
		Name string
		JSON string
	}
	seen := make(map[string]string)
	var tables []tableSrc
	for _, spec := range specs {
		for tableName, jsonArr := range spec.ReferenceData {
			tableName = strings.TrimSpace(tableName)
			if tableName == "" || jsonArr == "" {
				continue
			}
			if !validTableName(tableName) {
				log.Printf("[SQLITE] skip invalid table name %q in %s/%s", tableName, spec.WorkflowSlug, spec.StageSlug)
				continue
			}
			if prev, exists := seen[tableName]; exists && prev != jsonArr {
				log.Printf("[SQLITE] WARN table %q defined by multiple specs in %s — last-write-wins (%s/%s)",
					tableName, projectSlug, spec.WorkflowSlug, spec.StageSlug)
			}
			seen[tableName] = jsonArr
		}
	}
	for name, jsonArr := range seen {
		tables = append(tables, tableSrc{Name: name, JSON: jsonArr})
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })

	for _, t := range tables {
		if err := materializeTable(ctx, db, t.Name, t.JSON); err != nil {
			log.Printf("[SQLITE] table %s materialize failed: %v", t.Name, err)
			continue // skip on per-table error; don't kill the whole bootstrap
		}
	}
	log.Printf("[SQLITE] bootstrapped project %s — %d tables", projectSlug, len(tables))
	return nil
}

// listProjectCESpecs walks ce-registry/{wf}/*.json restricted to the workflow
// slug whose name matches projectSlug. For the demo, project_slug equals
// workflow_slug. If a project owns multiple workflows in the future, this
// helper expands to iterate them.
func listProjectCESpecs(projectSlug string) ([]CESpec, error) {
	all, err := listCESpecs()
	if err != nil {
		return nil, err
	}
	var out []CESpec
	for _, s := range all {
		// For now: match project_slug to workflow_slug. The bootstrap-demo
		// handler creates project "health-insurance-claim-pipeline" and the
		// contracts declare the same workflow slug, so this aligns.
		if s.WorkflowSlug == projectSlug {
			// Reload full spec (listCESpecs already returns full — keep as-is)
			out = append(out, s)
		}
	}
	return out, nil
}

// materializeTable creates/refreshes one table from its JSON array string.
// Strategy: parse first row → infer columns → if existing table columns
// differ → DROP + recreate. Otherwise leave structure alone, but always
// re-INSERT rows after a fresh `DELETE FROM` for idempotent refresh on
// re-bootstrap.
func materializeTable(ctx context.Context, db *sql.DB, table, jsonArr string) error {
	var rows []map[string]any
	if err := json.Unmarshal([]byte(jsonArr), &rows); err != nil {
		return fmt.Errorf("parse json array: %w", err)
	}
	if len(rows) == 0 {
		log.Printf("[SQLITE] table %s: zero rows in source, skip", table)
		return nil
	}

	// Column ordering: sorted by first-row keys (deterministic for the demo).
	first := rows[0]
	columns := make([]string, 0, len(first))
	for k := range first {
		columns = append(columns, k)
	}
	sort.Strings(columns)

	colTypes := make(map[string]string, len(columns))
	for _, c := range columns {
		colTypes[c] = sqliteTypeFor(first[c])
	}

	// Detect shape change vs existing table.
	shapeChanged, exists, err := tableShapeDiffers(ctx, db, table, columns, colTypes)
	if err != nil {
		return fmt.Errorf("inspect table: %w", err)
	}
	if exists && shapeChanged {
		if _, err := db.ExecContext(ctx, "DROP TABLE "+quoteIdent(table)); err != nil {
			return fmt.Errorf("drop %s: %w", table, err)
		}
		log.Printf("[SQLITE] table %s shape changed — dropped + recreating", table)
		exists = false
	}
	if !exists {
		var colDefs []string
		for _, c := range columns {
			colDefs = append(colDefs, fmt.Sprintf("%s %s", quoteIdent(c), colTypes[c]))
		}
		ddl := fmt.Sprintf("CREATE TABLE %s (%s)", quoteIdent(table), strings.Join(colDefs, ", "))
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("create %s: %w", table, err)
		}
	}

	// Always wipe + re-insert (idempotent refresh on every workflow run).
	if _, err := db.ExecContext(ctx, "DELETE FROM "+quoteIdent(table)); err != nil {
		return fmt.Errorf("delete %s: %w", table, err)
	}

	placeholders := make([]string, len(columns))
	for i := range columns {
		placeholders[i] = "?"
	}
	insertSQL := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		quoteIdent(table),
		strings.Join(quoteIdents(columns), ", "),
		strings.Join(placeholders, ", "),
	)
	stmt, err := db.PrepareContext(ctx, insertSQL)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	for _, row := range rows {
		args := make([]any, len(columns))
		for i, c := range columns {
			args[i] = normalizeSQLiteValue(row[c])
		}
		if _, err := stmt.ExecContext(ctx, args...); err != nil {
			log.Printf("[SQLITE] insert row into %s failed: %v", table, err)
			continue
		}
	}
	log.Printf("[SQLITE] table %s materialized — %d rows, %d cols", table, len(rows), len(columns))
	return nil
}

// tableShapeDiffers returns (shapeChanged, tableExists, err).
// tableExists=false → shapeChanged is meaningless. shapeChanged=true → DROP+recreate.
func tableShapeDiffers(ctx context.Context, db *sql.DB, table string, wantCols []string, wantTypes map[string]string) (bool, bool, error) {
	r, err := db.QueryContext(ctx, "SELECT name, type FROM pragma_table_info(?)", table)
	if err != nil {
		return false, false, err
	}
	defer r.Close()
	got := make(map[string]string)
	for r.Next() {
		var n, t string
		if err := r.Scan(&n, &t); err != nil {
			return false, false, err
		}
		got[n] = strings.ToUpper(t)
	}
	if len(got) == 0 {
		return false, false, nil // table doesn't exist
	}
	if len(got) != len(wantCols) {
		return true, true, nil
	}
	for _, c := range wantCols {
		if g, ok := got[c]; !ok || g != strings.ToUpper(wantTypes[c]) {
			return true, true, nil
		}
	}
	return false, true, nil
}

// sqliteTypeFor infers a SQLite affinity from a Go any value (parsed from JSON).
// JSON unmarshal produces: string, float64, bool, []any, map[string]any, nil.
// For NULL first-row values we fall back to TEXT — best-effort inference.
func sqliteTypeFor(v any) string {
	switch v.(type) {
	case nil:
		return "TEXT"
	case bool:
		return "INTEGER" // SQLite encodes booleans as 0/1
	case float64:
		return "REAL"
	case string:
		return "TEXT"
	case []any, map[string]any:
		return "TEXT" // serialize nested values as JSON strings
	default:
		return "TEXT"
	}
}

// normalizeSQLiteValue converts a JSON-decoded Go any to a value SQLite accepts.
// Maps + slices become JSON-encoded strings; bools become 0/1 INTEGER; everything
// else passes through.
func normalizeSQLiteValue(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case bool:
		if t {
			return 1
		}
		return 0
	case []any, map[string]any:
		buf, _ := json.Marshal(v)
		return string(buf)
	default:
		return v
	}
}

// validTableName enforces ASCII identifier rules so quoteIdent is enough to
// guard against injection. Same shape as a SQL identifier.
func validTableName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' {
			continue
		}
		if i > 0 && (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
func quoteIdents(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = quoteIdent(n)
	}
	return out
}

// listProjectTables returns table names in the project DB. Used by tool registry.
func listProjectTables(projectSlug string) ([]string, error) {
	db, err := openProjectDB(projectSlug)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// tableColumns returns the column names of `table` in the project DB. Used by
// the query/count tool handlers to validate the caller's WHERE keys.
func tableColumns(projectSlug, table string) ([]string, error) {
	if !validTableName(table) {
		return nil, fmt.Errorf("invalid table name")
	}
	db, err := openProjectDB(projectSlug)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}
