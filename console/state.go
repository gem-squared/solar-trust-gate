package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type WorkPlanSummary struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	State      string `json:"state"`
	UnitCount  int    `json:"unit_count"`
	Completed  int    `json:"completed"`
	InProgress int    `json:"in_progress"`
	Pending    int    `json:"pending"`
	FilePath   string `json:"file_path"`
}

type AlarmState struct {
	Pending    int `json:"pending"`
	InProgress int `json:"in_progress"`
	Completed  int `json:"completed"`
	Aborted    int `json:"aborted"`
}

type CrafterState struct {
	ProjectDir string            `json:"project_dir"`
	Alarm      AlarmState        `json:"alarm"`
	WorkPlans  []WorkPlanSummary `json:"work_plans"`
}

func NewCrafterState(projectDir string) *CrafterState {
	return &CrafterState{ProjectDir: projectDir}
}

func (cs *CrafterState) Refresh() {
	cs.readAlarm()
	cs.readWorkPlans()
}

func (cs *CrafterState) readAlarm() {
	path := filepath.Join(cs.ProjectDir, "alarm.md")
	if _, err := os.Stat(path); err != nil {
		path = filepath.Join(cs.ProjectDir, ".gem-squared", "alarm.md")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	content := string(data)
	cs.Alarm = AlarmState{}
	re := regexp.MustCompile(`PENDING:\s*(\d+)`)
	if m := re.FindStringSubmatch(content); len(m) > 1 {
		fmt.Sscanf(m[1], "%d", &cs.Alarm.Pending)
	}
	re = regexp.MustCompile(`IN_PROGRESS:\s*(\d+)`)
	if m := re.FindStringSubmatch(content); len(m) > 1 {
		fmt.Sscanf(m[1], "%d", &cs.Alarm.InProgress)
	}
	re = regexp.MustCompile(`COMPLETED:\s*(\d+)`)
	if m := re.FindStringSubmatch(content); len(m) > 1 {
		fmt.Sscanf(m[1], "%d", &cs.Alarm.Completed)
	}
	re = regexp.MustCompile(`ABORTED:\s*(\d+)`)
	if m := re.FindStringSubmatch(content); len(m) > 1 {
		fmt.Sscanf(m[1], "%d", &cs.Alarm.Aborted)
	}
}

func (cs *CrafterState) readWorkPlans() {
	dir := cs.WorkPlanDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	cs.WorkPlans = nil
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		wp := parseWorkPlanSummary(string(data), path)
		cs.WorkPlans = append(cs.WorkPlans, wp)
	}
}

func parseWorkPlanSummary(content, path string) WorkPlanSummary {
	wp := WorkPlanSummary{FilePath: path}

	lines := strings.Split(content, "\n")
	if len(lines) > 0 {
		title := strings.TrimPrefix(lines[0], "# ")
		if idx := strings.Index(title, ":"); idx > 0 {
			wp.ID = strings.TrimSpace(title[:idx])
			wp.Title = strings.TrimSpace(title[idx+1:])
		} else {
			wp.Title = title
		}
	}

	re := regexp.MustCompile(`\*\*STATUS:\*\*\s*(\w+)`)
	if m := re.FindStringSubmatch(content); len(m) > 1 {
		wp.Status = m[1]
	}
	re = regexp.MustCompile(`\*\*STATE:\*\*\s*(\S+)`)
	if m := re.FindStringSubmatch(content); len(m) > 1 {
		wp.State = m[1]
	}

	unitRe := regexp.MustCompile(`### \d+\..+\|\s*STATUS:\s*(\w+)`)
	matches := unitRe.FindAllStringSubmatch(content, -1)
	wp.UnitCount = len(matches)
	for _, m := range matches {
		switch m[1] {
		case "COMPLETED":
			wp.Completed++
		case "IN_PROGRESS":
			wp.InProgress++
		case "PENDING":
			wp.Pending++
		}
	}

	return wp
}

func (cs *CrafterState) Summary() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Project: %s\n", filepath.Base(cs.ProjectDir)))
	b.WriteString(fmt.Sprintf("Alarm: PENDING:%d | IN_PROGRESS:%d | COMPLETED:%d | ABORTED:%d\n",
		cs.Alarm.Pending, cs.Alarm.InProgress, cs.Alarm.Completed, cs.Alarm.Aborted))

	if len(cs.WorkPlans) == 0 {
		b.WriteString("Work Plans: none\n")
	} else {
		b.WriteString(fmt.Sprintf("Work Plans: %d\n", len(cs.WorkPlans)))
		for _, wp := range cs.WorkPlans {
			b.WriteString(fmt.Sprintf("  - %s: %s [STATUS: %s] (%d/%d units done)\n",
				wp.ID, wp.Title, wp.Status, wp.Completed, wp.UnitCount))
		}
	}
	return b.String()
}

func (cs *CrafterState) ToJSON() string {
	data, _ := json.MarshalIndent(cs, "", "  ")
	return string(data)
}

func (cs *CrafterState) WorkPlanDir() string {
	// Session dirs have work-plan/ directly; project root uses .gem-squared/work-plan/
	direct := filepath.Join(cs.ProjectDir, "work-plan")
	if info, err := os.Stat(direct); err == nil && info.IsDir() {
		return direct
	}
	return filepath.Join(cs.ProjectDir, ".gem-squared", "work-plan")
}

func (cs *CrafterState) WorkspacePath() string {
	return filepath.Join(cs.ProjectDir, "output")
}

func (cs *CrafterState) NextWPNumber() int {
	max := 0
	re := regexp.MustCompile(`WP-\w+-(\d+)`)
	for _, wp := range cs.WorkPlans {
		if m := re.FindStringSubmatch(wp.ID); len(m) > 1 {
			var n int
			fmt.Sscanf(m[1], "%d", &n)
			if n > max {
				max = n
			}
		}
	}
	return max + 1
}

func (cs *CrafterState) ReadWorkPlan(id string) (string, error) {
	for _, wp := range cs.WorkPlans {
		if wp.ID == id {
			data, err := os.ReadFile(wp.FilePath)
			if err != nil {
				return "", err
			}
			return string(data), nil
		}
	}
	return "", fmt.Errorf("work plan %s not found", id)
}

func (cs *CrafterState) WriteFile(relPath, content string) error {
	absPath := filepath.Join(cs.ProjectDir, relPath)
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(absPath, []byte(content), 0644)
}

func (cs *CrafterState) UpdateAlarmCounters() error {
	cs.Refresh()
	pending, inProgress, completed, aborted := 0, 0, 0, 0
	for _, wp := range cs.WorkPlans {
		pending += wp.Pending
		inProgress += wp.InProgress
		completed += wp.Completed
	}
	_ = aborted

	content := fmt.Sprintf(`# Alarm — ai-agent-olympics

**Last checked:** %s

## Counters
PENDING:%d | IN_PROGRESS:%d | COMPLETED:%d | DECOMPOSED:0 | ABORTED:%d

## Active Tasks
`, time.Now().Format(time.RFC3339), pending, inProgress, completed, aborted)

	for _, wp := range cs.WorkPlans {
		if wp.Status == "IN_PROGRESS" || wp.Status == "PENDING" {
			content += fmt.Sprintf("- %s: %s [%s] (%d/%d)\n", wp.ID, wp.Title, wp.Status, wp.Completed, wp.UnitCount)
		}
	}

	alarmPath := filepath.Join(cs.ProjectDir, "alarm.md")
	if _, err := os.Stat(alarmPath); err != nil {
		alarmPath = filepath.Join(cs.ProjectDir, ".gem-squared", "alarm.md")
	}
	dir := filepath.Dir(alarmPath)
	os.MkdirAll(dir, 0755)
	return os.WriteFile(alarmPath, []byte(content), 0644)
}
