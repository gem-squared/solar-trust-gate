package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type StepSummary struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Index int    `json:"index"`
}

type WorkflowSummary struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Domain string `json:"domain"`
	Steps  int    `json:"steps"`
}

type GateVerifyRequest struct {
	Workflow string         `json:"workflow"`
	Step     int            `json:"step"`
	InputA   map[string]any `json:"input_a"`
	OutputB  map[string]any `json:"output_b"`
	Provider string         `json:"provider,omitempty"`
}

type GateExecuteRequest struct {
	Workflow string         `json:"workflow"`
	Step     int            `json:"step"`
	InputA   map[string]any `json:"input_a"`
	Provider string         `json:"provider"`
}

type RunPipelineRequest struct {
	Workflow string         `json:"workflow"`
	InputA   map[string]any `json:"input_a"`
	Provider string         `json:"provider"`
}

type FullGateResult struct {
	Passed  bool       `json:"passed"`
	Verdict string     `json:"verdict"`
	L0      *GateLayer `json:"l0"`
	L1      *GateLayer `json:"l1,omitempty"`
	L2      *GateLayer `json:"l2"`
}

type ExecuteResponse struct {
	StepName   string         `json:"step_name"`
	StepIndex  int            `json:"step_index"`
	OutputB    map[string]any `json:"output_b"`
	Model      string         `json:"model"`
	Provider   string         `json:"provider"`
	DurationMs float64        `json:"exec_duration_ms"`
	Gate       *FullGateResult `json:"gate"`
	RawLLM     string         `json:"raw_llm,omitempty"`
}

type PipelineStepResult struct {
	StepName   string          `json:"step_name"`
	StepIndex  int             `json:"step_index"`
	OutputB    map[string]any  `json:"output_b,omitempty"`
	Model      string          `json:"model"`
	Provider   string          `json:"provider"`
	DurationMs float64         `json:"exec_duration_ms"`
	Gate       *FullGateResult `json:"gate"`
	Error      string          `json:"error,omitempty"`
}

type PipelineResponse struct {
	Workflow     string               `json:"workflow"`
	Provider     string               `json:"provider"`
	Steps        []PipelineStepResult `json:"steps"`
	FinalVerdict string               `json:"final_verdict"`
	TotalMs      float64              `json:"total_duration_ms"`
}

func getWorkflow(domain string) *Workflow {
	switch domain {
	case "loan-approval":
		wf := LoanApprovalWorkflow()
		return &wf
	default:
		return nil
	}
}

func handleWorkflows(w http.ResponseWriter, r *http.Request) {
	wf := LoanApprovalWorkflow()
	out := []WorkflowSummary{{
		ID:     wf.ID,
		Name:   wf.Name,
		Domain: wf.Domain,
		Steps:  len(wf.Steps),
	}}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func handleWorkflowSteps(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	wf := getWorkflow(domain)
	if wf == nil {
		http.Error(w, `{"error":"workflow not found"}`, http.StatusNotFound)
		return
	}

	var steps []StepSummary
	for _, s := range wf.Steps {
		steps = append(steps, StepSummary{ID: s.ID, Name: s.Name, Index: s.Index})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(steps)
}

func handleWorkflowStep(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	stepID := r.PathValue("step")
	wf := getWorkflow(domain)
	if wf == nil {
		http.Error(w, `{"error":"workflow not found"}`, http.StatusNotFound)
		return
	}

	for _, s := range wf.Steps {
		if s.ID == stepID || fmt.Sprintf("%d", s.Index) == stepID {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(s)
			return
		}
	}
	http.Error(w, `{"error":"step not found"}`, http.StatusNotFound)
}

func runGate(step *WorkflowStep, inputA, outputB map[string]any, provider string) *FullGateResult {
	gate := &FullGateResult{Passed: true, Verdict: "PASS"}

	outputJSON, _ := json.Marshal(outputB)
	l0Start := time.Now()
	l0 := ltInspect(string(outputJSON))
	gate.L0 = &GateLayer{
		Name:     "L0 Lobster Trap DPI",
		Verdict:  l0.Verdict,
		Duration: float64(time.Since(l0Start).Milliseconds()),
		Detail:   l0,
	}
	if l0.Verdict != "ALLOW" {
		gate.Passed = false
		gate.Verdict = "FAIL_L0"
		return gate
	}

	if provider != "" {
		l1Start := time.Now()
		content := fmt.Sprintf("Loan approval step '%s' output:\n%s", step.Name, string(outputJSON))
		gem2, err := gem2Audit(content, provider)
		l1Layer := &GateLayer{
			Name:     "L1 GEM² Truth Filter",
			Duration: float64(time.Since(l1Start).Milliseconds()),
		}
		if err != nil {
			l1Layer.Verdict = "SKIP"
			l1Layer.Detail = map[string]string{"error": err.Error()}
		} else {
			l1Layer.Verdict = gem2.Verdict
			l1Layer.Detail = gem2
			if gem2.Verdict == "BLOCK" {
				gate.Passed = false
				gate.Verdict = "FAIL_L1"
			}
		}
		gate.L1 = l1Layer
	}

	l2Start := time.Now()
	l2 := VerifyPostconditions(step, inputA, outputB)
	gate.L2 = &GateLayer{
		Name:     "L2 Contract Postconditions",
		Verdict:  "PASS",
		Duration: float64(time.Since(l2Start).Milliseconds()),
		Detail:   l2,
	}
	if !l2.Passed {
		gate.L2.Verdict = "FAIL"
		gate.Passed = false
		gate.Verdict = "FAIL_L2"
	}

	return gate
}

func handleGateVerify(w http.ResponseWriter, r *http.Request) {
	var req GateVerifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.OutputB == nil {
		http.Error(w, `{"error":"output_b is required"}`, http.StatusBadRequest)
		return
	}

	wf := getWorkflow(req.Workflow)
	if wf == nil {
		http.Error(w, `{"error":"workflow not found"}`, http.StatusNotFound)
		return
	}

	if req.Step < 1 || req.Step > len(wf.Steps) {
		http.Error(w, `{"error":"invalid step index"}`, http.StatusBadRequest)
		return
	}

	step := &wf.Steps[req.Step-1]
	gate := runGate(step, req.InputA, req.OutputB, req.Provider)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(gate)
}

func handleGateExecute(w http.ResponseWriter, r *http.Request) {
	var req GateExecuteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.Provider == "" {
		http.Error(w, `{"error":"provider is required"}`, http.StatusBadRequest)
		return
	}

	wf := getWorkflow(req.Workflow)
	if wf == nil {
		http.Error(w, `{"error":"workflow not found"}`, http.StatusNotFound)
		return
	}

	if req.Step < 1 || req.Step > len(wf.Steps) {
		http.Error(w, `{"error":"invalid step index"}`, http.StatusBadRequest)
		return
	}

	step := &wf.Steps[req.Step-1]
	inputA := req.InputA
	if inputA == nil {
		inputA = step.SampleInputA
	}

	exec, err := ExecuteStep(step, inputA, req.Provider)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	resp := ExecuteResponse{
		StepName:   step.Name,
		StepIndex:  step.Index,
		OutputB:    exec.OutputB,
		Model:      exec.Model,
		Provider:   exec.Provider,
		DurationMs: exec.DurationMs,
		RawLLM:     exec.RawResponse,
	}

	if exec.OutputB != nil {
		resp.Gate = runGate(step, inputA, exec.OutputB, req.Provider)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func handleRunPipeline(w http.ResponseWriter, r *http.Request) {
	var req RunPipelineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.Provider == "" {
		http.Error(w, `{"error":"provider is required"}`, http.StatusBadRequest)
		return
	}

	wf := getWorkflow(req.Workflow)
	if wf == nil {
		http.Error(w, `{"error":"workflow not found"}`, http.StatusNotFound)
		return
	}

	pipelineStart := time.Now()
	resp := PipelineResponse{
		Workflow:     req.Workflow,
		Provider:     req.Provider,
		FinalVerdict: "APPROVED",
	}

	currentInput := req.InputA
	if currentInput == nil {
		currentInput = wf.Steps[0].SampleInputA
	}

	for i := range wf.Steps {
		step := &wf.Steps[i]
		stepResult := PipelineStepResult{
			StepName:  step.Name,
			StepIndex: step.Index,
		}

		exec, err := ExecuteStep(step, currentInput, req.Provider)
		if err != nil {
			stepResult.Error = err.Error()
			stepResult.Gate = &FullGateResult{Passed: false, Verdict: "ERROR"}
			resp.Steps = append(resp.Steps, stepResult)
			resp.FinalVerdict = "ERROR"
			break
		}

		stepResult.Model = exec.Model
		stepResult.Provider = exec.Provider
		stepResult.DurationMs = exec.DurationMs
		stepResult.OutputB = exec.OutputB

		if exec.OutputB == nil {
			stepResult.Error = "LLM returned no parseable JSON"
			stepResult.Gate = &FullGateResult{Passed: false, Verdict: "FAIL_PARSE"}
			resp.Steps = append(resp.Steps, stepResult)
			resp.FinalVerdict = "REJECTED"
			break
		}

		gate := runGate(step, currentInput, exec.OutputB, req.Provider)
		stepResult.Gate = gate
		resp.Steps = append(resp.Steps, stepResult)

		if !gate.Passed {
			resp.FinalVerdict = "REJECTED"
			break
		}

		currentInput = chainOutput(step, exec.OutputB, currentInput)
	}

	resp.TotalMs = float64(time.Since(pipelineStart).Milliseconds())
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func chainOutput(step *WorkflowStep, outputB, prevInput map[string]any) map[string]any {
	next := make(map[string]any)
	for k, v := range outputB {
		next[k] = v
	}
	carryFields := []string{
		"application_id", "applicant_name", "applicant_dob",
		"loan_amount", "loan_purpose", "property_type",
		"income_annual", "employment_years",
	}
	for _, f := range carryFields {
		if _, exists := next[f]; !exists {
			if v, ok := prevInput[f]; ok {
				next[f] = v
			}
		}
	}
	return next
}

func handleLoanProviders(w http.ResponseWriter, r *http.Request) {
	type P struct {
		Name  string `json:"name"`
		Model string `json:"model"`
		Type  string `json:"type"`
	}
	var out []P

	out = append(out, P{"ollama", envOr("OLLAMA_MODEL", "llama3.1:8b"), "local"})
	out = append(out, P{"vultr", envOr("VULTR_MODEL", "llama-3.3-70b-instruct"), "cloud"})
	out = append(out, P{"featherless", envOr("FEATHERLESS_MODEL", "meta-llama/Llama-3.1-70B-Instruct"), "cloud"})

	for _, p := range getProviders() {
		out = append(out, P{p.name, p.model, "premium"})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func handleSampleInput(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	wf := getWorkflow(domain)
	if wf == nil {
		http.Error(w, `{"error":"workflow not found"}`, http.StatusNotFound)
		return
	}

	stepID := r.URL.Query().Get("step")
	for _, s := range wf.Steps {
		if s.ID == stepID || fmt.Sprintf("%d", s.Index) == stepID || stepID == "" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(s.SampleInputA)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(wf.Steps[0].SampleInputA)
}

func handleGateExecuteStream(w http.ResponseWriter, r *http.Request) {
	var req GateExecuteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.Provider == "" {
		http.Error(w, `{"error":"provider is required"}`, http.StatusBadRequest)
		return
	}

	wf := getWorkflow(req.Workflow)
	if wf == nil {
		http.Error(w, `{"error":"workflow not found"}`, http.StatusNotFound)
		return
	}

	if req.Step < 1 || req.Step > len(wf.Steps) {
		http.Error(w, `{"error":"invalid step index"}`, http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		handleGateExecute(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	step := &wf.Steps[req.Step-1]
	inputA := req.InputA
	if inputA == nil {
		inputA = step.SampleInputA
	}

	sendSSE := func(event string, data any) {
		j, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, string(j))
		flusher.Flush()
	}

	sendSSE("status", map[string]string{"phase": "executing", "step": step.Name})

	exec, err := ExecuteStep(step, inputA, req.Provider)
	if err != nil {
		sendSSE("error", map[string]string{"error": err.Error()})
		return
	}

	sendSSE("llm_done", map[string]any{
		"model": exec.Model, "duration_ms": exec.DurationMs,
		"has_output": exec.OutputB != nil,
	})

	if exec.OutputB == nil {
		sendSSE("error", map[string]string{"error": "no parseable JSON from LLM"})
		return
	}

	outputJSON, _ := json.Marshal(exec.OutputB)
	l0Start := time.Now()
	l0 := ltInspect(string(outputJSON))
	sendSSE("l0_done", map[string]any{
		"verdict": l0.Verdict, "duration_ms": float64(time.Since(l0Start).Milliseconds()),
		"flags": l0.Flags,
	})

	if l0.Verdict != "ALLOW" {
		sendSSE("gate_done", map[string]any{"passed": false, "verdict": "FAIL_L0"})
		return
	}

	l2Start := time.Now()
	l2 := VerifyPostconditions(step, inputA, exec.OutputB)
	sendSSE("l2_done", map[string]any{
		"verdict":  boolToVerdict(l2.Passed),
		"duration_ms": float64(time.Since(l2Start).Milliseconds()),
		"passed":   l2.PassedChecks, "failed": l2.FailedChecks,
		"skipped":  l2.SkippedChecks, "total": l2.TotalChecks,
	})

	verdict := "PASS"
	if !l2.Passed {
		verdict = "FAIL_L2"
	}

	sendSSE("gate_done", map[string]any{
		"passed":   l2.Passed && l0.Verdict == "ALLOW",
		"verdict":  verdict,
		"output_b": exec.OutputB,
		"step":     step.Name,
		"model":    exec.Model,
	})
}

func boolToVerdict(b bool) string {
	if b {
		return "PASS"
	}
	return "FAIL"
}

func handleRunPipelineStream(w http.ResponseWriter, r *http.Request) {
	var req RunPipelineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.Provider == "" {
		http.Error(w, `{"error":"provider is required"}`, http.StatusBadRequest)
		return
	}

	wf := getWorkflow(req.Workflow)
	if wf == nil {
		http.Error(w, `{"error":"workflow not found"}`, http.StatusNotFound)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		handleRunPipeline(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	sendSSE := func(event string, data any) {
		j, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, string(j))
		flusher.Flush()
	}

	pipelineStart := time.Now()
	currentInput := req.InputA
	if currentInput == nil {
		currentInput = wf.Steps[0].SampleInputA
	}

	sendSSE("pipeline_start", map[string]any{
		"workflow": req.Workflow, "provider": req.Provider, "steps": len(wf.Steps),
	})

	for i := range wf.Steps {
		step := &wf.Steps[i]

		sendSSE("step_start", map[string]any{
			"step": step.Name, "index": step.Index,
		})

		exec, err := ExecuteStep(step, currentInput, req.Provider)
		if err != nil {
			sendSSE("step_error", map[string]any{
				"step": step.Name, "index": step.Index, "error": err.Error(),
			})
			sendSSE("pipeline_done", map[string]any{
				"final_verdict": "ERROR",
				"total_ms":      float64(time.Since(pipelineStart).Milliseconds()),
				"steps_completed": i,
			})
			return
		}

		sendSSE("step_llm_done", map[string]any{
			"step": step.Name, "index": step.Index,
			"model": exec.Model, "duration_ms": exec.DurationMs,
			"has_output": exec.OutputB != nil,
		})

		if exec.OutputB == nil {
			sendSSE("step_error", map[string]any{
				"step": step.Name, "index": step.Index,
				"error": "no parseable JSON from LLM",
			})
			sendSSE("pipeline_done", map[string]any{
				"final_verdict": "REJECTED", "steps_completed": i,
				"total_ms": float64(time.Since(pipelineStart).Milliseconds()),
			})
			return
		}

		outputJSON, _ := json.Marshal(exec.OutputB)
		l0 := ltInspect(string(outputJSON))
		sendSSE("step_l0_done", map[string]any{
			"step": step.Name, "index": step.Index,
			"verdict": l0.Verdict, "flags": l0.Flags,
		})

		if l0.Verdict != "ALLOW" {
			sendSSE("step_gate_done", map[string]any{
				"step": step.Name, "index": step.Index,
				"passed": false, "verdict": "FAIL_L0",
			})
			sendSSE("pipeline_done", map[string]any{
				"final_verdict": "REJECTED", "steps_completed": i,
				"total_ms": float64(time.Since(pipelineStart).Milliseconds()),
			})
			return
		}

		l2 := VerifyPostconditions(step, currentInput, exec.OutputB)
		stepVerdict := "PASS"
		if !l2.Passed {
			stepVerdict = "FAIL_L2"
		}

		sendSSE("step_gate_done", map[string]any{
			"step": step.Name, "index": step.Index,
			"passed": l2.Passed, "verdict": stepVerdict,
			"output_b":       exec.OutputB,
			"l2_passed":      l2.PassedChecks,
			"l2_failed":      l2.FailedChecks,
			"l2_skipped":     l2.SkippedChecks,
			"l2_total":       l2.TotalChecks,
			"l2_checks":      l2.Checks,
		})

		if !l2.Passed {
			sendSSE("pipeline_done", map[string]any{
				"final_verdict": "REJECTED", "steps_completed": i,
				"total_ms": float64(time.Since(pipelineStart).Milliseconds()),
			})
			return
		}

		currentInput = chainOutput(step, exec.OutputB, currentInput)
	}

	sendSSE("pipeline_done", map[string]any{
		"final_verdict":   "APPROVED",
		"steps_completed": len(wf.Steps),
		"total_ms":        float64(time.Since(pipelineStart).Milliseconds()),
	})
}

func init() {
	_ = strings.TrimSpace
}
