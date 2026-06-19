package main

// WP-AO-39 — Workflow Deploy-to-Web-URL surface.
//
// Adds five handlers + RegisterWorkflowDeployRoutes. All additive over
// WP-AO-38's immutable workflow_runner.go / workflow_yaml.go engine.
//
// Routes:
//   POST /api/workflow/deploy          — validate + slug-collision check + write workflow.json
//   GET  /w/{slug}/                    — serve workflow-viewer.html (no authGuard)
//   GET  /api/w/{slug}/spec            — augmented spec for the viewer (workflow + entry A + exit B + sample)
//   POST /api/w/{slug}/run             — slug-aware run wrapper (reads json from disk, delegates to RunWorkflow)
//
// Existing /api/workflow/run/stream?run_id= is reused as-is.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// handleWorkflowDeploy handles POST /api/workflow/deploy. Body schema:
//
//	{ "slug": "<kebab>", "workflow": <WorkflowJSON>, "force": <bool?> }
//
// Returns 200 on successful write, 409 on slug collision (when force≠true).
// The 409 response includes a server-rolled `suggested_alternative_slug`
// that does NOT already collide (re-rolled up to 3 times; final fallback
// to a unix-timestamp suffix).
func handleWorkflowDeploy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Slug     string       `json:"slug"`
		Workflow WorkflowJSON `json:"workflow"`
		Force    bool         `json:"force,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid_body"}`, http.StatusBadRequest)
		return
	}
	if !slugPattern.MatchString(body.Slug) {
		http.Error(w, `{"error":"invalid_slug","detail":"kebab-case, 1-64 chars, must match ^[a-z0-9][a-z0-9-]{0,62}[a-z0-9]$"}`, http.StatusBadRequest)
		return
	}
	if len(body.Workflow.Nodes) == 0 {
		http.Error(w, `{"error":"empty_workflow","detail":"no nodes"}`, http.StatusBadRequest)
		return
	}
	if body.Workflow.EntryNode == "" || body.Workflow.ExitNode == "" {
		http.Error(w, `{"error":"missing_entry_or_exit","detail":"entry_node and exit_node fields required"}`, http.StatusBadRequest)
		return
	}
	// Sanity: entry + exit must be in nodes[]
	nodeIDs := make(map[string]bool, len(body.Workflow.Nodes))
	for _, n := range body.Workflow.Nodes {
		nodeIDs[n.ID] = true
	}
	if !nodeIDs[body.Workflow.EntryNode] {
		http.Error(w, `{"error":"entry_node_unknown","detail":"entry_node id not present in nodes[]"}`, http.StatusBadRequest)
		return
	}
	if !nodeIDs[body.Workflow.ExitNode] {
		http.Error(w, `{"error":"exit_node_unknown","detail":"exit_node id not present in nodes[]"}`, http.StatusBadRequest)
		return
	}

	dir := filepath.Join(baseDir, ".gem-squared", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, `{"error":"mkdir_failed"}`, http.StatusInternalServerError)
		return
	}
	targetPath := filepath.Join(dir, body.Slug+".json")

	// Collision check — when force=false and file exists, return 409 with a
	// suggested alternative slug that has been verified non-colliding.
	if !body.Force {
		if _, err := os.Stat(targetPath); err == nil {
			suggested := pickNonCollidingSlug(dir, body.Slug)
			existingDeployedAt := readWorkflowDeployedAt(targetPath)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":                      "slug_collision",
				"existing_slug":              body.Slug,
				"existing_deployed_at":       existingDeployedAt,
				"suggested_alternative_slug": suggested,
			})
			log.Printf("[DEPLOY] slug_collision: %s (suggested alt: %s)", body.Slug, suggested)
			return
		}
	}

	body.Workflow.WorkflowSlug = body.Slug
	if body.Workflow.SchemaVersion == "" {
		body.Workflow.SchemaVersion = "1.0"
	}
	// Stamp creation/update timestamps. CreatedAt preserved if file already
	// existed (a re-deploy keeps original authoring time).
	now := time.Now().UTC().Format(time.RFC3339)
	if existing, ok := readWorkflowFile(targetPath); ok && existing.CreatedAt != "" {
		body.Workflow.CreatedAt = existing.CreatedAt
	} else {
		body.Workflow.CreatedAt = now
	}

	data, err := json.MarshalIndent(body.Workflow, "", "  ")
	if err != nil {
		http.Error(w, `{"error":"marshal_failed"}`, http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(targetPath, data, 0o644); err != nil {
		http.Error(w, `{"error":"write_failed"}`, http.StatusInternalServerError)
		return
	}
	url := fmt.Sprintf("%s/w/%s/", strings.TrimRight(ceProductionHost, "/"), body.Slug)
	log.Printf("[DEPLOY] %s → %s (force=%v)", body.Slug, url, body.Force)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"slug":         body.Slug,
		"url":          url,
		"deployed_at":  now,
	})
}

// pickNonCollidingSlug rolls a friendly random postfix using
// suggestProjectSlug() up to 3 times to find a slug that does NOT
// already exist on disk. Final fallback uses a unix-timestamp postfix
// (guaranteed unique).
func pickNonCollidingSlug(dir, baseSlug string) string {
	for i := 0; i < 3; i++ {
		candidate := baseSlug + "-" + suggestProjectSlug()
		if _, err := os.Stat(filepath.Join(dir, candidate+".json")); err != nil {
			return candidate
		}
	}
	return fmt.Sprintf("%s-%d", baseSlug, time.Now().Unix())
}

// readWorkflowFile loads a workflow.json from disk. Returns ok=false on miss.
func readWorkflowFile(path string) (WorkflowJSON, bool) {
	var wf WorkflowJSON
	data, err := os.ReadFile(path)
	if err != nil {
		return wf, false
	}
	if err := json.Unmarshal(data, &wf); err != nil {
		return wf, false
	}
	return wf, true
}

// readWorkflowDeployedAt returns the workflow's last-known deployed_at
// (falls back to CreatedAt) for inclusion in the 409 response body.
func readWorkflowDeployedAt(path string) string {
	wf, ok := readWorkflowFile(path)
	if !ok {
		return ""
	}
	if wf.CreatedAt != "" {
		return wf.CreatedAt
	}
	return ""
}

// handleWorkflowViewer serves the static workflow-viewer.html when a
// deployed workflow exists at the given slug. Returns 404 with a
// structured error envelope on miss. NO authGuard — the page has its
// own inline auth gate identical to /ce-viewer (WP-AO-30 pattern).
func handleWorkflowViewer(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !slugPattern.MatchString(slug) {
		http.Error(w, `{"error":"invalid_slug"}`, http.StatusBadRequest)
		return
	}
	path := filepath.Join(baseDir, ".gem-squared", "workflows", slug+".json")
	if _, err := os.Stat(path); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":  "workflow_not_found",
			"slug":   slug,
			"detail": "no workflow deployed at this slug; deploy one via the canvas first",
		})
		return
	}
	data, err := staticFiles.ReadFile("static/workflow-viewer.html")
	if err != nil {
		http.Error(w, `{"error":"viewer_html_missing"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

// handleWorkflowSpec returns the augmented spec used by the viewer page:
// the full workflow.json + entry node's A schema + exit node's B schema
// + a sample input (lifted from the entry node's CE registry sample_i).
// authGuard'd to match the CE-registry endpoint convention.
func handleWorkflowSpec(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !slugPattern.MatchString(slug) {
		http.Error(w, `{"error":"invalid_slug"}`, http.StatusBadRequest)
		return
	}
	path := filepath.Join(baseDir, ".gem-squared", "workflows", slug+".json")
	wf, ok := readWorkflowFile(path)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "workflow_not_found", "slug": slug})
		return
	}

	// Resolve entry + exit node CE registry entries for schema + sample.
	var entryCE, exitCE *CESpec
	for _, n := range wf.Nodes {
		if n.ID == wf.EntryNode {
			if spec, err := loadCESpecByCESlug(n.CESlug); err == nil {
				entryCE = spec
			}
		}
		if n.ID == wf.ExitNode {
			if spec, err := loadCESpecByCESlug(n.CESlug); err == nil {
				exitCE = spec
			}
		}
	}

	resp := map[string]any{
		"workflow": wf,
		"slug":     slug,
		"url":      fmt.Sprintf("%s/w/%s/", strings.TrimRight(ceProductionHost, "/"), slug),
	}
	if entryCE != nil {
		resp["entry_a_schema"] = entryCE.A
		resp["entry_contract_title"] = entryCE.ContractTitle
		resp["sample_input"] = entryCE.SampleI
	}
	if exitCE != nil {
		resp["exit_b_schema"] = exitCE.B
		resp["exit_contract_title"] = exitCE.ContractTitle
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// loadCESpecByCESlug splits "workflow/stage" → calls existing loadCESpec.
// Returns nil + error when the slug doesn't parse or the CE isn't registered.
func loadCESpecByCESlug(ceSlug string) (*CESpec, error) {
	parts := strings.SplitN(ceSlug, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid ce_slug %q", ceSlug)
	}
	return loadCESpec(parts[0], parts[1])
}

// handleWorkflowRunBySlug — slug-aware run wrapper. Reads the workflow.json
// from disk, delegates to RunWorkflow via the same internal pathway as
// handleWorkflowRun. The input payload still comes from the request body.
func handleWorkflowRunBySlug(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !slugPattern.MatchString(slug) {
		http.Error(w, `{"error":"invalid_slug"}`, http.StatusBadRequest)
		return
	}
	path := filepath.Join(baseDir, ".gem-squared", "workflows", slug+".json")
	wf, ok := readWorkflowFile(path)
	if !ok {
		http.Error(w, `{"error":"workflow_not_found","slug":"`+slug+`"}`, http.StatusNotFound)
		return
	}

	var body struct {
		Input map[string]any `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid_body"}`, http.StatusBadRequest)
		return
	}
	if body.Input == nil {
		body.Input = map[string]any{}
	}

	runID := newRunID()
	ctx, cancel := context.WithCancel(context.Background())
	state := &runState{
		ch:        make(chan RunEvent, 64),
		cancel:    cancel,
		startedAt: time.Now(),
		finalCh:   make(chan RunFinalStatus, 1),
	}
	runsMu.Lock()
	runs[runID] = state
	runsMu.Unlock()

	authKey := r.Header.Get("X-Access-Key")
	if authKey == "" {
		authKey = r.URL.Query().Get("key")
	}
	port := workflowServerPort()
	base := fmt.Sprintf("http://localhost:%s", port)

	go func() {
		defer close(state.ch)
		final := RunWorkflow(ctx, wf, body.Input, runID, base, authKey, state.ch)
		state.finalCh <- final
		runsMu.Lock()
		state.ended = true
		runsMu.Unlock()
		log.Printf("[WORKFLOW] slug=%s run=%s final=%s", slug, runID, final)
	}()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"run_id": runID, "slug": slug})
}

// RegisterWorkflowDeployRoutes wires the WP-AO-39 deploy/viewer/spec/run-by-slug
// endpoints into the mux. Called from main.go alongside RegisterWorkflowRoutes
// (must be called AFTER it because Go mux 1.22 pattern conflict resolution
// requires registration order to be deterministic for the /api/workflow/...
// path overlaps — but in practice the two sets do not overlap on method+path).
func RegisterWorkflowDeployRoutes(mux *http.ServeMux, heavyRL *rateLimiter) {
	mux.HandleFunc("POST /api/workflow/deploy", heavyRL.wrap(authGuard(limitBody(handleWorkflowDeploy))))
	mux.HandleFunc("GET /w/{slug}/", heavyRL.wrap(handleWorkflowViewer))
	mux.HandleFunc("GET /w/{slug}", heavyRL.wrap(handleWorkflowViewer))
	mux.HandleFunc("GET /api/w/{slug}/spec", authGuard(handleWorkflowSpec))
	mux.HandleFunc("POST /api/w/{slug}/run", heavyRL.wrap(authGuard(limitBody(handleWorkflowRunBySlug))))
}
