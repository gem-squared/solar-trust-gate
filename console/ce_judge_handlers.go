package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// WP-AO-67 — judge-injection + last-run surfacing for CE Viewer.
//
// Two endpoints:
//   GET  /api/ce/{workflow}/{stage}/last-run   → returns last successful
//        F-execution JSON for that CE (404 if none yet). CE Viewer reads
//        this on load so the output panel pre-fills with what the most
//        recent workflow run actually produced.
//   POST /api/ce/{workflow}/{stage}/sample     → judge can edit the input
//        textarea then click Save to persist as the CE's default sample.
//        Body: {"i": <any-json>}. Updates spec.SampleI in ce-registry.
//
// Last-run persistence happens in workflow_runner.go (this file just
// exposes the read endpoint).

// lastRunPath returns the file location for a stage's last-run output.
func lastRunPath(workflowSlug, stageSlug string) string {
	return filepath.Join(baseDir, ".gem-squared", "workspace", workflowSlug, "last-runs", stageSlug+".json")
}

type lastRunRecord struct {
	Output    any    `json:"output"`
	NodeID    string `json:"node_id,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	Timestamp string `json:"timestamp"`
}

// persistLastRun writes a stage's most recent F-execution output. Called by
// workflow_runner after a successful node EXEC. Best-effort: failures are
// logged but don't abort the workflow.
func persistLastRun(workflowSlug, stageSlug, nodeID, runID string, output any) {
	rec := lastRunRecord{
		Output:    output,
		NodeID:    nodeID,
		RunID:     runID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return
	}
	path := lastRunPath(workflowSlug, stageSlug)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, data, 0o644)
}

func handleCELastRun(w http.ResponseWriter, r *http.Request) {
	workflow := r.PathValue("workflow")
	stage := r.PathValue("stage")
	path := lastRunPath(workflow, stage)
	data, err := os.ReadFile(path)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":"no_last_run","ce_slug":"%s/%s"}`, workflow, stage)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

// handleCESampleUpdate persists a judge-edited input as the CE's current
// SampleI. Body: {"i": <any-valid-json>}. OriginalSampleI is NEVER touched
// — that's the frozen standard the judge can revert to.
func handleCESampleUpdate(w http.ResponseWriter, r *http.Request) {
	workflow := r.PathValue("workflow")
	stage := r.PathValue("stage")

	var req struct {
		I json.RawMessage `json:"i"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_body","detail":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	if len(req.I) == 0 {
		http.Error(w, `{"error":"missing_field","detail":"'i' (input value) is required"}`, http.StatusBadRequest)
		return
	}
	var anyValue any
	if err := json.Unmarshal(req.I, &anyValue); err != nil {
		http.Error(w, `{"error":"invalid_json","detail":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}

	spec, err := loadCESpec(workflow, stage)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":"ce_not_found","ce_slug":"%s/%s"}`, workflow, stage)
		return
	}
	pretty, perr := json.MarshalIndent(anyValue, "", "  ")
	if perr != nil {
		http.Error(w, `{"error":"format_error"}`, http.StatusInternalServerError)
		return
	}
	// 2026-05-19 — every Save snaps spec.OriginalSampleI to the embedded
	// canonical so Reset always restores the TRUE clean baseline, not a
	// previously-edited / previously-self-healed sample that drifted via
	// create-ce or earlier Save paths. Mirrors GET + Reset behaviour (both
	// prefer embedded). For user-authored CEs not in the embed, falls back
	// to the existing self-heal (snapshot of SampleI when OriginalSampleI
	// is empty).
	embeddedStd, hasEmbedded := canonicalStandardSample(workflow, stage)
	if hasEmbedded {
		spec.OriginalSampleI = embeddedStd
	} else if spec.OriginalSampleI == "" {
		spec.OriginalSampleI = spec.SampleI
	}
	spec.SampleI = string(pretty)

	if err := saveCESpec(spec); err != nil {
		http.Error(w, `{"error":"save_failed","detail":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	// Report the embedded canonical when available so the client's edited-vs-
	// standard comparison agrees with GET (which also prefers embedded).
	originalForResp := spec.OriginalSampleI
	if hasEmbedded {
		originalForResp = embeddedStd
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":                true,
		"ce_slug":           workflow + "/" + stage,
		"sample_i":          spec.SampleI,
		"original_sample_i": originalForResp,
		"original_source":   ifEmbedded(hasEmbedded),
		"is_edited":         spec.SampleI != originalForResp && originalForResp != "",
		"saved_at":          time.Now().UTC().Format(time.RFC3339),
	})
}

func ifEmbedded(ok bool) string {
	if ok {
		return "embedded"
	}
	return "spec"
}

// canonicalStandardSample returns the embedded green-path sample for a CE
// (e.g. demo-assets/health-insurance-claim/samples/claim-01-intake-input.json).
// This is the TRUE standard — frozen in the binary at compile time, immune
// to per-spec drift. Returns ("", false) if no embedded sample exists.
func canonicalStandardSample(workflowSlug, stageSlug string) (string, bool) {
	// Demo embed is only health-insurance-claim for now. Future domains
	// (loan-approval / procurement) will register under the same path
	// scheme once their demo-assets/<slug>/samples/ directories land.
	if workflowSlug != demoProjectSlug {
		return "", false
	}
	path := fmt.Sprintf("demo-assets/health-insurance-claim/samples/%s-input.json", stageSlug)
	data, err := demoAssetsFS.ReadFile(path)
	if err != nil {
		return "", false
	}
	// Pretty-print so the viewer textarea stays readable.
	var any interface{}
	if jerr := json.Unmarshal(data, &any); jerr != nil {
		return string(data), true
	}
	pretty, perr := json.MarshalIndent(any, "", "  ")
	if perr != nil {
		return string(data), true
	}
	return string(pretty), true
}

// handleCESampleReset wipes the current SampleI back to the TRUE standard
// — the embedded green-path sample-input.json baked into the binary at
// build time. Falls back to spec.OriginalSampleI when no embedded sample
// exists (e.g. user-authored CEs). POST /api/ce/{workflow}/{stage}/sample/reset
func handleCESampleReset(w http.ResponseWriter, r *http.Request) {
	workflow := r.PathValue("workflow")
	stage := r.PathValue("stage")
	spec, err := loadCESpec(workflow, stage)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":"ce_not_found","ce_slug":"%s/%s"}`, workflow, stage)
		return
	}
	// 1st choice: the canonical embedded sample (immutable, never drifts).
	// 2nd choice: spec.OriginalSampleI (may have been backfilled from a
	// previously-edited SampleI on legacy registries).
	standard, hasEmbedded := canonicalStandardSample(workflow, stage)
	source := "embedded"
	if !hasEmbedded {
		if spec.OriginalSampleI == "" {
			http.Error(w, `{"error":"no_standard","detail":"no embedded sample and no OriginalSampleI on disk"}`, http.StatusConflict)
			return
		}
		standard = spec.OriginalSampleI
		source = "spec.original_sample_i"
	}
	spec.SampleI = standard
	// Also self-heal the on-disk OriginalSampleI to the canonical embedded
	// value so future GETs report `is_edited` correctly against the truth.
	if hasEmbedded {
		spec.OriginalSampleI = standard
	}
	if err := saveCESpec(spec); err != nil {
		http.Error(w, `{"error":"save_failed","detail":"`+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":                true,
		"ce_slug":           workflow + "/" + stage,
		"sample_i":          spec.SampleI,
		"original_sample_i": spec.OriginalSampleI,
		"reset_source":      source,
		"reset_at":          time.Now().UTC().Format(time.RFC3339),
	})
}

// handleCESampleGet returns the current and original sample inputs for the
// CE viewer to display side-by-side. GET /api/ce/{workflow}/{stage}/sample
//
// 'Original' is sourced from the embedded sample-input.json when available
// — that's the TRUE standard, frozen in the binary at build time. Falls
// back to spec.OriginalSampleI for user-authored CEs not in the embed.
func handleCESampleGet(w http.ResponseWriter, r *http.Request) {
	workflow := r.PathValue("workflow")
	stage := r.PathValue("stage")
	spec, err := loadCESpec(workflow, stage)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":"ce_not_found","ce_slug":"%s/%s"}`, workflow, stage)
		return
	}
	original := spec.OriginalSampleI
	source := "spec"
	if embedded, ok := canonicalStandardSample(workflow, stage); ok {
		original = embedded
		source = "embedded"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ce_slug":           workflow + "/" + stage,
		"sample_i":          spec.SampleI,
		"original_sample_i": original,
		"original_source":   source,
		"is_edited":         spec.SampleI != original && original != "",
	})
}
