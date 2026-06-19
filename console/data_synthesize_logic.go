package main

// data_synthesize_logic.go — parse + slice + atomic-patch helpers for
// the /data-synthesize skill (WP-AO-40 Unit 3).
//
// Pipeline used by both upload and from-disk handlers:
//
//   parseSyntheticDataDir(dir)  →  SyntheticBundle
//   sliceForStage(ceSlug, bundle) →  (sampleI, referenceData)
//   patchCESpec(wf, stage, sampleI, referenceData, force) → *CESpec
//
// The pipeline is glued together by dataSynthesizeRun, called from
// data_synthesize_handlers.go.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ── Sentinel errors ──────────────────────────────────────────────────

var (
	errStickyData    = errors.New("CESpec already has sample_i and/or reference_data populated; re-call with force=true to overwrite")
	errPPreViolation = errors.New("synthesized scenario fails the CE's P_pre validation")
)

// errNotImplementedV1 is the v1 sentinel for the llm-generate mode and
// any other deferred path. Kept exported via the checker below.
var errNotImplementedV1 = errors.New("llm-generate mode is reserved for v2; use upload or from-disk in v1")

func isStickyDataErr(err error) bool     { return errors.Is(err, errStickyData) }
func isPPreViolationErr(err error) bool  { return errors.Is(err, errPPreViolation) }
func isNotImplementedErr(err error) bool { return errors.Is(err, errNotImplementedV1) }

// ── Synthetic data parsing ───────────────────────────────────────────

// SyntheticBundle is the in-memory shape after walking a synthetic-data
// directory. tables is keyed by detected top-level table name found
// inside any db_*.json; scenarios is one entry per claim_*_full_pipeline.json.
type SyntheticBundle struct {
	Tables    map[string]json.RawMessage `json:"tables"`
	Scenarios []ScenarioBundle           `json:"scenarios"`
}

// ScenarioBundle captures one full pipeline trace from a claim_A###_full_pipeline.json.
type ScenarioBundle struct {
	Name     string                     `json:"name"`               // "claim_A001" (file stem)
	Scenario string                     `json:"scenario,omitempty"` // value of "_scenario" if present
	Outcome  string                     `json:"outcome,omitempty"`  // value of "_outcome" if present
	Stages   map[string]json.RawMessage `json:"stages"`             // "stage_1_intake" → raw stage object
}

// parseSyntheticDataDir walks a directory (non-recursive) looking for:
//   db_*.json                   → table source (one file may carry many tables)
//   claim_*_full_pipeline.json  → scenario bundle
// Other files (README, .DS_Store, …) are silently ignored.
func parseSyntheticDataDir(dir string) (*SyntheticBundle, error) {
	bundle := &SyntheticBundle{
		Tables:    make(map[string]json.RawMessage),
		Scenarios: []ScenarioBundle{},
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read synthetic-data dir %q: %w", dir, err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}

		switch {
		case strings.HasPrefix(name, "db_"):
			// Top-level may carry multiple tables — extract every top-level
			// key whose value is a JSON array.
			if err := extractTablesFromDBFile(bundle, raw, name); err != nil {
				return nil, err
			}
		case strings.HasPrefix(name, "claim_") && strings.HasSuffix(name, "_full_pipeline.json"):
			sb, err := extractScenarioBundle(raw, name)
			if err != nil {
				return nil, err
			}
			bundle.Scenarios = append(bundle.Scenarios, *sb)
		}
	}

	// Sort scenarios by name for deterministic ordering (A001, A002, ...).
	sort.Slice(bundle.Scenarios, func(i, j int) bool {
		return bundle.Scenarios[i].Name < bundle.Scenarios[j].Name
	})

	return bundle, nil
}

func extractTablesFromDBFile(bundle *SyntheticBundle, raw []byte, sourceName string) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return fmt.Errorf("parse %s: %w", sourceName, err)
	}
	for key, val := range top {
		// Skip metadata keys.
		if strings.HasPrefix(key, "_") {
			continue
		}
		// Accept only JSON arrays (tables) and JSON objects (reference dicts).
		// Discriminate by first non-whitespace byte of the raw value.
		trimmed := bytes.TrimSpace([]byte(val))
		if len(trimmed) == 0 {
			continue
		}
		if trimmed[0] != '[' && trimmed[0] != '{' {
			continue
		}
		bundle.Tables[key] = val
	}
	return nil
}

func extractScenarioBundle(raw []byte, sourceName string) (*ScenarioBundle, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("parse %s: %w", sourceName, err)
	}

	// Filename stem: strip "_full_pipeline.json" suffix to get e.g. "claim_A001".
	stem := strings.TrimSuffix(sourceName, "_full_pipeline.json")

	sb := &ScenarioBundle{
		Name:   stem,
		Stages: make(map[string]json.RawMessage),
	}

	if v, ok := top["_scenario"]; ok {
		_ = json.Unmarshal(v, &sb.Scenario)
	}
	if v, ok := top["_outcome"]; ok {
		_ = json.Unmarshal(v, &sb.Outcome)
	}
	for key, val := range top {
		if strings.HasPrefix(key, "stage_") {
			sb.Stages[key] = val
		}
	}
	return sb, nil
}

// ── Per-stage slicing ────────────────────────────────────────────────

// sliceForStage selects the table slice + scenario stage-input for a given CE.
// Returns the sample_i JSON string and a reference_data map (table_name →
// JSON-encoded rows). Empty config means passthrough — empty maps returned.
func sliceForStage(ceSlug string, bundle *SyntheticBundle) (sampleI string, referenceData map[string]string, err error) {
	cfg, ok := stageSliceConfig[ceSlug]
	referenceData = make(map[string]string)

	if !ok {
		// No hardcoded config — return what we have as a passthrough.
		// First scenario's first stage input becomes sampleI if available.
		if len(bundle.Scenarios) > 0 {
			for _, stageRaw := range bundle.Scenarios[0].Stages {
				if input := extractStageInput(stageRaw); input != "" {
					sampleI = input
					break
				}
			}
		}
		return sampleI, referenceData, nil
	}

	// 1. reference_data — pick the configured tables.
	for _, tableName := range cfg.Tables {
		if val, ok := bundle.Tables[tableName]; ok {
			referenceData[tableName] = string(val)
		}
	}

	// 2. sample_i — pick the configured scenario stage's _input.
	if len(bundle.Scenarios) == 0 {
		return "", referenceData, nil
	}
	stageRaw, ok := bundle.Scenarios[0].Stages[cfg.ScenarioStageKey]
	if !ok {
		// Stage key not found — leave sample_i empty rather than fail.
		return "", referenceData, nil
	}
	sampleI = extractStageInput(stageRaw)
	return sampleI, referenceData, nil
}

// extractStageInput pulls the "_input" field from a stage object. Returns
// empty string if the stage isn't an object or has no _input.
func extractStageInput(stageRaw json.RawMessage) string {
	var stage map[string]json.RawMessage
	if err := json.Unmarshal(stageRaw, &stage); err != nil {
		return ""
	}
	if v, ok := stage["_input"]; ok {
		return string(v)
	}
	return ""
}

// ── Atomic CESpec patch ──────────────────────────────────────────────

// patchCESpec reads the existing CESpec, applies sample_i + reference_data,
// and writes atomically via .new + rename. Refuses if existing fields are
// populated unless force=true.
func patchCESpec(wf, stage, sampleI string, referenceData map[string]string, force bool) (*CESpec, error) {
	registryPath := cePath(wf, stage)
	raw, err := os.ReadFile(registryPath)
	if err != nil {
		return nil, fmt.Errorf("read CESpec %q: %w", registryPath, err)
	}

	var spec CESpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("parse CESpec %q: %w", registryPath, err)
	}

	// Sticky-data guard.
	if !force {
		populated := spec.SampleI != "" || len(spec.ReferenceData) > 0
		if populated {
			return nil, fmt.Errorf("%w (CESpec=%s, sample_i_bytes=%d, ref_data_tables=%d)",
				errStickyData, registryPath, len(spec.SampleI), len(spec.ReferenceData))
		}
	}

	// Apply.
	spec.SampleI = sampleI
	spec.ReferenceData = referenceData
	spec.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	// Atomic write: marshal → .new → fsync → rename.
	out, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal patched CESpec: %w", err)
	}
	tmpPath := registryPath + ".new"
	if err := os.WriteFile(tmpPath, out, 0o644); err != nil {
		return nil, fmt.Errorf("write tmp %q: %w", tmpPath, err)
	}
	// Best-effort fsync (no-op if filesystem doesn't expose it; OS handles
	// durability on rename).
	if f, err := os.Open(tmpPath); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
	if err := os.Rename(tmpPath, registryPath); err != nil {
		return nil, fmt.Errorf("atomic rename %q → %q: %w", tmpPath, registryPath, err)
	}

	return &spec, nil
}

// ── dataSynthesizeRun — the glue called from handlers ───────────────

func dataSynthesizeRun(wf, stage, sourceDir string, force bool) (*dataSynthResponse, error) {
	bundle, err := parseSyntheticDataDir(sourceDir)
	if err != nil {
		return nil, err
	}

	ceSlug := wf + "/" + stage
	sampleI, referenceData, err := sliceForStage(ceSlug, bundle)
	if err != nil {
		return nil, err
	}

	patched, err := patchCESpec(wf, stage, sampleI, referenceData, force)
	if err != nil {
		return nil, err
	}

	// Stable order for table_names in the response.
	tableNames := make([]string, 0, len(referenceData))
	for k := range referenceData {
		tableNames = append(tableNames, k)
	}
	sort.Strings(tableNames)

	return &dataSynthResponse{
		CESlug:       ceSlug,
		RegistryPath: cePath(wf, stage),
		SampleIBytes: len(patched.SampleI),
		TablesCount:  len(patched.ReferenceData),
		TableNames:   tableNames,
	}, nil
}
