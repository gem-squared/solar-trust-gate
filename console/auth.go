package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	authFailWindow = 15 * time.Minute
	authFailMax    = 5
)

var validKeys map[string]bool

var authRL = struct {
	sync.Mutex
	wins map[string][]time.Time
}{wins: make(map[string][]time.Time)}

func initAuthKeys() {
	validKeys = make(map[string]bool)
	if keys := os.Getenv("AUTH_KEYS"); keys != "" {
		for _, k := range strings.Split(keys, ",") {
			k = strings.TrimSpace(k)
			if k != "" {
				validKeys[k] = true
			}
		}
	}
}

func authEnabled() bool { return len(validKeys) > 0 }

// firstAuthKey returns one of the configured AUTH_KEYS for the single-tenant
// demo flow (CE viewer link prefill). Returns empty string if auth is disabled.
// Maps iteration is non-deterministic but acceptable when AUTH_KEYS has a
// single entry (current VPS configuration).
func firstAuthKey() string {
	for k := range validKeys {
		return k
	}
	return ""
}

func authClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.Index(xff, ","); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return strings.TrimSpace(xr)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func authRLCheck(ip string) (blocked bool, retryAfter int) {
	now := time.Now()
	cutoff := now.Add(-authFailWindow)
	authRL.Lock()
	defer authRL.Unlock()
	w := authRL.wins[ip]
	valid := w[:0]
	for _, ts := range w {
		if ts.After(cutoff) {
			valid = append(valid, ts)
		}
	}
	authRL.wins[ip] = valid
	if len(valid) >= authFailMax {
		oldest := valid[0]
		ra := int(oldest.Add(authFailWindow).Sub(now).Seconds()) + 1
		if ra < 1 {
			ra = 1
		}
		return true, ra
	}
	return false, 0
}

func authRLRecord(ip string) {
	authRL.Lock()
	authRL.wins[ip] = append(authRL.wins[ip], time.Now())
	authRL.Unlock()
}

func keyPrefix(k string) string {
	if len(k) > 4 {
		return k[:4] + "..."
	}
	return "***"
}

func handleAuth(w http.ResponseWriter, r *http.Request) {
	if !authEnabled() {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return
	}

	ip := authClientIP(r)
	if blocked, retryAfter := authRLCheck(ip); blocked {
		log.Printf("[AUTH_BLOCKED] ip=%s failures=%d window=15m retry_after=%ds", ip, authFailMax, retryAfter)
		w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprintf(w, `{"error":"too many authentication failures","retry_after":%d}`, retryAfter)
		return
	}

	var req struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	if !validKeys[req.Key] {
		authRLRecord(ip)
		log.Printf("[AUTH_FAIL] ip=%s key_prefix=%s", ip, keyPrefix(req.Key))
		http.Error(w, `{"error":"invalid key"}`, http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"token": req.Key})
}

func authGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authEnabled() {
			next(w, r)
			return
		}
		key := r.Header.Get("X-Access-Key")
		if key == "" {
			if bearer := r.Header.Get("Authorization"); strings.HasPrefix(bearer, "Bearer ") {
				key = strings.TrimPrefix(bearer, "Bearer ")
			}
		}
		if !validKeys[key] {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func authGuardQuery(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authEnabled() {
			next(w, r)
			return
		}
		key := r.URL.Query().Get("key")
		if key == "" {
			key = r.Header.Get("X-Access-Key")
		}
		if !validKeys[key] {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}
