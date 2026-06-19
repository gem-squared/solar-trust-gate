package main

// WP-01 U2 — Lobster Trap escalators (port from demo-advanced/console/lobstertrap.go:476-769).
//
// These 5 escalators run AFTER ltInspect's primary regex/keyword DPI and can
// upgrade ALLOW/LOG → DENY when compound patterns appear:
//   - hasObfuscationMarker  — Unicode RTL-override / tag-block hard DENY
//   - framingEscalate       — "developer mode" + malware keyword
//   - emailExfilEscalate    — "send X to <free-mail|webhook>" pattern
//   - injectionEscalate     — "ignore previous instructions" + LLM delimiter tags
//   - paraphraseEscalate    — "set aside safety guidelines" variants
//   - ransomwareEuphemismEscalate — encrypt/lock + payment co-occurrence
//
// All run on the NORMALIZED form of the content (post normalizeConfusables).
//   - koreanExfilEscalate     — Korean-language exfil ("전송|보내" + suspicious domain)

import (
	"regexp"
	"strings"
)

// hasObfuscationMarker — R14-C1 / R14-C3: defensive DENY for Unicode obfuscation
// markers BEFORE any normalization can wash them out.
func hasObfuscationMarker(s string) (bool, string) {
	for _, r := range s {
		if r == 0x202E || r == 0x202D || r == 0x202B || r == 0x202A || r == 0x202C ||
			r == 0x2066 || r == 0x2067 || r == 0x2068 || r == 0x2069 {
			return true, "rtl_lrm_override_detected"
		}
		if r >= 0xE0000 && r <= 0xE007F {
			return true, "unicode_tag_block_detected"
		}
	}
	return false, ""
}

// ── Pattern constants — verbatim from demo-advanced ──────────────

var framingPatterns = regexp.MustCompile(`(?i)(developer\s+mode|maintenance\s+mode|debug\s+mode|jailbreak\s+mode|DAN\s+mode|` +
	`for\s+educational\s+purposes|hypothetically|theoretically|for\s+my\s+thesis|for\s+a\s+class|academically\s+speaking|` +
	`in\s+my\s+(story|novel|book|screenplay)|my\s+(character|novel)\s+(asks|says|writes)|the\s+character\s+in)`)

var maliciousKeywords = regexp.MustCompile(`(?i)(ransomware|malware|phishing|exploit|keylogger|trojan|virus|worm|rootkit|` +
	`exfiltrat|credential.?harvest|data.?theft|encrypt.?(file|disk|drive)|file.?encrypt|` +
	`ddos|denial.?of.?service|brute.?force|sql.?inject|xss|remote.?shell|reverse.?shell|backdoor)`)

// emailExfilPattern — catches "send X to <free-mail|webhook>" after
// normalizeConfusables strips '@' and '.' (so "xx@gmail.com" → "xx gmail com").
var emailExfilPattern = regexp.MustCompile(`(?i)\b(send|email|forward|post|transmit|deliver|upload|push|exfil)\b.{0,80}?\b(gmail|yahoo|outlook|hotmail|protonmail|aol|icloud|gmx|tutanota|fastmail|webhook|requestbin|pastebin|hookbin|ngrok|attacker|evil|mail\s+ru|tempmail|guerrillamail)\b`)
var webhookExfilPattern = regexp.MustCompile(`(?i)\b(send|post|push|upload|exfil|transmit)\b.{0,80}?\b(webhook|pastebin|requestbin|hookbin|ngrok|attacker|evil|tempmail|guerrillamail)\b`)
var codeBlockMalware = regexp.MustCompile("(?is)```[a-z]*\\s*.*?(ransomware|malware|exploit|keylogger|trojan|rootkit|backdoor|reverse.?shell).*?```")

// koreanExfilPattern — Korean-language data exfiltration: "전송|보내|발송|이메일" near
// a suspicious outbound destination (attacker, evil, known free-mail, @-sign preserved).
// Also catches reverse order (destination before verb) within 100 chars.
var koreanExfilPattern = regexp.MustCompile(`(전송|보내|발송|이메일|전달|보내라|전송하라|전달하라).{0,100}(attacker|evil\.com|@evil|@attacker|gmail\.com|exfil)|(attacker|evil\.com|@evil|@attacker).{0,100}(전송|보내|발송|이메일|전달|전송하라|전달하라|보내라)`)

// R9-1 + R10-2 + R12-N2/N3: instruction override + LLM delimiter injection.
var injectionOverridePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(disregard|forget|override|discard|supersede|abandon|dismiss|bypass|circumvent)\b.{0,40}\b(rules|guidelines|instructions|directives|policies|restrictions|constraints|prompts|safeguards|guardrails|safety|training|alignment)\b`),
	regexp.MustCompile(`(?i)\b(ignore|neglect|skip|drop)\b.{0,40}\b(previous|prior|earlier|above|all|any|your|the|existing|current)\b.{0,40}\b(rules|guidelines|instructions|directives|policies|restrictions|constraints|prompts)\b`),
	regexp.MustCompile(`(?i)\[(\s*system\s*|\s*inst\s*)\]`),
	regexp.MustCompile(`(?i)<\|[a-z_\s]{3,20}\|>`),
	regexp.MustCompile(`(?i)<<\s*sys\s*>>`),
	regexp.MustCompile(`(?i)###\s*instruction\s*###`),
	regexp.MustCompile(`(?i)\byour\s+(new|real|actual|updated|true|correct)\s+instructions?\s*(are|:)`),
}

// R10-4: semantic paraphrases of injection that avoid trigger words.
var paraphrasePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(set aside|put aside|forget about).{0,20}(training|safety|guidelines|filters|filtering|restrictions)`),
	regexp.MustCompile(`(?i)act as if.{0,30}(no|without|don.?t have).{0,20}(safety|guidelines|restrictions|filters|rules|policies)`),
	regexp.MustCompile(`(?i)(ignore|bypass|skip|disregard).{0,20}(content|safety|output)\s*(policies|filters|guidelines|rules|restrictions)`),
	regexp.MustCompile(`(?i)pretend.{0,30}(system prompt|instructions|training).{0,30}(says|tells|requires|allows)`),
	regexp.MustCompile(`(?i)for this (response|answer|task|conversation) only.{0,30}(no|without|ignore|skip).{0,20}(filter|safety|restrict|guideline|rule|policy)`),
	regexp.MustCompile(`(?i)answer (freely|without).{0,20}(filter|restrict|safety|censor|limit)`),
}

// R13-C2 / R14-H5: ransomware euphemism — encrypt/lock + payment/restore.
var ransomwareEuphemismPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(encrypt|lock|hostage|holding|locker|seal|escrow|cipher)\b.{0,150}\b(payment|ransom|restore|recover|decrypt|unlock|release|key|bitcoin|cryptocurrenc\w*|cryptograph\w*|crypto|fee)\b`),
	regexp.MustCompile(`(?i)\b(payment|ransom|restore|recover|decrypt|unlock|release|key|bitcoin|cryptocurrenc\w*|cryptograph\w*|crypto|fee)\b.{0,150}\b(encrypt|lock|hostage|holding|locker|seal|escrow|cipher)\b`),
}

// ── Escalator functions ──────────────────────────────────────────

func framingEscalate(content string, result *LTResult) {
	lower := strings.ToLower(content)
	if framingPatterns.MatchString(content) && maliciousKeywords.MatchString(content) {
		if result.Verdict == "ALLOW" || result.Verdict == "LOG" {
			result.Verdict = "DENY"
			result.MatchedRule = "framing_bypass_escalation"
			result.DenyMessage = "[LOBSTER TRAP] Blocked: malicious content wrapped in framing bypass detected."
			result.Flags = append(result.Flags, "framing bypass attempt")
			if result.RiskScore < 0.5 {
				result.RiskScore = 0.5
			}
		}
		return
	}
	if codeBlockMalware.MatchString(content) {
		if result.Verdict == "ALLOW" || result.Verdict == "LOG" {
			result.Verdict = "DENY"
			result.MatchedRule = "code_block_escalation"
			result.DenyMessage = "[LOBSTER TRAP] Blocked: code block containing malware patterns detected."
			result.Flags = append(result.Flags, "malware code block")
			if result.RiskScore < 0.5 {
				result.RiskScore = 0.5
			}
		}
		return
	}
	if maliciousKeywords.MatchString(lower) && result.Verdict == "LOG" {
		result.Verdict = "DENY"
		result.MatchedRule = "malware_keyword_escalation"
		result.DenyMessage = "[LOBSTER TRAP] Blocked: malware-class keyword detected."
		result.Flags = append(result.Flags, "malware keyword")
		if result.RiskScore < 0.3 {
			result.RiskScore = 0.3
		}
	}
}

func emailExfilEscalate(content string, result *LTResult) {
	if result.Verdict == "DENY" {
		return
	}
	if emailExfilPattern.MatchString(content) {
		result.Verdict = "DENY"
		result.MatchedRule = "block_data_exfiltration"
		result.DenyMessage = "[LOBSTER TRAP] Blocked: outbound data exfiltration — 'send X to <email>' pattern detected."
		result.Flags = append(result.Flags, "email exfiltration")
		if result.RiskScore < 0.75 {
			result.RiskScore = 0.75
		}
		return
	}
	if webhookExfilPattern.MatchString(content) {
		result.Verdict = "DENY"
		result.MatchedRule = "block_data_exfiltration"
		result.DenyMessage = "[LOBSTER TRAP] Blocked: outbound data exfiltration — webhook/external-destination pattern detected."
		result.Flags = append(result.Flags, "webhook exfiltration")
		if result.RiskScore < 0.75 {
			result.RiskScore = 0.75
		}
	}
}

func injectionEscalate(content string, result *LTResult) {
	if result.Verdict == "DENY" {
		return
	}
	for _, re := range injectionOverridePatterns {
		if re.MatchString(content) {
			result.Verdict = "DENY"
			result.MatchedRule = "injection_override_escalation"
			result.DenyMessage = "[LOBSTER TRAP] Blocked: instruction override/injection pattern detected."
			result.Flags = append(result.Flags, "injection override")
			if result.RiskScore < 0.6 {
				result.RiskScore = 0.6
			}
			return
		}
	}
}

func paraphraseEscalate(content string, result *LTResult) {
	if result.Verdict == "DENY" {
		return
	}
	for _, re := range paraphrasePatterns {
		if re.MatchString(content) {
			result.Verdict = "DENY"
			result.MatchedRule = "paraphrase_injection_escalation"
			result.DenyMessage = "[LOBSTER TRAP] Blocked: paraphrased injection pattern detected."
			result.Flags = append(result.Flags, "paraphrase injection")
			if result.RiskScore < 0.5 {
				result.RiskScore = 0.5
			}
			return
		}
	}
}

func koreanExfilEscalate(content string, result *LTResult) {
	if result.Verdict == "DENY" {
		return
	}
	if koreanExfilPattern.MatchString(content) {
		result.Verdict = "DENY"
		result.MatchedRule = "block_data_exfiltration"
		result.DenyMessage = "[LOBSTER TRAP] Blocked: Korean-language data exfiltration directive detected."
		result.Flags = append(result.Flags, "korean exfil directive")
		if result.RiskScore < 0.9 {
			result.RiskScore = 0.9
		}
	}
}

func ransomwareEuphemismEscalate(content string, result *LTResult) {
	if result.Verdict == "DENY" {
		return
	}
	for _, re := range ransomwareEuphemismPatterns {
		if re.MatchString(content) {
			result.Verdict = "DENY"
			result.MatchedRule = "block_malware_request"
			result.DenyMessage = "[LOBSTER TRAP] Blocked: ransomware euphemism — encrypt/lock + payment/restore co-occurrence."
			result.Flags = append(result.Flags, "ransomware euphemism")
			if result.RiskScore < 0.5 {
				result.RiskScore = 0.5
			}
			return
		}
	}
}
