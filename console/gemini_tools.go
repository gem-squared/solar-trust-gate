package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ToolDecl struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type ToolCallLog struct {
	Name   string         `json:"name"`
	Args   map[string]any `json:"args"`
	Result string         `json:"result"`
}

type ToolExecutor func(name string, args map[string]any) string

func geminiToolCall(apiKey, model, prompt string, tools []ToolDecl, executor ToolExecutor) (string, []ToolCallLog, error) {
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", model, apiKey)

	funcDecls := make([]map[string]any, len(tools))
	for i, t := range tools {
		funcDecls[i] = map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"parameters":  t.Parameters,
		}
	}

	contents := []map[string]any{
		{"role": "user", "parts": []map[string]any{{"text": prompt}}},
	}

	var callLog []ToolCallLog
	maxIterations := 15

	for iter := 0; iter < maxIterations; iter++ {
		body := map[string]any{
			"contents": contents,
			"tools":    []map[string]any{{"functionDeclarations": funcDecls}},
			"generationConfig": map[string]any{
				"temperature":    0.2,
				"maxOutputTokens": 8192,
			},
		}
		data, _ := json.Marshal(body)

		log.Printf("[TOOL-CALL] iteration %d, %d contents, %d tool calls so far", iter+1, len(contents), len(callLog))

		client := &http.Client{Timeout: 180 * time.Second}
		resp, err := client.Post(url, "application/json", bytes.NewReader(data))
		if err != nil {
			return "", callLog, fmt.Errorf("gemini tool-call request: %w", err)
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != 200 {
			return "", callLog, fmt.Errorf("gemini HTTP %d: %s", resp.StatusCode, truncateBytes(respBody, 500))
		}

		var result map[string]any
		if err := json.Unmarshal(respBody, &result); err != nil {
			return "", callLog, fmt.Errorf("parse response: %w", err)
		}

		candidates, _ := result["candidates"].([]any)
		if len(candidates) == 0 {
			return "", callLog, fmt.Errorf("no candidates in response")
		}
		cand, _ := candidates[0].(map[string]any)
		content, _ := cand["content"].(map[string]any)
		parts, _ := content["parts"].([]any)

		var functionCalls []map[string]any
		var textParts []string

		for _, p := range parts {
			part, _ := p.(map[string]any)
			if fc, ok := part["functionCall"].(map[string]any); ok {
				functionCalls = append(functionCalls, fc)
			}
			if text, ok := part["text"].(string); ok && text != "" {
				textParts = append(textParts, text)
			}
		}

		if len(functionCalls) == 0 {
			log.Printf("[TOOL-CALL] done after %d iterations, %d tool calls", iter+1, len(callLog))
			return strings.Join(textParts, "\n"), callLog, nil
		}

		// Append model response to conversation
		contents = append(contents, map[string]any{"role": "model", "parts": parts})

		// Execute each function call and build responses
		var responseParts []map[string]any
		for _, fc := range functionCalls {
			fcName, _ := fc["name"].(string)
			fcArgs, _ := fc["args"].(map[string]any)

			log.Printf("[TOOL-CALL] executing %s(%v)", fcName, summarizeArgs(fcArgs))
			execResult := executor(fcName, fcArgs)

			callLog = append(callLog, ToolCallLog{
				Name:   fcName,
				Args:   fcArgs,
				Result: truncate(execResult, 500),
			})

			responseParts = append(responseParts, map[string]any{
				"functionResponse": map[string]any{
					"name": fcName,
					"response": map[string]any{
						"result": execResult,
					},
				},
			})
		}

		contents = append(contents, map[string]any{"role": "user", "parts": responseParts})
	}

	return "", callLog, fmt.Errorf("tool-call loop exceeded %d iterations", maxIterations)
}

func summarizeArgs(args map[string]any) string {
	if args == nil {
		return ""
	}
	var parts []string
	for k, v := range args {
		s := fmt.Sprintf("%v", v)
		if len(s) > 40 {
			s = s[:40] + "..."
		}
		parts = append(parts, fmt.Sprintf("%s=%s", k, s))
	}
	return strings.Join(parts, ", ")
}

// --- Workspace tools ---

func workspaceTools() []ToolDecl {
	return []ToolDecl{
		{
			Name:        "create_file",
			Description: "Create a new file with the given content. Path is relative to the workspace directory.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string", "description": "Relative file path (e.g., 'src/index.js')"},
					"content": map[string]any{"type": "string", "description": "File content to write"},
				},
				"required": []string{"path", "content"},
			},
		},
		{
			Name:        "write_file",
			Description: "Overwrite an existing file with new content. Path is relative to the workspace directory.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string", "description": "Relative file path to overwrite"},
					"content": map[string]any{"type": "string", "description": "New file content"},
				},
				"required": []string{"path", "content"},
			},
		},
		{
			Name:        "read_file",
			Description: "Read the content of a file. Path is relative to the workspace directory.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "Relative file path to read"},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        "list_files",
			Description: "List files in a directory. Path is relative to the workspace directory. Use '.' for root.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"directory": map[string]any{"type": "string", "description": "Relative directory path (use '.' for workspace root)"},
				},
				"required": []string{"directory"},
			},
		},
	}
}

func workspaceToolExecutorWithSlugStrip(baseDir, projectSlug string) ToolExecutor {
	inner := workspaceToolExecutor(baseDir)
	return func(name string, args map[string]any) string {
		if path, ok := args["path"].(string); ok && projectSlug != "" {
			cleaned := filepath.Clean(path)
			prefix := projectSlug + "/"
			if strings.HasPrefix(cleaned, prefix) {
				args["path"] = strings.TrimPrefix(cleaned, prefix)
				log.Printf("[TOOL-STRIP] stripped slug prefix: %q → %q", path, args["path"])
			}
		}
		if dir, ok := args["directory"].(string); ok && projectSlug != "" {
			cleaned := filepath.Clean(dir)
			prefix := projectSlug + "/"
			if strings.HasPrefix(cleaned, prefix) {
				args["directory"] = strings.TrimPrefix(cleaned, prefix)
			} else if cleaned == projectSlug {
				args["directory"] = "."
			}
		}
		return inner(name, args)
	}
}

func workspaceToolExecutor(baseDir string) ToolExecutor {
	return func(name string, args map[string]any) string {
		switch name {
		case "create_file", "write_file":
			path, _ := args["path"].(string)
			content, _ := args["content"].(string)
			if path == "" {
				return `{"error": "path is required"}`
			}
			absPath, err := safePath(baseDir, path)
			if err != nil {
				return fmt.Sprintf(`{"error": %q}`, err.Error())
			}
			if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
				return fmt.Sprintf(`{"error": "mkdir: %s"}`, err.Error())
			}
			if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
				return fmt.Sprintf(`{"error": "write: %s"}`, err.Error())
			}
			return fmt.Sprintf(`{"success": true, "path": %q, "bytes": %d}`, path, len(content))

		case "read_file":
			path, _ := args["path"].(string)
			if path == "" {
				return `{"error": "path is required"}`
			}
			absPath, err := safePath(baseDir, path)
			if err != nil {
				return fmt.Sprintf(`{"error": %q}`, err.Error())
			}
			data, err := os.ReadFile(absPath)
			if err != nil {
				return fmt.Sprintf(`{"error": "read: %s"}`, err.Error())
			}
			if len(data) > 4096 {
				data = data[:4096]
			}
			return fmt.Sprintf(`{"content": %q}`, string(data))

		case "list_files":
			dir, _ := args["directory"].(string)
			if dir == "" {
				dir = "."
			}
			absDir, err := safePath(baseDir, dir)
			if err != nil {
				return fmt.Sprintf(`{"error": %q}`, err.Error())
			}
			entries, err := os.ReadDir(absDir)
			if err != nil {
				return fmt.Sprintf(`{"error": "readdir: %s"}`, err.Error())
			}
			var files []string
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() {
					name += "/"
				}
				files = append(files, name)
			}
			data, _ := json.Marshal(map[string]any{"files": files})
			return string(data)

		default:
			return fmt.Sprintf(`{"error": "unknown tool: %s"}`, name)
		}
	}
}

func safePath(baseDir, relPath string) (string, error) {
	cleaned := filepath.Clean(relPath)
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") {
		return "", fmt.Errorf("path traversal rejected: %s", relPath)
	}
	abs := filepath.Join(baseDir, cleaned)
	if !strings.HasPrefix(abs, baseDir) {
		return "", fmt.Errorf("path escapes sandbox: %s", relPath)
	}
	return abs, nil
}
