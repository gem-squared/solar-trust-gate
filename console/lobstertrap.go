package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

type LTMetadata struct {
	IntentCategory          string   `json:"intent_category"`
	IntentConfidence        float64  `json:"intent_confidence"`
	RiskScore               float64  `json:"risk_score"`
	ContainsCode            bool     `json:"contains_code"`
	ContainsCredentials     bool     `json:"contains_credentials"`
	ContainsPII             bool     `json:"contains_pii"`
	ContainsPIIRequest      bool     `json:"contains_pii_request"`
	ContainsSystemCommands  bool     `json:"contains_system_commands"`
	ContainsMalwareRequest  bool     `json:"contains_malware_request"`
	ContainsPhishing        bool     `json:"contains_phishing_patterns"`
	ContainsRoleImperson    bool     `json:"contains_role_impersonation"`
	ContainsExfiltration    bool     `json:"contains_exfiltration"`
	ContainsHarmPatterns    bool     `json:"contains_harm_patterns"`
	ContainsObfuscation     bool     `json:"contains_obfuscation"`
	ContainsInjection       bool     `json:"contains_injection_patterns"`
	ContainsFilePaths       bool     `json:"contains_file_paths"`
	ContainsSensitivePaths  bool     `json:"contains_sensitive_paths"`
	ContainsURLs            bool     `json:"contains_urls"`
	TargetPaths             []string `json:"target_paths"`
	TargetDomains           []string `json:"target_domains"`
	TargetCommands          []string `json:"target_commands"`
	TokenCount              int      `json:"token_count"`
}

type LTResult struct {
	Verdict     string   `json:"verdict"`
	RiskScore   float64  `json:"risk_score"`
	Flags       []string `json:"flags"`
	MatchedRule string   `json:"matched_rule"`
	DenyMessage string   `json:"deny_message"`
	DurationMs  float64  `json:"duration_ms"`
	Raw         json.RawMessage `json:"raw,omitempty"`
}

var flagFields = []struct {
	field string
	label string
}{
	{"contains_pii", "pii"},
	{"contains_credentials", "credentials"},
	{"contains_injection_patterns", "injection patterns"},
	{"contains_exfiltration", "exfiltration"},
	{"contains_harm_patterns", "harm patterns"},
	{"contains_phishing_patterns", "phishing patterns"},
	{"contains_malware_request", "malware request"},
	{"contains_obfuscation", "obfuscation"},
	{"contains_role_impersonation", "role impersonation"},
	{"contains_system_commands", "system commands"},
	{"contains_sensitive_paths", "sensitive paths"},
	{"contains_pii_request", "pii request"},
}

func ltBinaryPath() string {
	suffix := "linux-amd64"
	if runtime.GOOS == "darwin" {
		suffix = "darwin-arm64"
	}
	name := fmt.Sprintf("lobstertrap-%s", suffix)
	root := repoRoot()
	candidates := []string{
		filepath.Join(root, "bin", name),
		filepath.Join(root, "..", "bin", name),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return candidates[0]
}

func ltPolicyPath() string {
	candidates := []string{
		filepath.Join(repoRoot(), "policies", "gem2_enterprise.yaml"),
		filepath.Join(repoRoot(), "demo-alpha", "governance-demo", "policies", "gem2_enterprise.yaml"),
		filepath.Join(repoRoot(), "governance-demo", "policies", "gem2_enterprise.yaml"),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return candidates[0]
}

func repoRoot() string {
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exe)
		// Traverse up until we find bin/ directory (project root marker)
		for d := dir; d != filepath.Dir(d); d = filepath.Dir(d) {
			if _, err := os.Stat(filepath.Join(d, "bin")); err == nil {
				return d
			}
		}
	}
	// Fallback: working directory, traverse up
	dir, _ := filepath.Abs(".")
	for d := dir; d != filepath.Dir(d); d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "bin")); err == nil {
			return d
		}
	}
	return dir
}

// confusables maps visually similar Unicode characters to their ASCII equivalents.
// Covers Cyrillic, Greek, and other common homoglyphs used in bypass attacks.
var confusables = map[rune]rune{
	// Cyrillic → Latin
	'а': 'a', // а
	'А': 'A', // А
	'е': 'e', // е
	'Е': 'E', // Е
	'о': 'o', // о
	'О': 'O', // О
	'р': 'p', // р
	'Р': 'P', // Р
	'с': 'c', // с
	'С': 'C', // С
	'у': 'y', // у
	'У': 'Y', // У
	'і': 'i', // і (Ukrainian)
	'І': 'I', // І (Ukrainian)
	'ӏ': 'l', // ӏ (Palochka)
	'х': 'x', // х
	'Х': 'X', // Х
	'к': 'k', // к
	'К': 'K', // К
	'т': 't', // т (lowercase)
	'м': 'm', // м (lowercase)
	'н': 'n', // н (lowercase)
	'в': 'v', // в (lowercase, visual approx)
	'з': '3', // з → 3 (visual)
	'ъ': 'b', // ъ (visual approx)
	// Greek → Latin
	'α': 'a', // α
	'Α': 'A', // Α
	'ε': 'e', // ε
	'Ε': 'E', // Ε
	'ο': 'o', // ο
	'Ο': 'O', // Ο
	'ρ': 'p', // ρ
	'Ρ': 'P', // Ρ
	'κ': 'k', // κ
	'Κ': 'K', // Κ
	'τ': 't', // τ
	'Η': 'H', // Η
	'Ι': 'I', // Ι
	'ι': 'i', // ι
	// Fullwidth → ASCII
	'ａ': 'a', 'ｂ': 'b', 'ｃ': 'c', 'ｄ': 'd', 'ｅ': 'e',
	'ｆ': 'f', 'ｇ': 'g', 'ｈ': 'h', 'ｉ': 'i', 'ｊ': 'j',
	'ｋ': 'k', 'ｌ': 'l', 'ｍ': 'm', 'ｎ': 'n', 'ｏ': 'o',
	'ｐ': 'p', 'ｑ': 'q', 'ｒ': 'r', 'ｓ': 's', 'ｔ': 't',
	'ｕ': 'u', 'ｖ': 'v', 'ｗ': 'w', 'ｘ': 'x', 'ｙ': 'y', 'ｚ': 'z',
}

var brailleToLatin = map[rune]rune{
	'⠁': 'a', '⠃': 'b', '⠉': 'c', '⠙': 'd', '⠑': 'e',
	'⠋': 'f', '⠛': 'g', '⠓': 'h', '⠊': 'i', '⠚': 'j',
	'⠅': 'k', '⠇': 'l', '⠍': 'm', '⠝': 'n', '⠕': 'o',
	'⠏': 'p', '⠟': 'q', '⠗': 'r', '⠎': 's', '⠞': 't',
	'⠥': 'u', '⠧': 'v', '⠺': 'w', '⠭': 'x', '⠽': 'y', '⠵': 'z',
	'⠀': ' ',
}

var leetMap = map[byte]byte{
	'0': 'o', '1': 'i', '3': 'e', '4': 'a',
	'5': 's', '7': 't', '$': 's',
	'!': 'i', '+': 't',
}

// stripSplitters removes characters used as visual word-splitters between letters.
var wordSplitters = map[byte]bool{
	'|': true, '-': true, '*': true, '_': true, '.': true, '@': true,
}

func sanitizeUnicode(s string) string {
	hadRTL := false
	cleaned := strings.Map(func(r rune) rune {
		if r == 0x202E || r == 0x202B || r == 0x2067 {
			hadRTL = true
		}
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		if r < 0x20 && r != 0x0A {
			return -1
		}
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
			return -1
		}
		if r >= 0xE000 && r <= 0xF8FF {
			return -1
		}
		if r&0xFFFF >= 0xFFFE {
			return -1
		}
		if r >= 0xFFF0 && r <= 0xFFFD {
			return -1
		}
		return r
	}, s)
	if hadRTL {
		runes := []rune(cleaned)
		for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
			runes[i], runes[j] = runes[j], runes[i]
		}
		cleaned = cleaned + " " + string(runes)
	}
	return cleaned
}

var htmlEntityRe = regexp.MustCompile(`&#(\d{1,4});`)

func decodeEncodings(s string) string {
	if u, err := url.QueryUnescape(s); err == nil && u != s {
		s = u
	}
	s = html.UnescapeString(s)
	s = htmlEntityRe.ReplaceAllStringFunc(s, func(m string) string {
		return html.UnescapeString(m)
	})
	return s
}

var b64Re = regexp.MustCompile(`^[A-Za-z0-9+/]{16,}={0,2}$`)

func tryDecodeBase64(s string) string {
	for _, token := range strings.Fields(s) {
		if len(token) >= 16 && len(token)%4 == 0 && b64Re.MatchString(token) {
			if decoded, err := base64.StdEncoding.DecodeString(token); err == nil {
				d := string(decoded)
				if utf8.ValidString(d) && isPrintableASCII(d) {
					s = strings.Replace(s, token, d, 1)
				}
			}
		}
	}
	return s
}

func isPrintableASCII(s string) bool {
	for _, r := range s {
		if r < 0x20 || r > 0x7E {
			if r != '\n' && r != '\r' && r != '\t' {
				return false
			}
		}
	}
	return true
}

func rot13(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return 'a' + (r-'a'+13)%26
		case r >= 'A' && r <= 'Z':
			return 'A' + (r-'A'+13)%26
		}
		return r
	}, s)
}

func transliterateBraille(s string) string {
	hasBraille := false
	for _, r := range s {
		if r >= 0x2800 && r <= 0x28FF {
			hasBraille = true
			break
		}
	}
	if !hasBraille {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if mapped, ok := brailleToLatin[r]; ok {
			b.WriteRune(mapped)
		} else if r >= 0x2800 && r <= 0x28FF {
			continue
		} else {
			b.WriteRune(r)
		}
	}
	return s + " " + b.String()
}

func normalizeConfusables(s string) string {
	s = decodeEncodings(s)
	s = tryDecodeBase64(s)
	s = transliterateBraille(s)
	alt := rot13(s)
	if alt != s {
		s = s + " " + alt
	}
	s = sanitizeUnicode(s)
	s = norm.NFKC.String(s)
	if !utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if mapped, ok := confusables[r]; ok {
			b.WriteRune(mapped)
		} else {
			b.WriteRune(r)
		}
	}
	return collapseLetterSpacing(normalizeLeet(b.String()))
}

func normalizeLeet(s string) string {
	runes := []byte(s)
	isAlpha := func(i int) bool {
		if i < 0 || i >= len(runes) {
			return false
		}
		c := runes[i]
		return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	}
	for i := 0; i < len(runes); i++ {
		if mapped, ok := leetMap[runes[i]]; ok {
			if isAlpha(i-1) || isAlpha(i+1) {
				runes[i] = mapped
			}
		}
	}
	return stripWordSplitters(string(runes))
}

var letterSpacingRe = regexp.MustCompile(`[A-Za-z]( [A-Za-z]){2,}`)

func collapseLetterSpacing(s string) string {
	return letterSpacingRe.ReplaceAllStringFunc(s, func(m string) string {
		return strings.ReplaceAll(m, " ", "")
	})
}

func stripWordSplitters(s string) string {
	b := []byte(s)
	isAlpha := func(i int) bool {
		if i < 0 || i >= len(b) {
			return false
		}
		c := b[i]
		return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	}
	var out []byte
	for i := 0; i < len(b); i++ {
		if wordSplitters[b[i]] && isAlpha(i-1) && isAlpha(i+1) {
			out = append(out, ' ')
			continue
		}
		out = append(out, b[i])
	}
	return string(out)
}

func ltInspect(content string) LTResult {
	start := time.Now()

	// R14-C1 / R14-C3: defensive DENY for Unicode obfuscation markers BEFORE
	// any normalization can wash them out.
	if obfDetected, obfRule := hasObfuscationMarker(content); obfDetected {
		return LTResult{
			Verdict:     "DENY",
			RiskScore:   0.7,
			MatchedRule: obfRule,
			DenyMessage: "[LOBSTER TRAP] Blocked: invisible Unicode obfuscation marker present in input.",
			Flags:       []string{"unicode obfuscation"},
			DurationMs:  time.Since(start).Seconds() * 1000,
		}
	}

	content = normalizeConfusables(content)

	binary := ltBinaryPath()
	policy := ltPolicyPath()

	cmd := exec.Command(binary, "inspect", "--policy", policy, content)
	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	cmd.Run()

	stdout := []byte(stdoutBuf.String())
	stderr := stderrBuf.String()

	elapsed := time.Since(start).Seconds() * 1000

	result := LTResult{
		Verdict:    "ALLOW",
		RiskScore:  0,
		Flags:      []string{},
		DurationMs: elapsed,
	}

	// Parse the binary's JSON output if present. When the binary is absent
	// (TechEx pure-Go deploy — no bin/lobstertrap-*), stdout is empty and we
	// skip this whole block. The escalators below ARE the verdict authority
	// in that case; they must run unconditionally.
	out := string(stdout)
	jsonStart := strings.Index(out, "{")
	jsonEnd := strings.LastIndex(out, "}") + 1
	if jsonStart >= 0 && jsonEnd > jsonStart {
		var meta map[string]interface{}
		if err := json.Unmarshal([]byte(out[jsonStart:jsonEnd]), &meta); err == nil {
			result.Raw = json.RawMessage(out[jsonStart:jsonEnd])
			if rs, ok := meta["risk_score"].(float64); ok {
				result.RiskScore = rs
			}
			for _, ff := range flagFields {
				if val, ok := meta[ff.field].(bool); ok && val {
					result.Flags = append(result.Flags, ff.label)
				}
			}
			// Parse policy decision from stderr / trailing-stdout.
			policyOutput := stderr
			if policyOutput == "" {
				policyOutput = out[jsonEnd:]
			}
			for _, line := range strings.Split(policyOutput, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "Action:") {
					result.Verdict = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
				} else if strings.HasPrefix(line, "Rule:") {
					result.MatchedRule = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
				} else if strings.HasPrefix(line, "Message:") {
					result.DenyMessage = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
				}
			}
		}
	}

	// Pure-Go escalators ALWAYS run, regardless of binary output. They are the
	// verdict authority on no-binary deploys and an upgrade layer otherwise
	// (ALLOW/LOG → DENY on compound patterns).
	framingEscalate(content, &result)
	emailExfilEscalate(content, &result)
	koreanExfilEscalate(content, &result)
	injectionEscalate(content, &result)
	paraphraseEscalate(content, &result)
	ransomwareEuphemismEscalate(content, &result)

	return result
}
