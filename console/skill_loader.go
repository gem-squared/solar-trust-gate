package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// crafterCommonSkillsFS embeds the LLM-agnostic skill catalog at compile time.
// Source of truth: console/crafter-common-skills/{name}/SKILL.md (em-dash U+2014).
// See memory file project_skill_config_layout.md for the doctrine.
//
//go:embed all:crafter-common-skills
var crafterCommonSkillsFS embed.FS

type SkillSpec struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	ArgumentHint string `json:"argument_hint,omitempty"`
	Version      string `json:"version,omitempty"`
	RawBody      string `json:"-"`
	FilePath     string `json:"-"`
}

var globalSkills []SkillSpec

// LoadSkills walks a host-filesystem directory (kept for dev override / future
// per-provider directories). Not used at startup — see LoadSkillsFromFS.
func LoadSkills(dir string) ([]SkillSpec, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read skills dir: %w", err)
	}

	var skills []SkillSpec
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name(), "SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		skill := parseSkillMD(string(data), path)
		if skill.Name != "" {
			skills = append(skills, skill)
		}
	}
	return skills, nil
}

// LoadSkillsFromFS walks an fs.FS rooted at the embed dir (or any compatible FS)
// and returns parsed SkillSpec entries for each {name}/SKILL.md found. This is
// the LLM-agnostic loader used at startup — replaces the prior os.UserHomeDir()
// read which leaked Claude Code's installation namespace into the runtime.
func LoadSkillsFromFS(fsys fs.FS, rootPrefix string) ([]SkillSpec, error) {
	entries, err := fs.ReadDir(fsys, rootPrefix)
	if err != nil {
		return nil, fmt.Errorf("read embedded skills root %q: %w", rootPrefix, err)
	}

	var skills []SkillSpec
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := rootPrefix + "/" + e.Name() + "/SKILL.md"
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			continue // skill dir without a SKILL.md → skip silently
		}
		skill := parseSkillMD(string(data), path)
		if skill.Name != "" {
			skills = append(skills, skill)
		}
	}
	return skills, nil
}

func parseSkillMD(content, path string) SkillSpec {
	s := SkillSpec{FilePath: path}

	parts := strings.SplitN(content, "---", 3)
	if len(parts) < 3 {
		return s
	}
	frontmatter := parts[1]
	body := parts[2]

	s.Name = extractYAML(frontmatter, "name")
	s.Description = extractYAMLMultiline(frontmatter, "description")
	s.ArgumentHint = extractYAML(frontmatter, "argument-hint")
	s.Version = extractNestedYAML(frontmatter, "version")
	s.RawBody = strings.TrimSpace(body)

	return s
}

func extractYAML(fm, key string) string {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `:\s*"?([^"\n]+)"?`)
	m := re.FindStringSubmatch(fm)
	if len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func extractYAMLMultiline(fm, key string) string {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `:\s*>\s*\n((?:\s+.*\n?)*)`)
	m := re.FindStringSubmatch(fm)
	if len(m) > 1 {
		lines := strings.Split(strings.TrimRight(m[1], "\n"), "\n")
		var parts []string
		for _, l := range lines {
			parts = append(parts, strings.TrimSpace(l))
		}
		return strings.Join(parts, " ")
	}
	return extractYAML(fm, key)
}

func extractNestedYAML(fm, key string) string {
	re := regexp.MustCompile(`(?m)^\s+` + regexp.QuoteMeta(key) + `:\s*(.+)`)
	m := re.FindStringSubmatch(fm)
	if len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func BuildCatalog(skills []SkillSpec) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("## Available Skills (%d Implanted Skills)\n\n", len(skills)))
	for i, s := range skills {
		b.WriteString(fmt.Sprintf("%d. **/%s**", i+1, s.Name))
		if s.ArgumentHint != "" {
			b.WriteString(fmt.Sprintf(" %s", s.ArgumentHint))
		}
		b.WriteString("\n")
		if s.Description != "" {
			b.WriteString(fmt.Sprintf("   %s\n", s.Description))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func GetSkill(name string) *SkillSpec {
	clean := strings.TrimPrefix(name, "/")
	for i := range globalSkills {
		if globalSkills[i].Name == clean {
			return &globalSkills[i]
		}
	}
	return nil
}

// InitSkills loads the implanted skill catalog from the embed.FS baked into
// the binary at compile time. LLM-agnostic — no host filesystem dependency,
// no Claude Code installation coupling. See WP-AO-27 + memory file
// project_skill_config_layout.md for the full doctrine.
func InitSkills() error {
	skills, err := LoadSkillsFromFS(crafterCommonSkillsFS, "crafter-common-skills")
	if err != nil {
		return fmt.Errorf("init implanted skills: %w", err)
	}
	globalSkills = skills

	// Belt-and-suspenders builtins per WP-AO-26 D3 — registered ONLY if not
	// already loaded from embed (avoids double-registration). If the SKILL.md
	// file is missing for any reason, the builtin keeps the dispatcher alive.
	builtins := []SkillSpec{
		{Name: "create-project", Description: "Create a new project in /workspace with alarm.md, work-plan/, archive/. Args: project_name, stack (go/node/python/none), description.", ArgumentHint: "<project_name>"},
		{Name: "create-ce", Description: "Generate a Contract-Executor micro-service from an uploaded TPMN contract markdown file. Parses A/P_pre/F/B/P_post, writes ce-registry/{wf}/{stage}.json, makes /ce/{wf}/{stage}/ immediately callable. Args: file_path (under uploaded_files/), optional workflow_slug, stage_slug, vultr_model, force.", ArgumentHint: "<file_path>"},
	}
	for _, b := range builtins {
		if GetSkill(b.Name) == nil {
			globalSkills = append(globalSkills, b)
		}
	}

	fmt.Printf("Loaded %d implanted skills\n", len(globalSkills))
	return nil
}
