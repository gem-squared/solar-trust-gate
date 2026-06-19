package main

import (
	"context"
	"embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ── Demo Bootstrap ─────────────────────────────────────────────────
//
// WP-AO-65 reverse-engineered structure (replaces flat WP-AO-53 layout):
//
//   demo-assets/health-insurance-claim/
//     ├── contracts/   *.md         — 6 TPMN 5-block stage contracts
//     ├── compliance/  *.json       — per-stage rule arrays (merged into
//     │                                workspace compliance.json for L2)
//     ├── db/          *.csv        — reference table seeds (CSV → JSON →
//     │                                materializeTable → project SQLite)
//     └── samples/     *.json       — green-path I/O pairs (authoring
//                                      reference only; not loaded at boot)
//
// Single HTTP POST /api/crafter/bootstrap-demo wires everything: creates
// the project, materializes contracts as CEs, seeds the SQLite reference
// tables row-for-row, and stages compliance.json where loadComplianceCorpus
// finds it. Idempotent — re-runs without force return existing CEs as
// "skipped".
//
// Authoring source of truth: Docs/health-insurance-claim/{contracts,
// compliance,db,samples}/. The console/demo-assets/ tree mirrors it
// for compile-time embed.

//go:embed all:demo-assets/health-insurance-claim
var demoAssetsFS embed.FS

const (
	demoAssetsRoot   = "demo-assets/health-insurance-claim"
	demoContractsDir = "demo-assets/health-insurance-claim/contracts"
	demoComplianceDir = "demo-assets/health-insurance-claim/compliance"
	demoDBDir        = "demo-assets/health-insurance-claim/db"
	demoProjectSlug  = "health-insurance-claim-pipeline"
)

type bootstrapDemoResponse struct {
	ProjectSlug      string         `json:"project_slug"`
	CECount          int            `json:"ce_count"`
	CESlugs          []string       `json:"ce_slugs"`
	Skipped          []string       `json:"skipped,omitempty"`
	Errors           []string       `json:"errors,omitempty"`
	ComplianceLoaded bool           `json:"compliance_loaded"`
	ComplianceRules  int            `json:"compliance_rules"`
	DBSeeded         map[string]int `json:"db_seeded,omitempty"` // table → row count
	ElapsedMs        int64          `json:"elapsed_ms"`
}

// handleBootstrapDemo wires the embedded demo project into the live workspace.
// Request body (optional): {"force": true} to overwrite existing CEs.
func handleBootstrapDemo(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	var req struct {
		Force bool `json:"force,omitempty"`
	}
	if r.ContentLength > 0 {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	// 1. Scaffold project workspace — mirrors executeCreateProject's dir layout
	projectDir := filepath.Join(baseDir, ".gem-squared", "workspace", demoProjectSlug)
	uploadDir := filepath.Join(projectDir, "uploaded_files")
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		bootstrapJSONErr(w, http.StatusInternalServerError, "mkdir uploaded_files: "+err.Error())
		return
	}
	for _, sub := range []string{"work-plan", "archive", "verify-work-logs", filepath.Join("output", demoProjectSlug)} {
		_ = os.MkdirAll(filepath.Join(projectDir, sub), 0o755)
	}

	// 2. Walk embedded contracts/ in claim-01 → claim-06 order
	entries, err := fs.ReadDir(demoAssetsFS, demoContractsDir)
	if err != nil {
		bootstrapJSONErr(w, http.StatusInternalServerError, "embed read contracts dir: "+err.Error())
		return
	}
	var contractNames []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			contractNames = append(contractNames, e.Name())
		}
	}
	sort.Strings(contractNames)

	state := &CrafterState{ProjectDir: projectDir}
	var slugs []string
	var skipped []string
	var errs []string

	for _, name := range contractNames {
		data, rerr := fs.ReadFile(demoAssetsFS, demoContractsDir+"/"+name)
		if rerr != nil {
			errs = append(errs, fmt.Sprintf("%s: read embedded: %v", name, rerr))
			continue
		}
		destPath := filepath.Join(uploadDir, name)
		if werr := os.WriteFile(destPath, data, 0o644); werr != nil {
			errs = append(errs, fmt.Sprintf("%s: write upload: %v", name, werr))
			continue
		}

		ceArgs := map[string]any{
			"file_path": destPath,
			"force":     req.Force,
		}
		result, cerr := executeCreateCE(ceArgs, state, nil, time.Now())
		if cerr != nil {
			errs = append(errs, fmt.Sprintf("%s: create-ce: %v", name, cerr))
			continue
		}
		if result == nil {
			errs = append(errs, name+": create-ce returned nil")
			continue
		}

		if strings.HasPrefix(strings.TrimSpace(result.OutputB), "## CE already exists") {
			skipped = append(skipped, strings.TrimSuffix(name, ".md"))
			continue
		}

		if result.StateChange != "" {
			var sc struct {
				CESlug string `json:"ce_slug"`
			}
			if jerr := json.Unmarshal([]byte(result.StateChange), &sc); jerr == nil && sc.CESlug != "" {
				slugs = append(slugs, sc.CESlug)
				continue
			}
		}
		slugs = append(slugs, demoProjectSlug+"/"+strings.TrimSuffix(name, ".md"))
	}

	// 3. Merge embedded compliance/*.json → single workspace compliance.json
	//    so loadComplianceCorpus(audit_retrievers.go) finds it unchanged.
	complianceLoaded := false
	complianceRules := 0
	if merged, count, mErr := mergeEmbeddedCompliance(); mErr != nil {
		errs = append(errs, "compliance merge failed: "+mErr.Error())
	} else {
		dest := filepath.Join(projectDir, "compliance.json")
		if werr := os.WriteFile(dest, merged, 0o644); werr != nil {
			errs = append(errs, "compliance.json write failed: "+werr.Error())
		} else {
			complianceLoaded = true
			complianceRules = count
			log.Printf("[BOOTSTRAP-DEMO] compliance merged → %s (%d rules across %d files)",
				dest, count, complianceRuleFileCount())
		}
	}

	// 4. Seed SQLite reference tables from embedded db/*.csv. Each CSV becomes
	//    a row-array JSON, then materializeTable creates/refreshes the table
	//    inside the project's SQLite DB.
	dbSeeded := map[string]int{}
	if err := bootstrapProjectDB(r.Context(), demoProjectSlug); err != nil {
		errs = append(errs, "bootstrap project DB: "+err.Error())
	} else {
		dbHandle, dbErr := openProjectDB(demoProjectSlug)
		if dbErr != nil {
			errs = append(errs, "open project DB: "+dbErr.Error())
		} else {
			// NO defer Close() — openProjectDB returns a process-wide pooled
			// handle from projectDBs map. Closing it here breaks every other
			// caller (tableColumns, prefetchEvidence, ledgerCheckForStage)
			// which retrieves the same now-closed handle next time. The pool
			// is intentionally long-lived for the program's lifetime.
			csvEntries, _ := fs.ReadDir(demoAssetsFS, demoDBDir)
			for _, e := range csvEntries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".csv") {
					continue
				}
				tableName := strings.TrimSuffix(e.Name(), ".csv")
				if !validTableName(tableName) {
					errs = append(errs, fmt.Sprintf("csv %s: invalid table name", e.Name()))
					continue
				}
				csvData, cerr := fs.ReadFile(demoAssetsFS, demoDBDir+"/"+e.Name())
				if cerr != nil {
					errs = append(errs, fmt.Sprintf("csv %s: read: %v", e.Name(), cerr))
					continue
				}
				rows, conErr := csvToRows(csvData)
				if conErr != nil {
					errs = append(errs, fmt.Sprintf("csv %s: parse: %v", e.Name(), conErr))
					continue
				}
				if len(rows) == 0 {
					log.Printf("[BOOTSTRAP-DEMO] csv %s: zero data rows — skipping table create", e.Name())
					continue
				}
				rowsJSON, _ := json.Marshal(rows)
				if mErr := materializeTable(r.Context(), dbHandle, tableName, string(rowsJSON)); mErr != nil {
					errs = append(errs, fmt.Sprintf("materialize %s: %v", tableName, mErr))
					continue
				}
				dbSeeded[tableName] = len(rows)
			}
		}
	}

	log.Printf("[BOOTSTRAP-DEMO] project=%s ce_count=%d skipped=%d errors=%d compliance=%v rules=%d db_tables=%d elapsed=%dms",
		demoProjectSlug, len(slugs), len(skipped), len(errs),
		complianceLoaded, complianceRules, len(dbSeeded), time.Since(start).Milliseconds())

	resp := bootstrapDemoResponse{
		ProjectSlug:      demoProjectSlug,
		CECount:          len(slugs),
		CESlugs:          slugs,
		Skipped:          skipped,
		Errors:           errs,
		ComplianceLoaded: complianceLoaded,
		ComplianceRules:  complianceRules,
		DBSeeded:         dbSeeded,
		ElapsedMs:        time.Since(start).Milliseconds(),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// mergeEmbeddedCompliance walks compliance/*.json and concatenates the
// rule arrays into a single JSON array body. Returns the merged bytes
// and total rule count.
func mergeEmbeddedCompliance() ([]byte, int, error) {
	entries, err := fs.ReadDir(demoAssetsFS, demoComplianceDir)
	if err != nil {
		return nil, 0, fmt.Errorf("read compliance dir: %w", err)
	}
	var merged []map[string]any
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, rerr := fs.ReadFile(demoAssetsFS, demoComplianceDir+"/"+e.Name())
		if rerr != nil {
			return nil, 0, fmt.Errorf("read %s: %w", e.Name(), rerr)
		}
		var arr []map[string]any
		if jerr := json.Unmarshal(data, &arr); jerr != nil {
			return nil, 0, fmt.Errorf("parse %s: %w", e.Name(), jerr)
		}
		merged = append(merged, arr...)
	}
	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return nil, 0, err
	}
	return out, len(merged), nil
}

// complianceRuleFileCount returns the number of compliance/*.json files
// embedded (one per stage). Telemetry helper for the bootstrap log line.
func complianceRuleFileCount() int {
	entries, err := fs.ReadDir(demoAssetsFS, demoComplianceDir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			n++
		}
	}
	return n
}

// csvToRows converts CSV bytes (with header row) into []map[string]any with
// per-cell type inference: integer-shaped → int64, decimal-shaped → float64,
// empty → nil, else → string. Bool-like "0"/"1" remain integers (SQLite has
// no native bool; downstream contracts read them as truthy/falsy ints).
var (
	rxInt   = regexp.MustCompile(`^-?\d+$`)
	rxFloat = regexp.MustCompile(`^-?\d+\.\d+$`)
)

func csvToRows(data []byte) ([]map[string]any, error) {
	r := csv.NewReader(strings.NewReader(string(data)))
	r.FieldsPerRecord = -1 // allow ragged in case of trailing empty fields
	header, err := r.Read()
	if err == io.EOF {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	for i, h := range header {
		header[i] = strings.TrimSpace(h)
	}
	var out []map[string]any
	for {
		rec, rerr := r.Read()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, fmt.Errorf("read row: %w", rerr)
		}
		row := make(map[string]any, len(header))
		for i, h := range header {
			var cell string
			if i < len(rec) {
				cell = strings.TrimSpace(rec[i])
			}
			row[h] = inferCSVValue(cell)
		}
		out = append(out, row)
	}
	return out, nil
}

func inferCSVValue(cell string) any {
	if cell == "" {
		return nil
	}
	if rxInt.MatchString(cell) {
		n, err := strconv.ParseInt(cell, 10, 64)
		if err == nil {
			return n
		}
	}
	if rxFloat.MatchString(cell) {
		f, err := strconv.ParseFloat(cell, 64)
		if err == nil {
			return f
		}
	}
	return cell
}

func bootstrapJSONErr(w http.ResponseWriter, code int, msg string) {
	log.Printf("[BOOTSTRAP-DEMO] error %d: %s", code, msg)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// Compile-time guard so the unused `context` import survives even if the
// handler body is later refactored. The ctx is used via r.Context() above;
// keep the import explicit for readability.
var _ = context.Background
