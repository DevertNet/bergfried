package github

import (
	"regexp"
	"strings"
)

var redactors = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(-----END [A-Z ]*PRIVATE KEY-----|$)`), "[geschwärzt: privater Schlüssel]"},
	{regexp.MustCompile(`\b(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}\b`), "[geschwärzt: GitHub-Token]"},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`), "[geschwärzt: GitHub-Token]"},
	{regexp.MustCompile(`\b(AKIA|ASIA)[A-Z0-9]{16}\b`), "[geschwärzt: AWS-Schlüssel]"},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`), "[geschwärzt: API-Schlüssel]"},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`), "[geschwärzt: Slack-Token]"},
	{regexp.MustCompile(`(?i)(authorization:\s*(?:bearer|basic|token)\s+)\S+`), "${1}[geschwärzt]"},
	{regexp.MustCompile(`(?i)\b((?:password|passwd|secret|token|api[_-]?key|access[_-]?key)\s*[:=]\s*)["']?[^\s"']{6,}["']?`), "${1}[geschwärzt]"},
	{regexp.MustCompile(`(?i)(https?://)[^/\s:@]+:[^/\s@]+@`), "${1}[geschwärzt]@"},
}

// Redact schwärzt typische Geheimnisse. Ein Netz, keine Garantie (Logs gehen deshalb nur nach Bestätigung an den Agent).
func Redact(s string) string {
	for _, r := range redactors {
		s = r.re.ReplaceAllString(s, r.with)
	}
	return s
}

// FailureExcerpt kürzt ein Job-Log auf das Wesentliche: alle Zeilen mit ##[error] samt Kontext plus das Ende, geschwärzt, höchstens maxBytes.
func FailureExcerpt(log string, maxBytes int) string {
	log = strings.TrimPrefix(log, "\ufeff")
	lines := strings.Split(strings.ReplaceAll(log, "\r\n", "\n"), "\n")
	keep := make([]bool, len(lines))
	for i, l := range lines {
		if strings.Contains(l, "##[error]") {
			for j := i - 12; j <= i+3; j++ {
				if j >= 0 && j < len(lines) {
					keep[j] = true
				}
			}
		}
	}
	for i := len(lines) - 80; i < len(lines); i++ {
		if i >= 0 {
			keep[i] = true
		}
	}
	var b strings.Builder
	prev := -2
	for i, l := range lines {
		if !keep[i] {
			continue
		}
		if prev >= 0 && i != prev+1 {
			b.WriteString("[…]\n")
		}
		b.WriteString(stripTimestamp(l))
		b.WriteByte('\n')
		prev = i
	}
	out := Redact(b.String())
	if len(out) > maxBytes {
		out = "[… gekürzt …]\n" + out[len(out)-maxBytes:]
	}
	return out
}

var tsRe = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z `)

func stripTimestamp(l string) string { return tsRe.ReplaceAllString(l, "") }
