package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ── CE (Contract-Executor) Registry ────────────────────────────────
//
// File-backed registry of Contract-Executors. Each CE is a {WorkflowSlug,
// StageSlug} pair whose runtime behavior is fully determined by a TPMN
// contract (per Docs/contract-authoring-guide.md). When a user uploads a
// contract and triggers /create-ce, parseContractFile populates a CESpec
// and saveCESpec writes it to:
//
//   {baseDir}/.gem-squared/ce-registry/{WorkflowSlug}/{StageSlug}.json
//
// At /ce/{workflow}/{stage}/ invocation time, handleCEInvoke loads the spec
// and runs the Vultr LLM call using FBlock as the system prompt body.
//
// The CE handler is execute-only — L1 / L2 audit gates are the orchestrator's
// concern (WP-AO-27). See WP-AO-26 Result block of Unit 1.

// CESpec is the persisted definition of one Contract-Executor.
type CESpec struct {
	WorkflowSlug  string            `json:"workflow_slug"`
	StageSlug     string            `json:"stage_slug"`
	ContractTitle string            `json:"contract_title"`
	Domain        string            `json:"domain,omitempty"`

	// 5 mandatory blocks (per Docs/contract-authoring-guide.md §3).
	// A and B are kept as interface{} to preserve YAML-as-string OR JSON-as-object.
	A      interface{} `json:"a"`
	PPre   []string    `json:"p_pre"`  // flattened, "[Subgroup] rule" prefix
	FBlock string      `json:"f_block"` // verbatim — CE system prompt body
	B      interface{} `json:"b"`
	PPost  []string    `json:"p_post"`

	// Circus Executor metadata
	StageType   string `json:"stage_type,omitempty"`     // deterministic | hybrid | llm-assisted
	AgentRole   string `json:"agent_role,omitempty"`     // kebab-case slug
	TrustGateL0 int    `json:"trust_gate_l0"`            // 0-100 (WP-01 U6 — 0 = layer skipped)
	TrustGateL1 int    `json:"trust_gate_l1"`            // 0-100
	TrustGateL2 int    `json:"trust_gate_l2"`            // 0-100
	TrustGateL3 int    `json:"trust_gate_l3"`            // 0-100 (WP-01 U6 — 0 = layer skipped)
	VultrModel  string `json:"vultr_model,omitempty"`    // empty → registry default

	// Optional reference data (policy ID → text) — orchestrator may populate
	// at call time; this WP keeps it empty.
	ReferenceData map[string]string `json:"reference_data,omitempty"`

	// SampleI is the CURRENT judge-editable sample input. CE viewer's Save
	// button writes here. Canvas workflow runs use this as the first-node
	// input override (if set), making judge edits applied to next Run.
	SampleI string `json:"sample_i,omitempty"`

	// OriginalSampleI is the FROZEN standard input set ONCE at CE creation
	// (by Vultr at /create-ce time, never modified afterwards). Provides a
	// canonical reset baseline judges can revert to — the 'STANDARD
	// CORRECTION DATA' surfaced read-only in the CE viewer.
	OriginalSampleI string `json:"original_sample_i,omitempty"`

	// Provenance
	SourceFile string `json:"source_file,omitempty"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// defaultVultrModel is used when a contract's Circus Executor block doesn't
// name a specific model. DeepSeek-V3.2-NVFP4 chosen as default — verified
// reliable end-to-end during WP-AO-26 Unit 6 smoke test (1.66s response with
// valid structured JSON). vultr/Kimi-K2.6 was first choice per demo-action-plan
// but exhibits upstream Vultr-side routing failure (returns 404 referencing a
// different Nemotron model even when called via the correct moonshotai/Kimi-K2.6
// full namespaced ID). Authors who want Kimi can still set vultr_model
// explicitly in their contract's Circus Executor block.
const defaultVultrModel = "vultr/DeepSeek-V3.2-NVFP4"

// slugPattern enforces ASCII kebab-case to keep filesystem paths safe + URL safe.
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}[a-z0-9]$|^[a-z0-9]$`)

// ceRegistryRoot returns the directory that holds all CE specs. Overridable
// via CE_REGISTRY_DIR for testing.
func ceRegistryRoot() string {
	if v := os.Getenv("CE_REGISTRY_DIR"); v != "" {
		return v
	}
	return filepath.Join(baseDir, ".gem-squared", "ce-registry")
}

func cePath(workflow, stage string) string {
	return filepath.Join(ceRegistryRoot(), workflow, stage+".json")
}

// validateCESpec runs sanity checks before save. Returns the first failure.
func validateCESpec(spec *CESpec) error {
	if spec == nil {
		return fmt.Errorf("nil spec")
	}
	if !slugPattern.MatchString(spec.WorkflowSlug) {
		return fmt.Errorf("invalid workflow_slug %q — must match %s", spec.WorkflowSlug, slugPattern.String())
	}
	if !slugPattern.MatchString(spec.StageSlug) {
		return fmt.Errorf("invalid stage_slug %q — must match %s", spec.StageSlug, slugPattern.String())
	}
	if strings.TrimSpace(spec.FBlock) == "" {
		return fmt.Errorf("f_block is empty — contract must have a non-empty F: Processing Logic section")
	}
	if spec.A == nil {
		return fmt.Errorf("a (input type) is nil")
	}
	if spec.B == nil {
		return fmt.Errorf("b (output type) is nil")
	}
	if spec.TrustGateL1 < 0 || spec.TrustGateL1 > 100 {
		return fmt.Errorf("trust_gate_l1 %d out of range [0,100]", spec.TrustGateL1)
	}
	if spec.TrustGateL2 < 0 || spec.TrustGateL2 > 100 {
		return fmt.Errorf("trust_gate_l2 %d out of range [0,100]", spec.TrustGateL2)
	}
	if spec.VultrModel != "" {
		// non-empty model — must be a known Vultr model (per WP-AO-23 table)
		if _, ok := modelContextTable[spec.VultrModel]; !ok {
			return fmt.Errorf("vultr_model %q not in modelContextTable — see console/model_context.go", spec.VultrModel)
		}
	}
	return nil
}

// saveCESpec writes the spec to disk atomically (tmp file + rename).
// Creates parent directories as needed. Stamps timestamps.
func saveCESpec(spec *CESpec) error {
	if err := validateCESpec(spec); err != nil {
		return fmt.Errorf("validate: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if spec.CreatedAt == "" {
		spec.CreatedAt = now
	}
	spec.UpdatedAt = now
	if spec.VultrModel == "" {
		// Don't mutate spec.VultrModel — leave empty so registry returns
		// defaultVultrModel at lookup time. Empty in JSON means "use default".
	}

	dir := filepath.Dir(cePath(spec.WorkflowSlug, spec.StageSlug))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	final := cePath(spec.WorkflowSlug, spec.StageSlug)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// loadCESpec reads {workflow}/{stage}.json. Returns os.ErrNotExist-wrapped
// error if the file is missing — callers can use errors.Is(err, os.ErrNotExist).
func loadCESpec(workflow, stage string) (*CESpec, error) {
	if !slugPattern.MatchString(workflow) {
		return nil, fmt.Errorf("invalid workflow slug %q", workflow)
	}
	if !slugPattern.MatchString(stage) {
		return nil, fmt.Errorf("invalid stage slug %q", stage)
	}
	data, err := os.ReadFile(cePath(workflow, stage))
	if err != nil {
		return nil, err // os.IsNotExist / errors.Is(err, os.ErrNotExist) works
	}
	var spec CESpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("unmarshal %s/%s: %w", workflow, stage, err)
	}
	return &spec, nil
}

// listCESpecs walks the registry root and returns all specs (lighter — no
// FBlock content) for the management endpoint and orchestrator context.
func listCESpecs() ([]CESpec, error) {
	root := ceRegistryRoot()
	out := []CESpec{}
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return out, nil // empty registry is valid
	}

	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil // skip unreadable
		}
		var spec CESpec
		if err := json.Unmarshal(data, &spec); err != nil {
			return nil // skip malformed
		}
		out = append(out, spec)
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].WorkflowSlug != out[j].WorkflowSlug {
			return out[i].WorkflowSlug < out[j].WorkflowSlug
		}
		return out[i].StageSlug < out[j].StageSlug
	})
	return out, nil
}

// deleteCESpec removes one CE from the registry.
func deleteCESpec(workflow, stage string) error {
	if !slugPattern.MatchString(workflow) || !slugPattern.MatchString(stage) {
		return fmt.Errorf("invalid slug")
	}
	path := cePath(workflow, stage)
	if err := os.Remove(path); err != nil {
		return err
	}
	// Best-effort: remove the workflow dir if it's empty now (keeps registry tidy).
	parent := filepath.Dir(path)
	if entries, err := os.ReadDir(parent); err == nil && len(entries) == 0 {
		_ = os.Remove(parent)
	}
	return nil
}

// resolvedVultrModel returns the model the CE should call — spec value or
// default. Used by the CE handler.
func (s *CESpec) resolvedVultrModel() string {
	if s.VultrModel != "" {
		return s.VultrModel
	}
	return defaultVultrModel
}
