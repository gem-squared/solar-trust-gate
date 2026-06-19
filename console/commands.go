package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

type CommandResult struct {
	Command  string   `json:"command"`
	Output   string   `json:"output"`
	Files    []string `json:"files_affected,omitempty"`
	Success  bool     `json:"success"`
}

func ExecuteCommand(cmd string, args map[string]any, state *CrafterState) *CommandResult {
	log.Printf("[COMMAND] executing %q args=%v", cmd, args)

	switch cmd {
	case "clean-workplans":
		return cmdCleanWorkplans(state)
	case "clean-workspace":
		return cmdCleanWorkspace(state)
	case "delete-workplan":
		wpID, _ := args["wp_id"].(string)
		return cmdDeleteWorkplan(state, wpID)
	case "list-workspace":
		return cmdListWorkspace(state)
	case "reset-session":
		return cmdResetSession(state)
	case "switch-project":
		query, _ := args["query"].(string)
		return cmdSwitchProject(state, query)
	case "check-all-sessions":
		return cmdCheckAllSessions(state)
	case "deploy-work":
		return cmdDeployWorkByState(state, args)
	default:
		return &CommandResult{Command: cmd, Output: fmt.Sprintf("Unknown command: %s", cmd), Success: false}
	}
}

func cmdCleanWorkplans(state *CrafterState) *CommandResult {
	dir := state.WorkPlanDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return &CommandResult{Command: "clean-workplans", Output: fmt.Sprintf("Error reading: %v", err), Success: false}
	}

	var removed []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			path := filepath.Join(dir, e.Name())
			os.Remove(path)
			removed = append(removed, e.Name())
		}
	}

	state.UpdateAlarmCounters()
	return &CommandResult{
		Command: "clean-workplans",
		Output:  fmt.Sprintf("Removed %d work plan(s): %s", len(removed), strings.Join(removed, ", ")),
		Files:   removed,
		Success: true,
	}
}

func cmdCleanWorkspace(state *CrafterState) *CommandResult {
	dir := filepath.Join(state.ProjectDir, "output", filepath.Base(state.ProjectDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return &CommandResult{Command: "clean-workspace", Output: "Workspace is empty or doesn't exist.", Success: true}
	}

	var removed []string
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		os.RemoveAll(path)
		removed = append(removed, e.Name())
	}

	return &CommandResult{
		Command: "clean-workspace",
		Output:  fmt.Sprintf("Cleaned workspace: removed %d item(s)", len(removed)),
		Files:   removed,
		Success: true,
	}
}

func cmdDeleteWorkplan(state *CrafterState, wpID string) *CommandResult {
	if wpID == "" {
		return &CommandResult{Command: "delete-workplan", Output: "Missing wp_id argument.", Success: false}
	}

	state.Refresh()
	for _, wp := range state.WorkPlans {
		if wp.ID == wpID {
			os.Remove(wp.FilePath)
			state.UpdateAlarmCounters()
			return &CommandResult{
				Command: "delete-workplan",
				Output:  fmt.Sprintf("Deleted work plan %s (%s)", wpID, wp.Title),
				Files:   []string{wp.FilePath},
				Success: true,
			}
		}
	}

	return &CommandResult{Command: "delete-workplan", Output: fmt.Sprintf("Work plan %s not found.", wpID), Success: false}
}

func cmdListWorkspace(state *CrafterState) *CommandResult {
	dir := filepath.Join(state.ProjectDir, "output", filepath.Base(state.ProjectDir))
	var files []string

	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		files = append(files, rel)
		return nil
	})

	if len(files) == 0 {
		return &CommandResult{Command: "list-workspace", Output: "Workspace is empty.", Success: true}
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("**Workspace files (%d):**\n", len(files)))
	for _, f := range files {
		b.WriteString(fmt.Sprintf("- `%s`\n", f))
	}

	return &CommandResult{Command: "list-workspace", Output: b.String(), Files: files, Success: true}
}

func cmdResetSession(state *CrafterState) *CommandResult {
	cmdCleanWorkplans(state)
	cmdCleanWorkspace(state)

	alarmPath := filepath.Join(state.ProjectDir, ".gem-squared", "alarm.md")
	os.WriteFile(alarmPath, []byte("# Alarm\n\n## Counters\nPENDING:0 | IN_PROGRESS:0 | COMPLETED:0 | DECOMPOSED:0 | ABORTED:0\n"), 0644)

	return &CommandResult{
		Command: "reset-session",
		Output:  "Session reset: work plans cleared, workspace cleaned, counters reset.",
		Success: true,
	}
}

func cmdSwitchProject(state *CrafterState, query string) *CommandResult {
	if query == "" {
		return &CommandResult{Command: "switch-project", Output: "Please specify which project to switch to.", Success: false}
	}

	wsDir := filepath.Join(baseDir, ".gem-squared", "workspace")
	entries, err := os.ReadDir(wsDir)
	if err != nil {
		return &CommandResult{Command: "switch-project", Output: "No projects found in workspace.", Success: false}
	}

	queryLower := strings.ToLower(query)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		slug := e.Name()
		if strings.Contains(strings.ToLower(slug), queryLower) {
			return &CommandResult{
				Command: "switch-project",
				Output:  fmt.Sprintf("Found project **%s**. Switching active project.", slug),
				Files:   []string{slug},
				Success: true,
			}
		}
	}

	return &CommandResult{
		Command: "switch-project",
		Output:  fmt.Sprintf("No project matching '%s' found. Available projects: %s", query, listProjectSlugs(wsDir)),
		Success: false,
	}
}

func cmdCheckAllSessions(state *CrafterState) *CommandResult {
	wsDir := filepath.Join(baseDir, ".gem-squared", "workspace")
	entries, err := os.ReadDir(wsDir)

	var b strings.Builder
	b.WriteString("## All Projects\n\n")

	if err != nil || len(entries) == 0 {
		b.WriteString("No projects in workspace.\n")
	} else {
		b.WriteString("| Project | WPs | Status | Units Done |\n")
		b.WriteString("|---------|-----|--------|------------|\n")
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			slug := e.Name()
			projDir := filepath.Join(wsDir, slug)
			projState := NewCrafterState(projDir)
			projState.Refresh()
			totalUnits := 0
			doneUnits := 0
			status := "empty"
			for _, wp := range projState.WorkPlans {
				totalUnits += wp.UnitCount
				doneUnits += wp.Completed
				if wp.Status == "IN_PROGRESS" {
					status = "in progress"
				} else if wp.Status == "COMPLETED" && status != "in progress" {
					status = "completed"
				} else if status == "empty" {
					status = wp.Status
				}
			}
			b.WriteString(fmt.Sprintf("| **%s** | %d | %s | %d/%d |\n",
				slug, len(projState.WorkPlans), status, doneUnits, totalUnits))
		}
	}

	// Also list sessions
	sessionsMu.RLock()
	b.WriteString(fmt.Sprintf("\n## Active Sessions: %d\n\n", len(sessions)))
	for _, s := range sessions {
		proj := s.ActiveProject
		if proj == "" {
			proj = "(no project)"
		}
		b.WriteString(fmt.Sprintf("- **%s** (%s) — project: %s\n", s.Name, s.ID, proj))
	}
	sessionsMu.RUnlock()

	return &CommandResult{
		Command: "check-all-sessions",
		Output:  b.String(),
		Success: true,
	}
}

func cmdDeployWorkByState(state *CrafterState, args map[string]any) *CommandResult {
	slug, _ := args["slug"].(string)
	if slug == "" {
		slug = filepath.Base(state.ProjectDir)
	}

	projectSlug := filepath.Base(state.ProjectDir)
	workspaceDir := filepath.Join(state.ProjectDir, "output", projectSlug)
	if info, err := os.Stat(workspaceDir); err != nil || !info.IsDir() {
		return &CommandResult{Command: "deploy-work", Output: "No output files to deploy.", Success: false}
	}

	var files []string
	filepath.Walk(workspaceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(workspaceDir, path)
		files = append(files, rel)
		return nil
	})

	if len(files) == 0 {
		return &CommandResult{Command: "deploy-work", Output: "No files in workspace to deploy.", Success: false}
	}

	url := fmt.Sprintf("%s/p/%s/", strings.TrimRight(ceProductionHost, "/"), slug)

	return &CommandResult{
		Command: "deploy-work",
		Output:  fmt.Sprintf("## Deployed!\n\n**[▶ Open Deployed Site](%s)**\n\n**Files:** %d\n\nYour project is now live.", url, len(files)),
		Files:   files,
		Success: true,
	}
}

func cmdDeployWork(sess *SessionData) *CommandResult {
	slug := sess.ActiveProject
	if slug == "" {
		slug = filepath.Base(sess.State.ProjectDir)
	}
	return cmdDeployWorkByState(sess.State, map[string]any{"slug": slug})
}

func listProjectSlugs(wsDir string) string {
	entries, err := os.ReadDir(wsDir)
	if err != nil {
		return "(none)"
	}
	var slugs []string
	for _, e := range entries {
		if e.IsDir() {
			slugs = append(slugs, e.Name())
		}
	}
	if len(slugs) == 0 {
		return "(none)"
	}
	return strings.Join(slugs, ", ")
}
