package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"
)

type InspectRequest struct {
	Content string `json:"content"`
}

type AuditRequest struct {
	Content    string `json:"content"`
	Provider   string `json:"provider,omitempty"`
	WithLedger bool   `json:"with_ledger,omitempty"`
}

type DemoRequest struct {
	Content  string `json:"content"`
	Provider string `json:"provider,omitempty"`
}

type DemoResponse struct {
	Content  string      `json:"content"`
	LT       LTResult    `json:"lt"`
	GEM2     *GEM2Result `json:"gem2,omitempty"`
	GEM2Error string     `json:"gem2_error,omitempty"`
}

func handleInspect(w http.ResponseWriter, r *http.Request) {
	var req InspectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.Content == "" {
		http.Error(w, `{"error":"content is required"}`, http.StatusBadRequest)
		return
	}

	result := ltInspect(req.Content)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func handleAudit(w http.ResponseWriter, r *http.Request) {
	var req AuditRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.Content == "" {
		http.Error(w, `{"error":"content is required"}`, http.StatusBadRequest)
		return
	}

	auditContent := req.Content
	if req.WithLedger {
		auditContent = buildLedgerContext() + req.Content
	}

	result, err := gem2Audit(auditContent, req.Provider)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func handleDemo(w http.ResponseWriter, r *http.Request) {
	var req DemoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.Content == "" {
		http.Error(w, `{"error":"content is required"}`, http.StatusBadRequest)
		return
	}

	lt := ltInspect(req.Content)

	resp := DemoResponse{
		Content: req.Content,
		LT:      lt,
	}

	gem2, err := gem2Audit(req.Content, req.Provider)
	if err != nil {
		resp.GEM2Error = err.Error()
	} else {
		resp.GEM2 = gem2
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func handleProviders(w http.ResponseWriter, r *http.Request) {
	providers := getProviders()
	type ProviderInfo struct {
		Name  string `json:"name"`
		Model string `json:"model"`
	}
	var out []ProviderInfo
	for _, p := range providers {
		out = append(out, ProviderInfo{Name: p.name, Model: p.model})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func handleScenarios(w http.ResponseWriter, r *http.Request) {
	withGEM2 := r.URL.Query().Get("gem2") == "true"
	provider := r.URL.Query().Get("provider")
	result := runScenarios(withGEM2, provider)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func handleScenarioByID(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, `{"error":"invalid scenario id"}`, http.StatusBadRequest)
		return
	}

	var scenario *Scenario
	for _, s := range allScenarios {
		if s.ID == id {
			scenario = &s
			break
		}
	}
	if scenario == nil {
		http.Error(w, `{"error":"scenario not found"}`, http.StatusNotFound)
		return
	}

	lt := ltInspect(scenario.InputContent)
	sr := ScenarioResult{
		Scenario: *scenario,
		LT:       lt,
		LTPassed: lt.Verdict == scenario.ExpectedLTVerdict,
		Passed:   lt.Verdict == scenario.ExpectedLTVerdict,
	}

	withGEM2 := r.URL.Query().Get("gem2") == "true"
	provider := r.URL.Query().Get("provider")
	if withGEM2 && lt.Verdict == "ALLOW" {
		gem2, err := gem2Audit(scenario.InputContent, provider)
		if err == nil {
			sr.GEM2 = gem2
			if scenario.ExpectedGEM2Verdict != "" {
				sr.GEM2Passed = gem2.Verdict == scenario.ExpectedGEM2Verdict
				sr.Passed = sr.LTPassed && sr.GEM2Passed
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sr)
}

func handleLedger(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ledgerAccounts)
}

func handleComplianceSamples(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(complianceSamples)
}

func handleCompliance(w http.ResponseWriter, r *http.Request) {
	var req AuditRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.Content == "" {
		http.Error(w, `{"error":"content is required"}`, http.StatusBadRequest)
		return
	}

	start := time.Now()

	matched := searchRegulations(req.Content, 3)
	if len(matched) == 0 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ComplianceResult{
			Verdict:    "NO_MATCH",
			TruthScore: 0,
			DurationMs: float64(time.Since(start).Milliseconds()),
		})
		return
	}

	augmented := buildCompliancePromptWithLedger(req.Content, matched)
	gem2, err := gem2Audit(augmented, req.Provider)

	result := ComplianceResult{
		MatchedRegulations: matched,
		DurationMs:         float64(time.Since(start).Milliseconds()),
	}
	if err != nil {
		result.Verdict = "ERROR"
	} else {
		result.GEM2 = gem2
		result.TruthScore = gem2.TruthScore
		result.Verdict = gem2.Verdict
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func handleInterpretScorecard(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	var req struct {
		Provider string `json:"provider"`
	}
	json.Unmarshal(body, &req)

	result, err := gem2InterpretProxy("/api/v1/interpret-scorecard", body, req.Provider)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(result)
}

func handleInterpretScenario(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	var parsed map[string]json.RawMessage
	json.Unmarshal(body, &parsed)

	var req struct {
		Provider string `json:"provider"`
	}
	json.Unmarshal(body, &req)

	if compRaw, ok := parsed["compliance"]; ok {
		var comp struct {
			Verdict            string `json:"verdict"`
			TruthScore         int    `json:"truth_score"`
			MatchedRegulations []struct {
				Regulation struct {
					Framework string `json:"framework"`
					Article   string `json:"article"`
					Title     string `json:"title"`
				} `json:"regulation"`
			} `json:"matched_regulations"`
		}
		if json.Unmarshal(compRaw, &comp) == nil && comp.Verdict != "" {
			var scenario map[string]interface{}
			if scRaw, ok := parsed["scenario"]; ok {
				json.Unmarshal(scRaw, &scenario)
			}
			if scenario == nil {
				scenario = map[string]interface{}{}
			}

			compSummary := "\n\n[Layer 2 — Compliance Check]\nVerdict: " + comp.Verdict
			compSummary += "\nTruth Score: " + strconv.Itoa(comp.TruthScore)
			for _, rm := range comp.MatchedRegulations {
				compSummary += "\nMatched: " + rm.Regulation.Framework + " " + rm.Regulation.Article + " — " + rm.Regulation.Title
			}
			compSummary += "\nInclude Layer 2 Compliance Check assessment in your interpretation."

			if content, ok := scenario["content"].(string); ok {
				scenario["content"] = content + compSummary
			} else {
				scenario["content"] = compSummary
			}

			scBytes, _ := json.Marshal(scenario)
			parsed["scenario"] = json.RawMessage(scBytes)
			body, _ = json.Marshal(parsed)
		}
	}

	result, err := gem2InterpretProxy("/api/v1/interpret-scenario", body, req.Provider)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(result)
}
