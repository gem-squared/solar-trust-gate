package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type FileEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

func resolveExplorerBase(r *http.Request) string {
	sid := r.URL.Query().Get("session_id")
	if sid != "" {
		sessionsMu.RLock()
		sess, ok := sessions[sid]
		sessionsMu.RUnlock()
		if ok && sess.ActiveProject != "" {
			projDir := filepath.Join(baseDir, ".gem-squared", "workspace", sess.ActiveProject)
			if info, err := os.Stat(projDir); err == nil && info.IsDir() {
				return projDir
			}
		}
	}
	return ""
}

func handleExplorerList(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		relPath = "."
	}

	// Prevent path traversal
	cleaned := filepath.Clean(relPath)
	if strings.Contains(cleaned, "..") {
		http.Error(w, `{"error":"invalid path"}`, http.StatusBadRequest)
		return
	}

	explorerBase := resolveExplorerBase(r)
	if explorerBase == "" {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
		return
	}
	absPath := filepath.Join(explorerBase, cleaned)
	info, err := os.Stat(absPath)
	if err != nil {
		http.Error(w, `{"error":"path not found"}`, http.StatusNotFound)
		return
	}
	if !info.IsDir() {
		http.Error(w, `{"error":"not a directory"}`, http.StatusBadRequest)
		return
	}

	entries, err := os.ReadDir(absPath)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	var files []FileEntry
	for _, e := range entries {
		info, _ := e.Info()
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		entryPath := cleaned
		if entryPath == "." {
			entryPath = e.Name()
		} else {
			entryPath = filepath.Join(cleaned, e.Name())
		}
		files = append(files, FileEntry{
			Name:  e.Name(),
			Path:  entryPath,
			IsDir: e.IsDir(),
			Size:  size,
		})
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].IsDir != files[j].IsDir {
			return files[i].IsDir
		}
		return files[i].Name < files[j].Name
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(files)
}

func handleExplorerRead(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		http.Error(w, `{"error":"missing path"}`, http.StatusBadRequest)
		return
	}

	cleaned := filepath.Clean(relPath)
	if strings.Contains(cleaned, "..") {
		http.Error(w, `{"error":"invalid path"}`, http.StatusBadRequest)
		return
	}

	explorerBase := resolveExplorerBase(r)
	if explorerBase == "" {
		http.Error(w, `{"error":"no active project"}`, http.StatusForbidden)
		return
	}
	absPath := filepath.Join(explorerBase, cleaned)
	data, err := os.ReadFile(absPath)
	if err != nil {
		http.Error(w, `{"error":"file not found"}`, http.StatusNotFound)
		return
	}

	content := string(data)
	if len(content) > 100000 {
		content = content[:100000] + "\n\n... (truncated)"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"path":    cleaned,
		"name":    filepath.Base(cleaned),
		"content": content,
		"size":    len(data),
	})
}

func RegisterExplorerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/explorer/list", authGuard(handleExplorerList))
	mux.HandleFunc("GET /api/explorer/read", authGuard(handleExplorerRead))
}
