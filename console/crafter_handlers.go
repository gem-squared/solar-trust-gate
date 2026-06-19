package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	sessionsMu sync.RWMutex
	sessions   = make(map[string]*SessionData)
	baseDir    string
)

type SessionData struct {
	ID            string             `json:"id"`
	Name          string             `json:"name"`
	CreatedAt     string             `json:"created_at"`
	ActiveProject string             `json:"active_project,omitempty"`
	// Model is the CRAFTER LLM (orchestrator + ALL skill execution).
	// AgentModel is the AGENT LLM (CE Executor runtime ONLY — never skills).
	// See memory: project_crafter_vs_agent_llm.md for the strict role split.
	Model         string             `json:"-"`
	AgentModel    string             `json:"-"`
	State         *CrafterState      `json:"-"`
	History       []Turn             `json:"-"`
	ExecLog       []ExecutionLogEntry `json:"-"`
	mu            sync.Mutex
	cancelCtx     context.Context
	cancelFunc    context.CancelFunc
}

func (sess *SessionData) StartProcessing() context.Context {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.cancelFunc != nil {
		sess.cancelFunc()
	}
	sess.cancelCtx, sess.cancelFunc = context.WithCancel(context.Background())
	return sess.cancelCtx
}

func (sess *SessionData) StopProcessing() {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.cancelFunc != nil {
		sess.cancelFunc()
		sess.cancelFunc = nil
		sess.cancelCtx = nil
	}
}

func (sess *SessionData) Cancel() bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.cancelFunc != nil {
		sess.cancelFunc()
		log.Printf("[CANCEL] session %s cancelled", sess.ID)
		return true
	}
	return false
}

func (sess *SessionData) IsCancelled() bool {
	sess.mu.Lock()
	ctx := sess.cancelCtx
	sess.mu.Unlock()
	if ctx == nil {
		return false
	}
	return ctx.Err() != nil
}

type ExecutionLogEntry struct {
	Timestamp string         `json:"timestamp"`
	Skill     string         `json:"skill"`
	Args      map[string]any `json:"args,omitempty"`
	Duration  int64          `json:"duration_ms"`
	Success   bool           `json:"success"`
	Summary   string         `json:"summary"`
}

type ChatRequest struct {
	Message    string `json:"message"`
	SessionID  string `json:"session_id,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
	AgentModel string `json:"agent_model,omitempty"`
}

type ChatResponse struct {
	Response    string   `json:"response"`
	SkillUsed   string   `json:"skill_used,omitempty"`
	StateChange string   `json:"state_change,omitempty"`
	Suggestion  string   `json:"suggestion,omitempty"`
	Duration    int64    `json:"duration_ms"`
	Files       []string `json:"files_modified,omitempty"`
	SessionID   string   `json:"session_id"`
}

func InitCrafter(projectDir string) {
	baseDir = projectDir
	for _, d := range []string{"sessions", "work-plan", "archive", "workspace"} {
		os.MkdirAll(filepath.Join(baseDir, ".gem-squared", d), 0755)
	}
	loadExistingSessions()
	if err := InitSkills(); err != nil {
		fmt.Printf("Warning: skill loading: %v\n", err)
	}
}

func loadExistingSessions() {
	sessDir := filepath.Join(baseDir, ".gem-squared", "sessions")
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sid := e.Name()
		sessPath := filepath.Join(sessDir, sid)
		name := sid
		activeProject := ""
		if data, err := os.ReadFile(filepath.Join(sessPath, "session.json")); err == nil {
			var meta map[string]string
			if json.Unmarshal(data, &meta) == nil {
				if n, ok := meta["name"]; ok {
					name = n
				}
				if ap, ok := meta["active_project"]; ok {
					activeProject = ap
				}
			}
		}
		info, _ := e.Info()
		created := time.Now().Format(time.RFC3339)
		if info != nil {
			created = info.ModTime().Format(time.RFC3339)
		}
		stateDir := sessPath
		if activeProject != "" {
			projectDir := filepath.Join(baseDir, ".gem-squared", "workspace", activeProject)
			if pi, err := os.Stat(projectDir); err == nil && pi.IsDir() {
				stateDir = projectDir
			}
		}
		// WP-AO-34: retroactively rename sessions with a default "Session XXXX"
		// name when an active_project is bound. Surfaces meaningful names for
		// any session loaded on startup that pre-dated the auto-name fix.
		if activeProject != "" && strings.HasPrefix(name, "Session ") {
			name = activeProject
			// persist the rename so it sticks across restarts
			meta, _ := json.Marshal(map[string]string{
				"name": name, "id": sid, "active_project": activeProject,
			})
			os.WriteFile(filepath.Join(sessPath, "session.json"), meta, 0644)
		}
		s := &SessionData{
			ID:            sid,
			Name:          name,
			CreatedAt:     created,
			ActiveProject: activeProject,
			State:         NewCrafterState(stateDir),
		}
		s.loadChatHistory()
		sessions[sid] = s
	}
	fmt.Printf("Loaded %d existing sessions\n", len(sessions))
}

func genSessionID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func getOrCreateSession(sid string) *SessionData {
	sessionsMu.RLock()
	s, ok := sessions[sid]
	sessionsMu.RUnlock()
	if ok {
		return s
	}
	return createSession(sid, "")
}

func createSession(sid, name string) *SessionData {
	if sid == "" {
		sid = genSessionID()
	}
	if name == "" {
		name = "Session " + sid[:4]
	}
	sessPath := filepath.Join(baseDir, ".gem-squared", "sessions", sid)
	os.MkdirAll(filepath.Join(sessPath, "work-plan"), 0755)
	os.MkdirAll(filepath.Join(sessPath, "output"), 0755)

	meta, _ := json.Marshal(map[string]string{"name": name, "id": sid})
	os.WriteFile(filepath.Join(sessPath, "session.json"), meta, 0644)

	s := &SessionData{
		ID:        sid,
		Name:      name,
		CreatedAt: time.Now().Format(time.RFC3339),
		State:     NewCrafterState(sessPath),
	}

	sessionsMu.Lock()
	sessions[sid] = s
	sessionsMu.Unlock()
	return s
}

func handleCrafterChat(w http.ResponseWriter, r *http.Request) {
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	if req.Message == "" {
		http.Error(w, `{"error":"empty message"}`, http.StatusBadRequest)
		return
	}

	log.Printf("[CHAT] session=%s msg=%q", req.SessionID, truncate(req.Message, 80))
	sess := getOrCreateSession(req.SessionID)
	sess.mu.Lock()
	sess.History = append(sess.History, Turn{Role: "user", Content: req.Message})
	history := make([]Turn, len(sess.History))
	copy(history, sess.History)
	sess.mu.Unlock()

	sess.State.Refresh()
	catalog := BuildCatalog(globalSkills)

	orchResult, err := Orchestrate(req.Message, catalog, sess.State, history, req.Model)
	if err != nil {
		jsonError(w, fmt.Sprintf("orchestration failed: %v", err), http.StatusInternalServerError)
		return
	}

	resp := ChatResponse{SessionID: sess.ID}

	if orchResult.SelectedSkill != "" && orchResult.SelectedSkill != "none" {
		skill := GetSkill(orchResult.SelectedSkill)
		if skill == nil {
			resp.Response = fmt.Sprintf("Skill '/%s' not found. %s", orchResult.SelectedSkill, orchResult.Reasoning)
			resp.SkillUsed = orchResult.SelectedSkill
		} else {
			execResult, err := ExecuteSkill(skill, orchResult.Args, sess.State, sess)
			if err != nil {
				resp.Response = fmt.Sprintf("Skill /%s failed: %v", skill.Name, err)
				resp.SkillUsed = skill.Name
				sessLogExec(sess, skill.Name, orchResult.Args, 0, false, err.Error())
			} else {
				resp.Response = execResult.OutputB
				resp.SkillUsed = execResult.Skill
				resp.StateChange = execResult.StateChange
				resp.Duration = execResult.Duration
				resp.Files = execResult.FilesModified
				sessLogExec(sess, skill.Name, orchResult.Args, execResult.Duration, true, truncate(execResult.OutputB, 100))
			}
		}
	} else {
		resp.Response = orchResult.DirectReply
		if resp.Response == "" {
			resp.Response = orchResult.Reasoning
		}
	}

	resp.Suggestion = suggestNext(sess.State, orchResult)

	sess.mu.Lock()
	sess.History = append(sess.History, Turn{Role: "assistant", Content: resp.Response})
	sess.saveChatHistory()
	sess.mu.Unlock()

	// Auto-name session from first plan
	if resp.SkillUsed == "plan-work" && strings.HasPrefix(sess.Name, "Session ") {
		if work, ok := orchResult.Args["work"].(string); ok && work != "" {
			name := work
			if len(name) > 40 {
				name = name[:40] + "..."
			}
			sess.Name = name
			meta, _ := json.Marshal(map[string]string{"name": name, "id": sess.ID})
			os.WriteFile(filepath.Join(baseDir, ".gem-squared", "sessions", sess.ID, "session.json"), meta, 0644)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func handleCrafterChatStream(w http.ResponseWriter, r *http.Request) {
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("[CHAT-STREAM] bad request: %v", err)
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	if req.Message == "" {
		http.Error(w, `{"error":"empty message"}`, http.StatusBadRequest)
		return
	}

	log.Printf("[CHAT-STREAM] session=%s msg=%q model=%s agent=%s", req.SessionID, truncate(req.Message, 80), req.Model, req.AgentModel)
	chatStart := time.Now()
	sess := getOrCreateSession(req.SessionID)
	if req.Model != "" {
		sess.Model = req.Model
	}
	if req.AgentModel != "" {
		sess.AgentModel = req.AgentModel
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ctx := sess.StartProcessing()
	defer sess.StopProcessing()

	cancelled := false
	sendSSE := func(event string, data any) {
		if ctx.Err() != nil {
			if !cancelled {
				cancelled = true
			}
			switch event {
			case "chain_cancelled", "unit_cancelled", "done", "response":
			default:
				return
			}
		}
		jsonData, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, jsonData)
		flusher.Flush()
	}

	sess.mu.Lock()
	sess.History = append(sess.History, Turn{Role: "user", Content: req.Message})
	sess.mu.Unlock()

	sendSSE("thinking", map[string]string{"status": "Analyzing your message...", "session_id": sess.ID})

	sess.State.Refresh()

	if sess.ActiveProject == "" {
		autoBindProject(sess)
	}

	catalog := BuildCatalog(globalSkills)

	sess.mu.Lock()
	history := make([]Turn, len(sess.History))
	copy(history, sess.History)
	sess.mu.Unlock()

	// ALL messages go through LLM orchestrator — single front-door
	chunkCount := 0
	onChunk := func(chunk string) {
		chunkCount++
		if chunkCount%3 == 0 {
			sendSSE("thinking", map[string]string{"status": "Thinking...", "chunks": fmt.Sprintf("%d", chunkCount)})
		}
	}
	orchResult, err := OrchestrateWithCallback(req.Message, catalog, sess.State, history, req.Model, onChunk)
	if err != nil {
		log.Printf("[CHAT-STREAM] orchestration error after %v: %v", time.Since(chatStart), err)
		sendSSE("error", map[string]string{"error": err.Error()})
		return
	}
	log.Printf("[CHAT-STREAM] orchestrated in %v → skill=%q cmd=%q auto_loop=%v chain=%d", time.Since(chatStart), orchResult.SelectedSkill, orchResult.Command, orchResult.AutoLoop, len(orchResult.TaskChain))

	// Task chain — LLM planned multiple steps, execute mechanically
	if len(orchResult.TaskChain) > 0 {
		log.Printf("[CHAT-STREAM] task_chain detected: %d steps", len(orchResult.TaskChain))
		executePlannedChain(orchResult.TaskChain, sess, req.Model, sendSSE)
		return
	}

	// Handle greeting command
	if orchResult.Command == "__greeting__" {
		reply := orchResult.DirectReply
		if reply == "" {
			reply = "Hey! I'm GEM² AI Crafter — your skillful engineering partner. What can I build for you today?"
		}
		sendSSE("response", map[string]any{
			"content":    reply,
			"session_id": sess.ID,
		})
		sess.mu.Lock()
		sess.History = append(sess.History, Turn{Role: "assistant", Content: reply})
		sess.saveChatHistory()
		sess.mu.Unlock()
		sendSSE("done", map[string]string{"status": "complete"})
		return
	}

	// Handle system commands
	if orchResult.Command != "" {
		sendSSE("executing", map[string]string{
			"skill":  orchResult.Command,
			"status": fmt.Sprintf("Running command: %s...", orchResult.Command),
		})
		cmdResult := ExecuteCommand(orchResult.Command, orchResult.Args, sess.State)
		log.Printf("[CHAT-STREAM] command %q → success=%v", orchResult.Command, cmdResult.Success)

		// For switch-project, actually bind the session
		if orchResult.Command == "switch-project" && cmdResult.Success && len(cmdResult.Files) > 0 {
			slug := cmdResult.Files[0]
			sess.SetActiveProject(slug)
			sess.State.Refresh()
			cmdResult.Output += fmt.Sprintf("\n\nActive project is now **%s**.", slug)
		}

		sendSSE("state_updated", map[string]string{"change": orchResult.Command})
		sendSSE("response", map[string]any{
			"content":        cmdResult.Output,
			"skill":          orchResult.Command,
			"files_modified": cmdResult.Files,
			"session_id":     sess.ID,
		})
		sess.mu.Lock()
		sess.History = append(sess.History, Turn{Role: "assistant", Content: cmdResult.Output})
		sess.saveChatHistory()
		sess.mu.Unlock()
		sendSSE("done", map[string]string{"status": "complete"})
		return
	}

	if orchResult.SelectedSkill != "" && orchResult.SelectedSkill != "none" {
		// Auto-loop for proceed-work: default true unless LLM set auto_loop=false
		if orchResult.SelectedSkill == "proceed-work" && orchResult.AutoLoop {
			deployAfter := false
			if v, ok := orchResult.Args["deploy_after"].(bool); ok && v {
				deployAfter = true
			}
			sendSSE("skill_selected", map[string]string{
				"skill":     "auto-proceed",
				"reasoning": "Auto-proceed loop: executing all PENDING units with inline verification",
			})
			result, err := autoProceedLoop(sess, sendSSE)
			if err != nil {
				sendSSE("error", map[string]string{"error": err.Error()})
				sendSSE("done", map[string]string{"status": "complete"})
				return
			}
			sendSSE("response", map[string]any{
				"content":        result.OutputB,
				"skill":          result.Skill,
				"state_change":   result.StateChange,
				"duration_ms":    result.Duration,
				"files_modified": result.FilesModified,
				"session_id":     sess.ID,
			})
			sess.appendAndSave("assistant", result.OutputB)
			// Auto-deploy after loop if requested
			if deployAfter && sess.ActiveProject != "" {
				deployResult := cmdDeployWork(sess)
				sendSSE("response", map[string]any{
					"content":    deployResult.Output,
					"skill":     "deploy-work",
					"session_id": sess.ID,
				})
				sess.appendAndSave("assistant", deployResult.Output)
			}
			sendSSE("done", map[string]string{"status": "complete"})
			return
		}

		sendSSE("skill_selected", map[string]string{
			"skill":     orchResult.SelectedSkill,
			"reasoning": orchResult.Reasoning,
		})

		skill := GetSkill(orchResult.SelectedSkill)
		if skill == nil {
			sendSSE("error", map[string]string{"error": fmt.Sprintf("skill '%s' not found", orchResult.SelectedSkill)})
			return
		}

		sendSSE("executing", map[string]string{
			"skill":  skill.Name,
			"status": fmt.Sprintf("Executing /%s...", skill.Name),
		})

		execStart := time.Now()
		execResult, err := ExecuteSkill(skill, orchResult.Args, sess.State, sess)
		if err != nil {
			log.Printf("[CHAT-STREAM] skill /%s failed after %v: %v", skill.Name, time.Since(execStart), err)
			sendSSE("error", map[string]string{"error": err.Error()})
			sessLogExec(sess, skill.Name, orchResult.Args, 0, false, err.Error())
			return
		}
		log.Printf("[CHAT-STREAM] skill /%s completed in %v (%d chars output)", skill.Name, time.Since(execStart), len(execResult.OutputB))

		if execResult.StateChange != "" {
			sendSSE("state_updated", map[string]string{"change": execResult.StateChange})
		}

		sendSSE("response", map[string]any{
			"content":        execResult.OutputB,
			"skill":          execResult.Skill,
			"state_change":   execResult.StateChange,
			"duration_ms":    execResult.Duration,
			"files_modified": execResult.FilesModified,
			"suggestion":     suggestNext(sess.State, orchResult),
			"session_id":     sess.ID,
		})
		sess.appendAndSave("assistant", execResult.OutputB)
		sessLogExec(sess, skill.Name, orchResult.Args, execResult.Duration, true, truncate(execResult.OutputB, 100))

		// Auto-name
		if skill.Name == "plan-work" && strings.HasPrefix(sess.Name, "Session ") {
			if work, ok := orchResult.Args["work"].(string); ok && work != "" {
				name := work
				if len(name) > 40 {
					name = name[:40] + "..."
				}
				sess.Name = name
				meta, _ := json.Marshal(map[string]string{"name": name, "id": sess.ID})
				os.WriteFile(filepath.Join(baseDir, ".gem-squared", "sessions", sess.ID, "session.json"), meta, 0644)
			}
		}
	} else {
		reply := orchResult.DirectReply
		if reply == "" {
			reply = orchResult.Reasoning
		}
		sendSSE("response", map[string]any{
			"content":    reply,
			"suggestion": suggestNext(sess.State, orchResult),
			"session_id": sess.ID,
		})
		sess.appendAndSave("assistant", reply)
	}

	sendSSE("done", map[string]string{"status": "complete"})
}


func (sess *SessionData) SetActiveProject(slug string) {
	sess.ActiveProject = slug
	projectDir := filepath.Join(baseDir, ".gem-squared", "workspace", slug)
	sess.State = NewCrafterState(projectDir)
	// WP-AO-34: auto-name sessions from project slug when they haven't been
	// renamed yet. Previously only /plan-work could rename a session, so CE
	// flows (create-project → create-ce, no plan-work) stayed on `Session
	// XXXX`. The plan-work auto-rename at handleCrafterChatStream still
	// runs as a secondary override for planning-heavy sessions.
	if strings.HasPrefix(sess.Name, "Session ") {
		sess.Name = slug
	}
	sess.saveSessionMeta()
	log.Printf("[SESSION] %s bound to project %q → %s", sess.ID, slug, projectDir)
}

func (sess *SessionData) saveSessionMeta() {
	meta := map[string]string{"name": sess.Name, "id": sess.ID}
	if sess.ActiveProject != "" {
		meta["active_project"] = sess.ActiveProject
	}
	data, _ := json.Marshal(meta)
	sessPath := filepath.Join(baseDir, ".gem-squared", "sessions", sess.ID)
	os.WriteFile(filepath.Join(sessPath, "session.json"), data, 0644)
}

func (sess *SessionData) saveChatHistory() {
	sessPath := filepath.Join(baseDir, ".gem-squared", "sessions", sess.ID)
	data, _ := json.Marshal(sess.History)
	os.WriteFile(filepath.Join(sessPath, "chat.json"), data, 0644)
}

// appendAndSave is the single source of truth for persisting an assistant
// turn. Locks the session mutex, appends to sess.History, and writes the
// chat.json file atomically (well, the JSON marshal + WriteFile pattern).
// Call this from every SSE response site in chat handlers so reload restores
// the same chat view the user had before refresh. See WP-AO-34.
func (sess *SessionData) appendAndSave(role, content string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.History = append(sess.History, Turn{Role: role, Content: content})
	sessPath := filepath.Join(baseDir, ".gem-squared", "sessions", sess.ID)
	data, _ := json.Marshal(sess.History)
	os.WriteFile(filepath.Join(sessPath, "chat.json"), data, 0644)
}

func (sess *SessionData) loadChatHistory() {
	sessPath := filepath.Join(baseDir, ".gem-squared", "sessions", sess.ID)
	data, err := os.ReadFile(filepath.Join(sessPath, "chat.json"))
	if err != nil {
		return
	}
	json.Unmarshal(data, &sess.History)
}

func handleCrafterState(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("session_id")
	if sid == "" {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"alarm":{},"work_plans":[]}`)
		return
	}
	sessionsMu.RLock()
	sess, ok := sessions[sid]
	sessionsMu.RUnlock()
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"alarm":{},"work_plans":[]}`)
		return
	}
	sess.State.Refresh()
	stateMap := map[string]any{
		"project_dir":    sess.State.ProjectDir,
		"alarm":          sess.State.Alarm,
		"work_plans":     sess.State.WorkPlans,
		"active_project": sess.ActiveProject,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stateMap)
}

func handleCrafterSkills(w http.ResponseWriter, r *http.Request) {
	type skillInfo struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		ArgHint     string `json:"argument_hint,omitempty"`
	}
	var skills []skillInfo
	for _, s := range globalSkills {
		skills = append(skills, skillInfo{
			Name:        s.Name,
			Description: s.Description,
			ArgHint:     s.ArgumentHint,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(skills)
}

func handleChatHistory(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("session_id")
	w.Header().Set("Content-Type", "application/json")
	if sid == "" {
		fmt.Fprint(w, "[]")
		return
	}
	sessionsMu.RLock()
	sess, ok := sessions[sid]
	sessionsMu.RUnlock()
	if !ok {
		fmt.Fprint(w, "[]")
		return
	}
	sess.mu.Lock()
	hist := make([]Turn, len(sess.History))
	copy(hist, sess.History)
	sess.mu.Unlock()
	json.NewEncoder(w).Encode(hist)
}

func handleCrafterHistory(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("session_id")
	if sid == "" {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "[]")
		return
	}
	sessionsMu.RLock()
	sess, ok := sessions[sid]
	sessionsMu.RUnlock()
	if !ok {
		fmt.Fprint(w, "[]")
		return
	}
	sess.mu.Lock()
	log := make([]ExecutionLogEntry, len(sess.ExecLog))
	copy(log, sess.ExecLog)
	sess.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if len(log) == 0 {
		fmt.Fprint(w, "[]")
		return
	}
	n := len(log)
	start := 0
	if n > 20 {
		start = n - 20
	}
	json.NewEncoder(w).Encode(log[start:])
}

func handleCrafterWorkplan(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sid := r.URL.Query().Get("session_id")
	if id == "" || sid == "" {
		http.Error(w, `{"error":"missing id or session_id"}`, http.StatusBadRequest)
		return
	}
	sessionsMu.RLock()
	sess, ok := sessions[sid]
	sessionsMu.RUnlock()
	if !ok {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}
	sess.State.Refresh()
	content, err := sess.State.ReadWorkPlan(id)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": id, "content": content})
}

// Session management endpoints

func handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessionsMu.RLock()
	defer sessionsMu.RUnlock()
	type sessionInfo struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		CreatedAt string `json:"created_at"`
		WPCount   int    `json:"wp_count"`
	}
	var list []sessionInfo
	for _, s := range sessions {
		s.State.Refresh()
		list = append(list, sessionInfo{
			ID:        s.ID,
			Name:      s.Name,
			CreatedAt: s.CreatedAt,
			WPCount:   len(s.State.WorkPlans),
		})
	}
	if list == nil {
		list = []sessionInfo{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

func handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	sess := createSession("", req.Name)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"id":         sess.ID,
		"name":       sess.Name,
		"created_at": sess.CreatedAt,
	})
}

func handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("id")
	sessionsMu.Lock()
	delete(sessions, sid)
	sessionsMu.Unlock()
	sessPath := filepath.Join(baseDir, ".gem-squared", "sessions", sid)
	os.RemoveAll(sessPath)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"ok":true}`)
}

func handleCancelSession(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("id")
	sessionsMu.RLock()
	sess, ok := sessions[sid]
	sessionsMu.RUnlock()
	if !ok {
		jsonError(w, "session not found", http.StatusNotFound)
		return
	}
	cancelled := sess.Cancel()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"cancelled": cancelled})
}

func autoBindProject(sess *SessionData) {
	wsDir := filepath.Join(baseDir, ".gem-squared", "workspace")
	entries, err := os.ReadDir(wsDir)
	if err != nil {
		return
	}

	var projects []string
	var withPending []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		slug := e.Name()
		projects = append(projects, slug)
		projDir := filepath.Join(wsDir, slug)
		projState := NewCrafterState(projDir)
		projState.Refresh()
		for _, wp := range projState.WorkPlans {
			if wp.Pending > 0 || wp.InProgress > 0 {
				withPending = append(withPending, slug)
				break
			}
		}
	}

	var target string
	if len(withPending) == 1 {
		target = withPending[0]
	} else if len(withPending) == 0 && len(projects) == 1 {
		target = projects[0]
	}

	if target != "" {
		log.Printf("[AUTO-BIND] session %s → project %q (pending work detected)", sess.ID, target)
		sess.SetActiveProject(target)
		sess.State.Refresh()
	}
}

func suggestNext(state *CrafterState, orch *OrchestrateResult) string {
	state.Refresh()
	for _, wp := range state.WorkPlans {
		if wp.InProgress > 0 || (wp.Status == "IN_PROGRESS" && wp.Pending > 0) {
			return fmt.Sprintf("Continue with: \"proceed with %s\"", wp.ID)
		}
	}
	for _, wp := range state.WorkPlans {
		if wp.Status == "COMPLETED" || (wp.Completed > 0 && wp.Pending == 0) {
			return fmt.Sprintf("Verify results: \"verify %s\"", wp.ID)
		}
	}
	for _, wp := range state.WorkPlans {
		if wp.Status == "PENDING" {
			return fmt.Sprintf("Start work: \"proceed with %s\"", wp.ID)
		}
	}
	return ""
}

func sessLogExec(sess *SessionData, skill string, args map[string]any, duration int64, success bool, summary string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.ExecLog = append(sess.ExecLog, ExecutionLogEntry{
		Timestamp: time.Now().Format(time.RFC3339),
		Skill:     skill,
		Args:      args,
		Duration:  duration,
		Success:   success,
		Summary:   summary,
	})
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func handleDeployServe(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if slug == "" {
		http.NotFound(w, r)
		return
	}

	wsDir := filepath.Join(baseDir, ".gem-squared", "workspace", slug, "output", slug)
	if info, err := os.Stat(wsDir); err != nil || !info.IsDir() {
		http.NotFound(w, r)
		return
	}

	// Strip /p/{slug}/ prefix and serve the file
	prefix := fmt.Sprintf("/p/%s/", slug)
	handler := http.StripPrefix(prefix, http.FileServer(http.Dir(wsDir)))
	handler.ServeHTTP(w, r)
}

const maxUploadTotal = 20 << 20 // 20MB
const maxUploadPerFile = 2 << 20 // 2MB

type uploadedFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Path string `json:"path"`
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadTotal+1024) // small buffer for form fields

	if err := r.ParseMultipartForm(maxUploadTotal); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		json.NewEncoder(w).Encode(map[string]string{"error": "total upload exceeds 20MB"})
		return
	}
	defer r.MultipartForm.RemoveAll()

	sid := r.FormValue("session_id")
	if sid == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "session_id required"})
		return
	}

	sessionsMu.RLock()
	sess, ok := sessions[sid]
	sessionsMu.RUnlock()
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "session not found"})
		return
	}
	if sess.ActiveProject == "" {
		// Auto-create a sandbox project so judges can attach + /create-ce
		// in one shot without an explicit /create-project step.
		log.Printf("[UPLOAD] no active project for session %s — auto-creating ce-sandbox", sid)
		autoArgs := map[string]any{"project_name": "ce-sandbox", "stack": "tpmn"}
		if _, perr := executeCreateProject(autoArgs, nil, sess, time.Now()); perr != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": "auto-create project failed: " + perr.Error()})
			return
		}
	}

	uploadDir := filepath.Join(baseDir, ".gem-squared", "workspace", sess.ActiveProject, "uploaded_files")
	os.MkdirAll(uploadDir, 0755)

	files := r.MultipartForm.File["files"]
	var uploaded []uploadedFile
	var errors []string

	for _, fh := range files {
		if fh.Size > maxUploadPerFile {
			errors = append(errors, fmt.Sprintf("%s: exceeds 2MB limit (%d bytes)", fh.Filename, fh.Size))
			continue
		}

		src, err := fh.Open()
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: open error", fh.Filename))
			continue
		}

		safeName := filepath.Base(filepath.Clean(fh.Filename))
		if safeName == "." || safeName == "/" {
			src.Close()
			errors = append(errors, fmt.Sprintf("%s: invalid filename", fh.Filename))
			continue
		}

		destPath := filepath.Join(uploadDir, safeName)
		if _, err := os.Stat(destPath); err == nil {
			ext := filepath.Ext(safeName)
			base := strings.TrimSuffix(safeName, ext)
			for i := 1; ; i++ {
				candidate := fmt.Sprintf("%s_%d%s", base, i, ext)
				destPath = filepath.Join(uploadDir, candidate)
				if _, err := os.Stat(destPath); os.IsNotExist(err) {
					safeName = candidate
					break
				}
			}
		}

		dst, err := os.Create(destPath)
		if err != nil {
			src.Close()
			errors = append(errors, fmt.Sprintf("%s: write error", safeName))
			continue
		}

		written, err := io.Copy(dst, src)
		src.Close()
		dst.Close()
		if err != nil {
			os.Remove(destPath)
			errors = append(errors, fmt.Sprintf("%s: copy error", safeName))
			continue
		}

		log.Printf("[UPLOAD] %s → %s (%d bytes)", safeName, destPath, written)
		uploaded = append(uploaded, uploadedFile{
			Name: safeName,
			Size: written,
			Path: "uploaded_files/" + safeName,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"uploaded": uploaded,
		"errors":   errors,
	})
}

func RegisterCrafterRoutes(mux *http.ServeMux, heavyRL, lightRL *rateLimiter) {
	projectDir := os.Getenv("PROJECT_DIR")
	if projectDir == "" {
		projectDir, _ = os.Getwd()
	}
	InitCrafter(projectDir)

	mux.HandleFunc("POST /api/chat", heavyRL.wrap(authGuard(limitBody(handleCrafterChat))))
	mux.HandleFunc("POST /api/chat-stream", heavyRL.wrap(authGuardQuery(limitBody(handleCrafterChatStream))))
	mux.HandleFunc("GET /api/crafter/state", authGuard(handleCrafterState))
	mux.HandleFunc("GET /api/crafter/skills", authGuard(handleCrafterSkills))
	mux.HandleFunc("GET /api/crafter/history", authGuard(handleCrafterHistory))
	mux.HandleFunc("GET /api/crafter/chat-history", authGuard(handleChatHistory))
	mux.HandleFunc("GET /api/crafter/workplan/{id}", authGuard(handleCrafterWorkplan))
	mux.HandleFunc("GET /api/sessions", authGuard(handleListSessions))
	mux.HandleFunc("POST /api/sessions", authGuard(handleCreateSession))
	mux.HandleFunc("DELETE /api/sessions/{id}", authGuard(handleDeleteSession))
	mux.HandleFunc("POST /api/sessions/{id}/cancel", authGuard(handleCancelSession))
	mux.HandleFunc("POST /api/upload", heavyRL.wrap(authGuard(handleUpload)))

	mux.HandleFunc("GET /api/crafter/sheep-registry", authGuard(handleSheepRegistry))
	mux.HandleFunc("GET /api/crafter/vultr-models", authGuard(handleVultrModels))

	// Deploy stage: serve workspace files at /p/{slug}/ — PUBLIC (un-gated)
	mux.HandleFunc("GET /p/{slug}/{path...}", handleDeployServe)
}

func handleSheepRegistry(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(globalSheepRegistry)
}

func handleVultrModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(AllVultrModels())
}
