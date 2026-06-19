package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func executeCreateProject(args map[string]any, state *CrafterState, sess *SessionData, start time.Time) (*SkillExecResult, error) {
	projectName, _ := args["project_name"].(string)
	if projectName == "" {
		projectName, _ = args["work"].(string)
	}
	if projectName == "" {
		return nil, fmt.Errorf("create-project requires 'project_name' argument")
	}

	slug := slugify(projectName)
	stack, _ := args["stack"].(string)
	if stack == "" {
		stack = "none"
	}
	description, _ := args["description"].(string)
	if description == "" {
		description = projectName
	}

	// Project lives inside .gem-squared/workspace/{slug}/ (global, not session-scoped)
	projectDir := filepath.Join(baseDir, ".gem-squared", "workspace", slug)

	// Duplicate check: if project already exists, bind session to it instead of re-creating
	if info, err := os.Stat(projectDir); err == nil && info.IsDir() {
		log.Printf("[CREATE-PROJECT] project %q already exists, switching to it", slug)
		if sess != nil {
			sess.SetActiveProject(slug)
		}
		return &SkillExecResult{
			Skill:       "create-project",
			OutputB:     fmt.Sprintf("Project **%s** already exists. Switched to it.\n\nUse `/plan-work` to plan new work or `/check-session` to see current state.", slug),
			StateChange: fmt.Sprintf("Switched to existing project %s", slug),
			Duration:    time.Since(start).Milliseconds(),
		}, nil
	}

	log.Printf("[CREATE-PROJECT] creating %q at %s (stack=%s)", projectName, projectDir, stack)

	// 1. Scaffold directory structure
	dirs := []string{
		"",
		"work-plan",
		"archive",
		"verify-work-logs",
		filepath.Join("output", slug),
	}
	for _, d := range dirs {
		os.MkdirAll(filepath.Join(projectDir, d), 0755)
	}

	// 2. Write alarm.md (project-scoped source of truth)
	now := time.Now().Format(time.RFC3339)
	alarmContent := fmt.Sprintf(`# %s — Session Alarm
**Project:** %s | **project_slug:** %s
**Updated:** %s

## Status Counters
PENDING:0 | IN_PROGRESS:0 | COMPLETED:0 | DECOMPOSED:0 | ABORTED:0

## Active Tasks
(none)

## Notes
- LLM-agnostic orchestrator mode — Gemini-powered
- Stack: %s
`, projectName, projectName, slug, now, stack)

	alarmPath := filepath.Join(projectDir, "alarm.md")
	if _, err := os.Stat(alarmPath); os.IsNotExist(err) {
		os.WriteFile(alarmPath, []byte(alarmContent), 0644)
	}

	// 3. Write project.json (metadata for the UI)
	projectMeta := fmt.Sprintf(`{
  "slug": %q,
  "name": %q,
  "description": %q,
  "stack": %q,
  "created_at": %q
}`, slug, projectName, description, stack, now)
	os.WriteFile(filepath.Join(projectDir, "project.json"), []byte(projectMeta), 0644)

	// 4. Stack-specific setup
	var stackFile string
	switch stack {
	case "go":
		stackFile = "go.mod"
		modPath := filepath.Join(projectDir, "output", slug, "go.mod")
		if _, err := os.Stat(modPath); os.IsNotExist(err) {
			os.WriteFile(modPath, []byte(fmt.Sprintf("module %s\n\ngo 1.22\n", slug)), 0644)
		}
	case "node":
		stackFile = "package.json"
		pkgPath := filepath.Join(projectDir, "output", slug, "package.json")
		if _, err := os.Stat(pkgPath); os.IsNotExist(err) {
			os.WriteFile(pkgPath, []byte(fmt.Sprintf(`{"name": %q, "version": "0.1.0"}`, slug)), 0644)
		}
	case "python":
		stackFile = "pyproject.toml"
		pyPath := filepath.Join(projectDir, "output", slug, "pyproject.toml")
		if _, err := os.Stat(pyPath); os.IsNotExist(err) {
			os.WriteFile(pyPath, []byte(fmt.Sprintf("[project]\nname = %q\nversion = \"0.1.0\"\n", slug)), 0644)
		}
	}

	// Count created artifacts
	var files []string
	filepath.Walk(projectDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(projectDir, path)
		files = append(files, rel)
		return nil
	})

	var output strings.Builder
	output.WriteString(fmt.Sprintf("## Project Created: %s\n\n", projectName))
	output.WriteString(fmt.Sprintf("- **Slug:** `%s`\n", slug))
	output.WriteString(fmt.Sprintf("- **Path:** `workspace/%s/`\n", slug))
	output.WriteString(fmt.Sprintf("- **Stack:** %s\n", stack))
	if stackFile != "" {
		output.WriteString(fmt.Sprintf("- **Stack file:** `%s`\n", stackFile))
	}
	output.WriteString(fmt.Sprintf("- **Files:** %d created\n", len(files)))
	output.WriteString("\n**Structure:**\n")
	output.WriteString(fmt.Sprintf("```\nworkspace/%s/\n", slug))
	output.WriteString("  alarm.md          ← project-scoped source of truth\n")
	output.WriteString("  project.json      ← metadata\n")
	output.WriteString("  work-plan/        ← TPMN work plans\n")
	output.WriteString("  archive/          ← completed work\n")
	output.WriteString(fmt.Sprintf("  output/%s/  ← code artifacts (deployable)\n", slug))
	output.WriteString("```\n\n")
	output.WriteString("**Next:** Use `/plan-work` to plan your first task for this project.")

	if sess != nil {
		sess.SetActiveProject(slug)
	}

	return &SkillExecResult{
		Skill:         "create-project",
		OutputB:       output.String(),
		StateChange:   fmt.Sprintf("Created project %s at workspace/%s/", projectName, slug),
		Duration:      time.Since(start).Milliseconds(),
		FilesModified: files,
	}, nil
}

func slugify(name string) string {
	s := strings.ToLower(name)
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		if r == ' ' || r == '_' {
			return '-'
		}
		return -1
	}, s)
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}

// executeDeployWork is the WP-AO-35 deploy-work TPMN skill. Closes the
// canonical pipeline I→P→E→V→D with a concrete "now it's live" artifact.
// Three output_kind branches:
//   "project"  → output/{slug}/ has files served at host/p/{slug}/
//   "ce-only"  → CE registered under this project — point user back at viewer
//   "empty"    → nothing produced; graceful "nothing to deploy" message
func executeDeployWork(args map[string]any, state *CrafterState, sess *SessionData, start time.Time) (*SkillExecResult, error) {
	if sess == nil || sess.ActiveProject == "" {
		return nil, fmt.Errorf("deploy-work needs an active project — create or switch to one first")
	}
	slug := sess.ActiveProject
	if override, ok := args["project_slug"].(string); ok && override != "" {
		slug = override
	}
	projectDir := filepath.Join(baseDir, ".gem-squared", "workspace", slug)
	if info, err := os.Stat(projectDir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("deploy-work: project dir not found: %s", projectDir)
	}

	outputDir := filepath.Join(projectDir, "output", slug)
	var files []string
	if info, err := os.Stat(outputDir); err == nil && info.IsDir() {
		filepath.Walk(outputDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(outputDir, path)
			files = append(files, rel)
			return nil
		})
	}

	if len(files) > 0 {
		url := fmt.Sprintf("%s/p/%s/", strings.TrimRight(ceProductionHost, "/"), slug)
		out := fmt.Sprintf("## Deployed!\n\n**Project files served live.**\n\n**Files:** %d\n\n[[CE_VIEWER_BUTTON|%s]]\n\n*(Button labelled \"Open CE Viewer\" — viewer pill is shared across deploy + create-ce artifacts.)*", len(files), url)
		return &SkillExecResult{
			Skill:         "deploy-work",
			OutputB:       out,
			StateChange:   fmt.Sprintf("Deployed %d files at %s", len(files), url),
			Duration:      time.Since(start).Milliseconds(),
			FilesModified: files,
		}, nil
	}

	// output/ is empty — check if a CE is registered under this project
	ceURL := findCEViewerURLForProject(projectDir)
	if ceURL != "" {
		out := fmt.Sprintf("## CE Deploy complete\n\nThe Contract-Executor created in this project is already live. /create-ce atomically includes deploy, so no separate deployment step was needed.\n\n[[CE_VIEWER_BUTTON|%s]]", ceURL)
		return &SkillExecResult{
			Skill:       "deploy-work",
			OutputB:     out,
			StateChange: "CE-only deploy — viewer URL re-emitted",
			Duration:    time.Since(start).Milliseconds(),
		}, nil
	}

	return &SkillExecResult{
		Skill:       "deploy-work",
		OutputB:     fmt.Sprintf("## Nothing to deploy\n\nNo output artifacts under `output/%s/`, and no Contract-Executor registered under this project. If you intended a CE flow, upload a TPMN contract and run `/create-ce`.", slug),
		StateChange: "deploy-work: empty output, no CE",
		Duration:    time.Since(start).Milliseconds(),
	}, nil
}

// findCEViewerURLForProject returns the viewer URL for the most recently
// created CE whose source contract lives under {projectDir}/uploaded_files/.
// Empty string if none. Used by deploy-work to re-emit the viewer for CE-only
// flows whose project output dir is empty by design.
func findCEViewerURLForProject(projectDir string) string {
	uploadDir := filepath.Join(projectDir, "uploaded_files")
	if _, err := os.Stat(uploadDir); err != nil {
		return ""
	}
	specs, err := listCESpecs()
	if err != nil {
		return ""
	}
	absUpload, _ := filepath.Abs(uploadDir)
	var best *CESpec
	for i := range specs {
		s := &specs[i]
		if s.SourceFile == "" {
			continue
		}
		absSource, _ := filepath.Abs(s.SourceFile)
		if !strings.HasPrefix(absSource, absUpload) {
			continue
		}
		if best == nil || s.UpdatedAt > best.UpdatedAt {
			best = s
		}
	}
	if best == nil {
		return ""
	}
	apiPath := fmt.Sprintf("/ce/%s/%s/", best.WorkflowSlug, best.StageSlug)
	return buildCEViewerURL(ceProductionHost, best, apiPath)
}
