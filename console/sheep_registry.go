package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
)

type SheepDef struct {
	ID        string   `json:"id"`
	Model     string   `json:"model"`
	Endpoint  string   `json:"endpoint"`
	Tier      string   `json:"tier"`
	CostIn    float64  `json:"cost_input"`
	CostOut   float64  `json:"cost_output"`
	Strengths []string `json:"strengths"`
}

type SheepPreset struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Assignments map[string]string `json:"assignments"` // contract pattern -> sheep id
	Default     string            `json:"default"`     // fallback sheep id
}

type SheepRegistry struct {
	Sheep   []SheepDef    `json:"sheep"`
	Presets []SheepPreset `json:"presets"`
}

var globalSheepRegistry *SheepRegistry

func init() {
	globalSheepRegistry = defaultSheepRegistry()
}

func defaultSheepRegistry() *SheepRegistry {
	return &SheepRegistry{
		Sheep: []SheepDef{
			{
				ID:        "sheep-default",
				Model:     "Kimi-K2.6",
				Endpoint:  "https://api.vultrinference.com/v1",
				Tier:      "standard",
				CostIn:    0.15,
				CostOut:   0.60,
				Strengths: []string{"instruction-following", "json-output", "general-purpose", "fast"},
			},
			{
				ID:        "sheep-reasoning",
				Model:     "DeepSeek-V3.2-NVFP4",
				Endpoint:  "https://api.vultrinference.com/v1",
				Tier:      "premium",
				CostIn:    0.55,
				CostOut:   1.65,
				Strengths: []string{"complex-reasoning", "medical-codes", "cross-referencing", "multi-factor-analysis"},
			},
			{
				ID:        "sheep-fast",
				Model:     "Nemotron-Cascade-2-30B-A3B",
				Endpoint:  "https://api.vultrinference.com/v1",
				Tier:      "budget",
				CostIn:    0.15,
				CostOut:   0.60,
				Strengths: []string{"fast", "structured-output", "arithmetic", "validation"},
			},
			{
				ID:        "sheep-heavy",
				Model:     "Qwen3.5-397B-A17B-FP8",
				Endpoint:  "https://api.vultrinference.com/v1",
				Tier:      "premium",
				CostIn:    0.30,
				CostOut:   1.20,
				Strengths: []string{"large-model", "397B-MoE", "complex-reasoning", "deep-analysis"},
			},
		},
		Presets: []SheepPreset{
			{
				Name:        "budget",
				Description: "All contracts use cheapest model — cost-sensitive",
				Assignments: map[string]string{},
				Default:     "sheep-fast",
			},
			{
				Name:        "standard",
				Description: "Reasoning model for complex contracts, fast model for the rest",
				Assignments: map[string]string{
					"claim-04": "sheep-reasoning",
				},
				Default: "sheep-default",
			},
			{
				Name:        "premium",
				Description: "Reasoning model for complex contracts, heavy model for the rest",
				Assignments: map[string]string{
					"claim-03": "sheep-reasoning",
					"claim-04": "sheep-reasoning",
					"claim-05": "sheep-reasoning",
				},
				Default: "sheep-heavy",
			},
		},
	}
}

func loadSheepRegistryFromFile(path string) (*SheepRegistry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read sheep registry: %w", err)
	}
	var reg SheepRegistry
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("parse sheep registry: %w", err)
	}
	return &reg, nil
}

func (r *SheepRegistry) FindSheep(id string) *SheepDef {
	for i := range r.Sheep {
		if r.Sheep[i].ID == id {
			return &r.Sheep[i]
		}
	}
	return nil
}

func (r *SheepRegistry) FindByModel(model string) *SheepDef {
	for i := range r.Sheep {
		if strings.EqualFold(r.Sheep[i].Model, model) {
			return &r.Sheep[i]
		}
	}
	return nil
}

func (r *SheepRegistry) FindPreset(name string) *SheepPreset {
	for i := range r.Presets {
		if r.Presets[i].Name == name {
			return &r.Presets[i]
		}
	}
	return nil
}

func (r *SheepRegistry) AssignSheep(contractID, presetName string) *SheepDef {
	preset := r.FindPreset(presetName)
	if preset == nil {
		preset = r.FindPreset("standard")
	}
	if preset == nil {
		return r.FindSheep("sheep-default")
	}
	if sheepID, ok := preset.Assignments[contractID]; ok {
		if s := r.FindSheep(sheepID); s != nil {
			return s
		}
	}
	return r.FindSheep(preset.Default)
}

func (r *SheepRegistry) AllModels() []string {
	var models []string
	for _, s := range r.Sheep {
		models = append(models, s.Model)
	}
	return models
}

func (r *SheepRegistry) SummaryForWolfi() string {
	var b strings.Builder
	b.WriteString("## Available Sheep (Vultr Serverless Inference)\n\n")
	for _, s := range r.Sheep {
		b.WriteString(fmt.Sprintf("- **%s** (`%s`) — tier: %s, $%.2f/$%.2f per M tokens\n",
			s.ID, s.Model, s.Tier, s.CostIn, s.CostOut))
		b.WriteString(fmt.Sprintf("  Strengths: %s\n", strings.Join(s.Strengths, ", ")))
	}
	b.WriteString("\n## Presets\n\n")
	for _, p := range r.Presets {
		b.WriteString(fmt.Sprintf("- **%s**: %s (default: %s)\n", p.Name, p.Description, p.Default))
		for contract, sheep := range p.Assignments {
			b.WriteString(fmt.Sprintf("  - %s → %s\n", contract, sheep))
		}
	}
	return b.String()
}

// AllVultrModels returns the full catalog for the Agent LLM dropdown
func AllVultrModels() []map[string]any {
	return []map[string]any{
		{"model": "vultr/Llama-3.1-Nemotron-Safety-Guard-8B-v3", "label": "Nemotron Safety Guard 8B ($0.01/$0.01)", "cost_in": 0.01, "cost_out": 0.01},
		{"model": "vultr/Nemotron-3-Nano-Omni-30B-A3B-Reasoning-BF16", "label": "Nemotron Nano 30B Reasoning ($0.13/$0.38)", "cost_in": 0.13, "cost_out": 0.38},
		{"model": "vultr/Kimi-K2.6", "label": "Kimi K2.6 ($0.15/$0.60) — recommended", "cost_in": 0.15, "cost_out": 0.60},
		{"model": "vultr/Nemotron-Cascade-2-30B-A3B", "label": "Nemotron Cascade 30B ($0.15/$0.60)", "cost_in": 0.15, "cost_out": 0.60},
		{"model": "vultr/MiniMax-M2.7", "label": "MiniMax M2.7 ($0.30/$1.20)", "cost_in": 0.30, "cost_out": 1.20},
		{"model": "vultr/Qwen3.5-397B-A17B-FP8", "label": "Qwen 3.5 397B MoE ($0.30/$1.20)", "cost_in": 0.30, "cost_out": 1.20},
		{"model": "vultr/DeepSeek-V3.2-NVFP4", "label": "DeepSeek V3.2 ($0.55/$1.65)", "cost_in": 0.55, "cost_out": 1.65},
		{"model": "vultr/GLM-5.1-FP8", "label": "GLM 5.1 ($0.85/$3.10)", "cost_in": 0.85, "cost_out": 3.10},
	}
}

func initSheepRegistry() {
	regPath := os.Getenv("SHEEP_REGISTRY_PATH")
	if regPath != "" {
		reg, err := loadSheepRegistryFromFile(regPath)
		if err != nil {
			log.Printf("[SHEEP] Failed to load registry from %s: %v — using defaults", regPath, err)
		} else {
			globalSheepRegistry = reg
			log.Printf("[SHEEP] Loaded registry from %s: %d sheep, %d presets", regPath, len(reg.Sheep), len(reg.Presets))
			return
		}
	}
	log.Printf("[SHEEP] Using built-in registry: %d sheep, %d presets", len(globalSheepRegistry.Sheep), len(globalSheepRegistry.Presets))
}
