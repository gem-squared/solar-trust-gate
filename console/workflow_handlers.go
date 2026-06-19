package main

// HTTP surface for the workflow canvas. Phase-2 / Stage-3.
//
// Routes (registered via RegisterWorkflowRoutes from main.go):
//
//   POST  /api/workflow/run         start a run, returns {run_id}
//   GET   /api/workflow/run/stream  SSE trace of one run (authGuardQuery)
//   POST  /api/workflow/save        write workflow.json to .gem-squared/workflows/
//   GET   /api/workflow/load        read a saved workflow by slug
//   GET   /api/workflow/list        list saved workflows
//
// Run state lives in-memory in `runs`. A janitor goroutine reaps ended runs
// after 1 hour.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type runState struct {
	ch        chan RunEvent
	cancel    context.CancelFunc
	startedAt time.Time
	finalCh   chan RunFinalStatus
	ended     bool
}

var (
	runsMu              sync.Mutex
	runs                = map[string]*runState{}
	workflowJanitorOnce sync.Once
)

// RegisterWorkflowRoutes wires the workflow canvas backend onto mux.
// Called from main.go (one-line append per WP-AO-38 Unit 2 ¬B-amendment).
func RegisterWorkflowRoutes(mux *http.ServeMux, heavyRL *rateLimiter) {
	mux.HandleFunc("POST /api/workflow/run", heavyRL.wrap(authGuard(limitBody(handleWorkflowRun))))
	mux.HandleFunc("GET /api/workflow/run/stream", authGuardQuery(handleWorkflowRunStream))
	mux.HandleFunc("POST /api/workflow/save", authGuard(limitBody(handleWorkflowSave)))
	mux.HandleFunc("GET /api/workflow/load", authGuard(handleWorkflowLoad))
	mux.HandleFunc("GET /api/workflow/list", authGuard(handleWorkflowList))
	mux.HandleFunc("GET /api/workflow/ce-contract", authGuard(handleWorkflowCEContract))

	// WP-AO-39: deploy-to-web-URL surface (additive, parallel-safe over the
	// immutable WP-AO-38 engine). Wired via this RegisterWorkflowRoutes call
	// rather than a separate main.go edit per the project's init()-preferred
	// doctrine for Stage-3 route registration.
	RegisterWorkflowDeployRoutes(mux, heavyRL)

	workflowJanitorOnce.Do(startWorkflowJanitor)
}

// handleWorkflowCEContract returns the FULL CESpec (including A, B, PPre, PPost
// schemas) for one CE. The standard /api/crafter/ce-registry endpoint emits a
// light projection that drops these fields; the canvas needs them for edge
// type-compatibility checks. Read-only access to the registry on disk —
// touches no Stage-2 Go code.
func handleWorkflowCEContract(w http.ResponseWriter, r *http.Request) {
	slug := r.URL.Query().Get("slug")
	parts := strings.SplitN(slug, "/", 2)
	if len(parts) != 2 {
		http.Error(w, `{"error":"invalid_slug","detail":"expected 'workflow/stage'"}`, http.StatusBadRequest)
		return
	}
	spec, err := loadCESpec(parts[0], parts[1])
	if err != nil {
		http.Error(w, `{"error":"ce_not_found"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(spec)
}

func handleWorkflowRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Workflow WorkflowJSON   `json:"workflow"`
		Input    map[string]any `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid_body"}`, http.StatusBadRequest)
		return
	}
	if len(body.Workflow.Nodes) == 0 {
		http.Error(w, `{"error":"empty_workflow","detail":"no nodes"}`, http.StatusBadRequest)
		return
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

	// Forward the caller's auth key to the /ce/ loopback handler so the CE
	// invocation passes authGuard (one server, one key set).
	authKey := r.Header.Get("X-Access-Key")
	if authKey == "" {
		authKey = r.URL.Query().Get("key")
	}

	port := workflowServerPort()
	base := fmt.Sprintf("http://localhost:%s", port)

	go func() {
		defer close(state.ch)
		final := RunWorkflow(ctx, body.Workflow, body.Input, runID, base, authKey, state.ch)
		state.finalCh <- final
		runsMu.Lock()
		state.ended = true
		runsMu.Unlock()
		log.Printf("[WORKFLOW] run=%s final=%s", runID, final)
	}()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"run_id": runID})
}

func handleWorkflowRunStream(w http.ResponseWriter, r *http.Request) {
	runID := r.URL.Query().Get("run_id")
	if runID == "" {
		http.Error(w, `{"error":"missing_run_id"}`, http.StatusBadRequest)
		return
	}
	runsMu.Lock()
	state, ok := runs[runID]
	runsMu.Unlock()
	if !ok {
		http.Error(w, `{"error":"unknown_run_id"}`, http.StatusNotFound)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, `{"error":"streaming_unsupported"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable buffering on reverse proxies
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case evt, ok := <-state.ch:
			if !ok {
				// channel closed by producer → run ended
				select {
				case final := <-state.finalCh:
					fmt.Fprintf(w, "event: complete\ndata: {\"final_status\":\"%s\"}\n\n", final)
					flusher.Flush()
				case <-time.After(2 * time.Second):
					fmt.Fprintf(w, "event: complete\ndata: {\"final_status\":\"UNKNOWN\"}\n\n")
					flusher.Flush()
				}
				return
			}
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: progress\ndata: %s\n\n", data)
			flusher.Flush()
		case <-ctx.Done():
			// client disconnected
			return
		}
	}
}

func handleWorkflowSave(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Slug     string       `json:"slug"`
		Workflow WorkflowJSON `json:"workflow"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid_body"}`, http.StatusBadRequest)
		return
	}
	if !slugPattern.MatchString(body.Slug) {
		http.Error(w, `{"error":"invalid_slug","detail":"kebab-case, 1-64 chars"}`, http.StatusBadRequest)
		return
	}
	dir := filepath.Join(baseDir, ".gem-squared", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, `{"error":"mkdir_failed"}`, http.StatusInternalServerError)
		return
	}
	body.Workflow.WorkflowSlug = body.Slug
	if body.Workflow.SchemaVersion == "" {
		body.Workflow.SchemaVersion = "1.0"
	}
	body.Workflow.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(body.Workflow, "", "  ")
	if err != nil {
		http.Error(w, `{"error":"marshal_failed"}`, http.StatusInternalServerError)
		return
	}
	path := filepath.Join(dir, body.Slug+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		http.Error(w, `{"error":"write_failed"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"slug": body.Slug})
}

func handleWorkflowLoad(w http.ResponseWriter, r *http.Request) {
	slug := r.URL.Query().Get("slug")
	if !slugPattern.MatchString(slug) {
		http.Error(w, `{"error":"invalid_slug"}`, http.StatusBadRequest)
		return
	}
	path := filepath.Join(baseDir, ".gem-squared", "workflows", slug+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func handleWorkflowList(w http.ResponseWriter, r *http.Request) {
	dir := filepath.Join(baseDir, ".gem-squared", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		// dir doesn't exist yet — empty list
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"workflows": []any{}})
		return
	}
	list := []map[string]any{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		slug := strings.TrimSuffix(e.Name(), ".json")
		info, _ := e.Info()
		var title string
		if data, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
			var wf WorkflowJSON
			if json.Unmarshal(data, &wf) == nil {
				title = wf.Title
			}
		}
		list = append(list, map[string]any{
			"slug":        slug,
			"title":       title,
			"modified_at": info.ModTime().Format(time.RFC3339),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"workflows": list})
}

func newRunID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%d", hex.EncodeToString(b), time.Now().Unix())
}

func workflowServerPort() string {
	if p := os.Getenv("PORT"); p != "" {
		return p
	}
	// Match main.go's default fallback so the loopback URL hits our own
	// listener when PORT is unset.
	return "8090"
}

// startWorkflowJanitor reaps ended runs older than 1 hour every 15 minutes.
func startWorkflowJanitor() {
	go func() {
		t := time.NewTicker(15 * time.Minute)
		defer t.Stop()
		for range t.C {
			cutoff := time.Now().Add(-1 * time.Hour)
			runsMu.Lock()
			reaped := 0
			for id, s := range runs {
				if s.ended && s.startedAt.Before(cutoff) {
					delete(runs, id)
					reaped++
				}
			}
			runsMu.Unlock()
			if reaped > 0 {
				log.Printf("[WF_JANITOR] reaped %d ended run(s)", reaped)
			}
		}
	}()
}
