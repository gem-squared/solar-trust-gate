package main

// WP-01 U4 — SQLite layer_audit_log table for L0/L1/L2/L3 verdict persistence.
//
// Per David spec (2026-05-19): LOG verdicts must be persisted to SQLite even
// when the UI maps LOG → ALLOW visually. Every layer call (ALLOW + LOG + DENY)
// gets one row in the per-project DB so post-hoc forensics can query e.g.
// "show me all L0 LOG events in run XYZ".

import (
	"context"
	"encoding/json"
	"log"
	"time"
)

const layerAuditLogDDL = `
CREATE TABLE IF NOT EXISTS layer_audit_log (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts TEXT NOT NULL,
	run_id TEXT NOT NULL,
	stage_id INTEGER,
	ce_slug TEXT,
	layer TEXT NOT NULL CHECK (layer IN ('L0','L1','L2','L3')),
	verdict TEXT NOT NULL,
	risk_score REAL,
	matched_rule TEXT,
	flags_json TEXT,
	deny_message TEXT,
	payload_json TEXT
);
CREATE INDEX IF NOT EXISTS idx_layer_audit_run ON layer_audit_log(run_id, stage_id);
CREATE INDEX IF NOT EXISTS idx_layer_audit_layer_verdict ON layer_audit_log(layer, verdict);
`

// ensureLayerAuditTable — idempotent migration; called lazily on first INSERT.
func ensureLayerAuditTable(projectSlug string) error {
	db, err := openProjectDB(projectSlug)
	if err != nil {
		return err
	}
	_, err = db.Exec(layerAuditLogDDL)
	return err
}

// AppendLayerAudit persists a single layer verdict row to the per-project DB.
// Best-effort: errors logged but never returned, so a SQLite hiccup never
// breaks the workflow run.
func AppendLayerAudit(ctx context.Context, projectSlug, runID, ceSlug, layer, verdict, matchedRule, denyMessage string, stageID int, riskScore float64, flags []string, payload interface{}) {
	if projectSlug == "" || layer == "" {
		return
	}
	if err := ensureLayerAuditTable(projectSlug); err != nil {
		log.Printf("[AUDIT-LOG] ensure table failed: %v", err)
		return
	}
	db, err := openProjectDB(projectSlug)
	if err != nil {
		log.Printf("[AUDIT-LOG] open db failed: %v", err)
		return
	}
	flagsJSON, _ := json.Marshal(flags)
	var payloadJSON string
	if payload != nil {
		if b, err := json.Marshal(payload); err == nil {
			payloadJSON = string(b)
		}
	}
	_, err = db.ExecContext(ctx, `INSERT INTO layer_audit_log
		(ts, run_id, stage_id, ce_slug, layer, verdict, risk_score, matched_rule, flags_json, deny_message, payload_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		time.Now().UTC().Format(time.RFC3339Nano),
		runID, stageID, ceSlug, layer, verdict,
		riskScore, matchedRule, string(flagsJSON), denyMessage, payloadJSON,
	)
	if err != nil {
		log.Printf("[AUDIT-LOG] insert failed (%s/%s): %v", layer, verdict, err)
	}
}
