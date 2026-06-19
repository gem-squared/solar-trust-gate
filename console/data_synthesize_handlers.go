package main

// /data-synthesize — populate a CE's spec.SampleI + spec.ReferenceData on disk.
//
// Three modes:
//   upload       — files arrive base64-encoded in the POST body (canvas-driven)
//   from-disk    — server reads files directly from a path (operator-side / bootstrap)
//   llm-generate — Wolfi generates N scenarios from the contract (v1: deferred, 501)
//
// HTTP surface (registered via RegisterDataSynthesizeRoutes from main.go):
//   POST  /api/data-synthesize/upload       body: {ce_slug, files[], force?}
//   POST  /api/data-synthesize/from-disk    body: {ce_slug, source_dir, force?}
//   GET   /api/data-synthesize/slice-map    query: ?ce_slug=wf/stage
//
// Helpers (parse + slice + atomic patch) live in data_synthesize_logic.go
// — Unit 3 of WP-AO-40. This file (Unit 2) provides the HTTP plumbing +
// slice-config constants and calls into the helpers.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ── HTTP route registration ──────────────────────────────────────────

// RegisterDataSynthesizeRoutes wires the three data-synthesize endpoints onto
// the supplied mux. Called from main.go as a one-line append (the ¬B-amendment
// for this WP, same pattern as WP-AO-38 Unit 2).
func RegisterDataSynthesizeRoutes(mux *http.ServeMux, heavyRL *rateLimiter) {
	mux.HandleFunc("POST /api/data-synthesize/upload", heavyRL.wrap(authGuard(limitBodyN(handleDataSynthesizeUpload, 16<<20)))) // 16 MB body cap
	mux.HandleFunc("POST /api/data-synthesize/from-disk", authGuard(limitBody(handleDataSynthesizeFromDisk)))
	mux.HandleFunc("GET /api/data-synthesize/slice-map", authGuard(handleDataSynthesizeSliceMap))
}

// ── Request / response shapes ────────────────────────────────────────

type dataSynthUploadRequest struct {
	CESlug string                  `json:"ce_slug"`
	Files  []dataSynthUploadedFile `json:"files"`
	Force  bool                    `json:"force,omitempty"`
}

type dataSynthUploadedFile struct {
	Filename      string `json:"filename"`
	ContentBase64 string `json:"content_base64"`
}

type dataSynthFromDiskRequest struct {
	CESlug    string `json:"ce_slug"`
	SourceDir string `json:"source_dir"`
	Force     bool   `json:"force,omitempty"`
}

// dataSynthResponse mirrors the SKILL.md output contract B.
type dataSynthResponse struct {
	CESlug         string `json:"ce_slug"`
	RegistryPath   string `json:"registry_path"`
	SampleIBytes   int    `json:"sample_i_bytes"`
	TablesCount    int    `json:"tables_count"`
	TableNames     []string `json:"table_names"`
	PatchedAt      string `json:"patched_at"`
	Forced         bool   `json:"forced,omitempty"`
}

type dataSynthErrorResponse struct {
	Error  string `json:"error"`
	Detail string `json:"detail,omitempty"`
}

// ── Per-CE slice configuration ───────────────────────────────────────
//
// Which reference tables go into spec.ReferenceData for each CE-stage,
// AND which scenario stage-key supplies spec.SampleI.
//
// Hardcoded for v1 (the health-insurance-claim demo). v2 will move this
// into the workflow.json itself or a sidecar manifest.

type sliceConfig struct {
	// Tables the CE needs (lookup keys into SyntheticBundle.tables).
	Tables []string `json:"tables"`
	// Which scenario stage-key supplies spec.SampleI. Empty = use the first
	// stage of the first scenario.
	ScenarioStageKey string `json:"scenario_stage_key"`
}

// stageSliceConfig keys by full ce_slug = "{workflow_slug}/{stage_slug}".
// The workflow slug is derived from each contract's H1/Workflow header at
// /create-ce time and stays stable across the pipeline.
var stageSliceConfig = map[string]sliceConfig{
	"health-insurance-claim-pipeline/claim-intake": {
		Tables:           []string{"policies"},
		ScenarioStageKey: "stage_1_intake",
	},
	"health-insurance-claim-pipeline/policy-verification": {
		Tables:           []string{"policies", "policy_members", "premium_ledger", "claims"},
		ScenarioStageKey: "stage_2_policy_verification",
	},
	"health-insurance-claim-pipeline/eligibility-check": {
		Tables:           []string{"plan_benefits", "claim_utilisation"},
		ScenarioStageKey: "stage_3_eligibility_check",
	},
	"health-insurance-claim-pipeline/medical-review": {
		Tables: []string{
			"icd10_reference", "cpt_reference", "accredited_providers",
			"physician_registry", "pre_authorisations",
			"medical_necessity_guidelines", "rps_schedule",
			"icd10_cpt_plausibility",
		},
		ScenarioStageKey: "stage_4_medical_review",
	},
	"health-insurance-claim-pipeline/claim-adjudication": {
		Tables:           []string{"plan_benefits", "deductible_ledger"},
		ScenarioStageKey: "stage_5_adjudication",
	},
	"health-insurance-claim-pipeline/disbursement": {
		Tables:           []string{"accredited_providers", "claim_sequence"},
		ScenarioStageKey: "stage_6_disbursement",
	},
}

// ── HTTP handlers ────────────────────────────────────────────────────

// handleDataSynthesizeUpload reads base64-encoded file contents from the body,
// decodes them into a temp directory, then runs the same parse+slice+patch
// pipeline as the from-disk handler.
func handleDataSynthesizeUpload(w http.ResponseWriter, r *http.Request) {
	var req dataSynthUploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeDataSynthError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.CESlug == "" || len(req.Files) == 0 {
		writeDataSynthError(w, http.StatusBadRequest, "missing_field", "ce_slug and files are both required")
		return
	}

	// Materialize uploaded files in a temp dir, scoped by ce_slug + timestamp.
	tmpDir, err := os.MkdirTemp("", "data-synthesize-upload-*")
	if err != nil {
		writeDataSynthError(w, http.StatusInternalServerError, "tmpdir_failed", err.Error())
		return
	}
	defer os.RemoveAll(tmpDir)

	for _, f := range req.Files {
		if f.Filename == "" {
			continue
		}
		// Sanitize filename — strip any path components.
		clean := filepath.Base(f.Filename)
		if clean == "" || clean == "." || clean == ".." {
			continue
		}
		decoded, derr := base64.StdEncoding.DecodeString(f.ContentBase64)
		if derr != nil {
			writeDataSynthError(w, http.StatusBadRequest, "base64_decode_failed",
				fmt.Sprintf("file %q: %v", f.Filename, derr))
			return
		}
		dst := filepath.Join(tmpDir, clean)
		if err := os.WriteFile(dst, decoded, 0o644); err != nil {
			writeDataSynthError(w, http.StatusInternalServerError, "write_failed",
				fmt.Sprintf("file %q: %v", f.Filename, err))
			return
		}
	}

	runDataSynthesize(w, req.CESlug, tmpDir, req.Force)
}

// handleDataSynthesizeFromDisk reads files directly from a server-side path.
// Used by the deploy-bootstrap script and by operators who have the synthetic
// data already on the server's filesystem.
func handleDataSynthesizeFromDisk(w http.ResponseWriter, r *http.Request) {
	var req dataSynthFromDiskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeDataSynthError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.CESlug == "" || req.SourceDir == "" {
		writeDataSynthError(w, http.StatusBadRequest, "missing_field", "ce_slug and source_dir are both required")
		return
	}
	// Path safety: must be absolute or anchored under the project tree.
	// (Conservative — relative paths are fragile across the systemd service WD.)
	if !filepath.IsAbs(req.SourceDir) {
		req.SourceDir = filepath.Join(baseDir, req.SourceDir)
	}
	info, err := os.Stat(req.SourceDir)
	if err != nil || !info.IsDir() {
		writeDataSynthError(w, http.StatusBadRequest, "source_dir_invalid",
			fmt.Sprintf("not a directory: %s", req.SourceDir))
		return
	}

	runDataSynthesize(w, req.CESlug, req.SourceDir, req.Force)
}

// handleDataSynthesizeSliceMap returns the configured table+scenario mapping
// for a given CE — lets the frontend preview what WILL be injected before
// the user confirms the synthesize action.
func handleDataSynthesizeSliceMap(w http.ResponseWriter, r *http.Request) {
	ceSlug := r.URL.Query().Get("ce_slug")
	if ceSlug == "" {
		writeDataSynthError(w, http.StatusBadRequest, "missing_field", "ce_slug query parameter is required")
		return
	}
	cfg, ok := stageSliceConfig[ceSlug]
	if !ok {
		// No hardcoded mapping for this CE — return an empty config; the
		// caller can still upload, just no auto-slice will happen.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ce_slug":            ceSlug,
			"tables":             []string{},
			"scenario_stage_key": "",
			"detail":             "no hardcoded slice-config for this CE; data-synthesize will run with passthrough",
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ce_slug":            ceSlug,
		"tables":             cfg.Tables,
		"scenario_stage_key": cfg.ScenarioStageKey,
	})
}

// ── runDataSynthesize — the common parse+slice+patch pipeline ────────

func runDataSynthesize(w http.ResponseWriter, ceSlug, sourceDir string, force bool) {
	// Validate ce_slug format and split.
	parts := strings.SplitN(ceSlug, "/", 2)
	if len(parts) != 2 || !slugPattern.MatchString(parts[0]) || !slugPattern.MatchString(parts[1]) {
		writeDataSynthError(w, http.StatusBadRequest, "invalid_ce_slug",
			"expected '{workflow_slug}/{stage_slug}' both kebab-case")
		return
	}
	wf, stage := parts[0], parts[1]

	// Confirm the CESpec exists.
	registryPath := cePath(wf, stage)
	if _, err := os.Stat(registryPath); err != nil {
		writeDataSynthError(w, http.StatusNotFound, "ce_not_found",
			fmt.Sprintf("no CESpec at %s — run /create-ce first", registryPath))
		return
	}

	// Run the parse → slice → patch pipeline (helpers live in
	// data_synthesize_logic.go, Unit 3).
	resp, err := dataSynthesizeRun(wf, stage, sourceDir, force)
	if err != nil {
		// Map known sentinel errors to HTTP codes.
		switch {
		case isStickyDataErr(err):
			writeDataSynthError(w, http.StatusConflict, "sticky_data", err.Error())
		case isPPreViolationErr(err):
			writeDataSynthError(w, http.StatusUnprocessableEntity, "p_pre_violation", err.Error())
		case isNotImplementedErr(err):
			writeDataSynthError(w, http.StatusNotImplemented, "mode_not_implemented", err.Error())
		default:
			writeDataSynthError(w, http.StatusInternalServerError, "synthesize_failed", err.Error())
		}
		return
	}
	resp.Forced = force
	resp.PatchedAt = time.Now().UTC().Format(time.RFC3339)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// writeDataSynthError emits a standard JSON error envelope.
func writeDataSynthError(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(dataSynthErrorResponse{Error: code, Detail: detail})
}
