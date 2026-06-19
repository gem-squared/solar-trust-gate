package main

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

//go:embed static/*
var staticFiles embed.FS

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}

	initAuthKeys()
	initSheepRegistry()
	go loadKoreanClaimPDFs()

	mux := http.NewServeMux()

	heavyRL := newRateLimiter(10, time.Minute)
	lightRL := newRateLimiter(60, time.Minute)

	// Auth endpoint — must be reachable without authGuard
	mux.HandleFunc("POST /api/auth", handleAuth)

	mux.HandleFunc("POST /api/inspect", lightRL.wrap(authGuard(limitBodyN(handleInspect, 100<<10))))
	mux.HandleFunc("POST /api/audit", heavyRL.wrap(authGuard(limitBody(handleAudit))))
	mux.HandleFunc("POST /api/demo", heavyRL.wrap(authGuard(limitBody(handleDemo))))
	mux.HandleFunc("GET /api/providers", authGuard(handleProviders))
	mux.HandleFunc("GET /api/scenarios", authGuard(handleScenarios))
	mux.HandleFunc("GET /api/scenarios/{id}", authGuard(handleScenarioByID))
	mux.HandleFunc("GET /api/ledger", authGuard(handleLedger))
	mux.HandleFunc("GET /api/compliance-samples", authGuard(handleComplianceSamples))
	mux.HandleFunc("POST /api/compliance", heavyRL.wrap(authGuard(limitBody(handleCompliance))))
	mux.HandleFunc("POST /api/interpret-scorecard", heavyRL.wrap(authGuard(limitBody(handleInterpretScorecard))))
	mux.HandleFunc("POST /api/interpret-scenario", heavyRL.wrap(authGuard(limitBody(handleInterpretScenario))))

	// Workflow Gate API
	mux.HandleFunc("GET /api/workflows", authGuard(handleWorkflows))
	mux.HandleFunc("GET /api/workflows/{domain}", authGuard(handleWorkflowSteps))
	mux.HandleFunc("GET /api/workflows/{domain}/{step}", authGuard(handleWorkflowStep))
	mux.HandleFunc("GET /api/workflows/{domain}/sample-input", authGuard(handleSampleInput))
	mux.HandleFunc("GET /api/loan-providers", authGuard(handleLoanProviders))
	mux.HandleFunc("POST /api/gate/verify", heavyRL.wrap(authGuard(limitBody(handleGateVerify))))
	mux.HandleFunc("POST /api/gate/execute", heavyRL.wrap(authGuard(limitBody(handleGateExecute))))
	mux.HandleFunc("POST /api/gate/execute-stream", heavyRL.wrap(authGuardQuery(limitBody(handleGateExecuteStream))))
	mux.HandleFunc("POST /api/gate/run-pipeline", heavyRL.wrap(authGuard(limitBody(handleRunPipeline))))
	mux.HandleFunc("POST /api/gate/run-pipeline-stream", heavyRL.wrap(authGuardQuery(limitBody(handleRunPipelineStream))))

	// Audit-Gate API (Phase-2 L1/L2 gates — proxy to gem2-tpmn-checker SaaS)
	mux.HandleFunc("POST /api/audit-gate/p-check", heavyRL.wrap(authGuard(limitBody(handlePCheck))))
	mux.HandleFunc("POST /api/audit-gate/o-check", heavyRL.wrap(authGuard(limitBody(handleOCheck))))

	// Contract-Executor (Phase-2 single-F worker — data-driven from ce-registry)
	mux.HandleFunc("POST /ce/{workflow}/{stage}/", heavyRL.wrap(authGuard(limitBody(handleCEInvoke))))
	mux.HandleFunc("POST /ce/{workflow}/{stage}", heavyRL.wrap(authGuard(limitBody(handleCEInvoke))))
	// WP-AO-62 — audited variant: runs L1 → F → L2 in one HTTP round-trip.
	// Used by the CE Viewer modal to surface the full governance chain for
	// an isolated CE without the user leaving the viewer.
	mux.HandleFunc("POST /ce/{workflow}/{stage}/audited", heavyRL.wrap(authGuard(limitBody(handleCEInvokeAudited))))
	// WP-AO-67 — judge-injection endpoints. last-run returns the most recent
	// successful F-execution JSON for a CE (populated by workflow_runner);
	// sample-update persists a judge-edited input as the CE's new default.
	mux.HandleFunc("GET /api/ce/{workflow}/{stage}/last-run", authGuard(handleCELastRun))
	mux.HandleFunc("GET /api/ce/{workflow}/{stage}/sample", authGuard(handleCESampleGet))
	mux.HandleFunc("POST /api/ce/{workflow}/{stage}/sample", authGuard(limitBody(handleCESampleUpdate)))
	mux.HandleFunc("POST /api/ce/{workflow}/{stage}/sample/reset", authGuard(handleCESampleReset))
	mux.HandleFunc("GET /api/crafter/ce-registry", authGuard(handleCEList))

	// CE Viewer — single generic static page served like the cover page.
	// AI-Pilot emits a clickable link in chat after /create-ce with query
	// params ?api=&sample=&key=&title= so judges land on a pre-filled
	// form and click Run in one step. Per-CE bundle pages dropped — this
	// single viewer replaces them. NO authGuard (page handles auth
	// client-side); the underlying /ce/{wf}/{stage}/ POST stays guarded.
	mux.HandleFunc("GET /ce-viewer", func(w http.ResponseWriter, r *http.Request) {
		data, err := staticFiles.ReadFile("static/ce-viewer.html")
		if err != nil {
			http.Error(w, "ce-viewer not found", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})

	// GEM²-Crafter API
	RegisterCrafterRoutes(mux, heavyRL, lightRL)
	RegisterExplorerRoutes(mux)

	// WP-AO-53 Unit 1 — embedded demo project bootstrap (one-click for judges)
	mux.HandleFunc("POST /api/crafter/bootstrap-demo", heavyRL.wrap(authGuard(limitBody(handleBootstrapDemo))))

	// WP-01 — read-only audit-log query for the canvas Audit Log button
	mux.HandleFunc("GET /api/audit-log", authGuard(handleAuditLog))

	// Korean insurance claim processing pipeline (WP-ST-03 Unit 6+7, WP-ST-04)
	mux.HandleFunc("POST /api/claim/process", heavyRL.wrap(handleClaimProcess))
	mux.HandleFunc("GET /api/claim/scenarios", handleClaimScenarios)
	mux.HandleFunc("GET /api/claim/tfc-template", handleTFCTemplate)
	mux.HandleFunc("POST /api/claim/hitl/add-rule", handleHITLAddRule)
	mux.HandleFunc("POST /api/claim/hitl/reset", handleHITLReset)

	// Stage-3 Workflow Canvas (WP-AO-38)
	RegisterWorkflowRoutes(mux, heavyRL)

	// /data-synthesize TPMN skill (WP-AO-40)
	RegisterDataSynthesizeRoutes(mux, heavyRL)

	// WP-AO-39 deploy routes are cascaded from RegisterWorkflowRoutes (in
	// workflow_handlers.go) — do not register them again here, that panics
	// with a double-registration on POST /api/workflow/deploy.

	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatal(err)
	}
	// Serve Korean claim UI at root (WP-ST-03 Unit 8).
	// /canvas routes to the original workflow canvas for legacy access.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		data, err := staticFiles.ReadFile("static/solar-claim.html")
		if err != nil {
			http.Error(w, "solar-claim.html not found", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})
	mux.HandleFunc("GET /canvas", func(w http.ResponseWriter, r *http.Request) {
		data, err := staticFiles.ReadFile("static/workflow-canvas.html")
		if err != nil {
			http.Error(w, "workflow-canvas.html not found", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})
	mux.Handle("GET /", http.FileServer(http.FS(sub)))

	handler := corsMiddleware(mux)

	if authEnabled() {
		fmt.Printf("GEM² Console listening on :%s (auth: %d keys)\n", port, len(validKeys))
	} else {
		fmt.Printf("GEM² Console listening on :%s (auth: disabled)\n", port)
	}
	log.Fatal(http.ListenAndServe(":"+port, handler))
}

const maxRequestBodyBytes = 1 << 20 // 1MB

func limitBody(next http.HandlerFunc) http.HandlerFunc {
	return limitBodyN(next, maxRequestBodyBytes)
}

func limitBodyN(next http.HandlerFunc, n int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, n)
		next(w, r)
	}
}

type ipBucket struct {
	tokens    int
	lastReset time.Time
}

type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*ipBucket
	limit   int
	window  time.Duration
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		buckets: make(map[string]*ipBucket),
		limit:   limit,
		window:  window,
	}
}

func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	b, ok := rl.buckets[ip]
	if !ok || now.Sub(b.lastReset) >= rl.window {
		rl.buckets[ip] = &ipBucket{tokens: 1, lastReset: now}
		return true
	}
	if b.tokens >= rl.limit {
		return false
	}
	b.tokens++
	return true
}

func (rl *rateLimiter) wrap(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if ip == "" {
			ip = r.RemoteAddr
		}
		if fwd := r.Header.Get("Fly-Client-IP"); fwd != "" {
			ip = fwd
		}
		if !rl.allow(ip) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"rate limit exceeded, try again later"}`))
			return
		}
		next(w, r)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Access-Key, Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}
