// Package redact provides best-effort secret removal, not a DLP boundary.
package redact

import "regexp"

var rules = []struct {
	pattern *regexp.Regexp
	replace string
}{
	{regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----.*?-----END (?:[A-Z0-9]+ )*PRIVATE KEY-----`), `[REDACTED:private-key]`},
	{regexp.MustCompile(`(?im)(\bauthorization\b["']?[ \t]*:[ \t]*)(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^\r\n]+)`), `${1}[REDACTED:authorization]`},
	{regexp.MustCompile(`(?im)(\b(?:set-cookie|cookie)\b["']?[ \t]*:[ \t]*)(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^\r\n]+)`), `${1}[REDACTED:cookie]`},
	{regexp.MustCompile(`(?i)(["']?\b[A-Za-z0-9_.-]*(?:password|passwd|secret|token|api[_-]?key)[A-Za-z0-9_.-]*["']?[ \t]*(?::|=)[ \t]*)(?:"""(?s:.*?)"""|'''(?s:.*?)'''|` + "`(?:\\\\.|[^`\\\\])*`" + `|"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^ \t\r\n,;}\])]+)`), `${1}[REDACTED:assignment]`},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`), `[REDACTED:aws-key]`},
	{regexp.MustCompile(`(?i)\bBearer[ \t]+[A-Za-z0-9._~+/-]+=*`), `Bearer [REDACTED:token]`},
	{regexp.MustCompile(`\b(?:sk-(?:proj-)?[A-Za-z0-9_-]{16,}|(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{12,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[baprs]-[A-Za-z0-9-]{10,})\b`), `[REDACTED:token]`},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\b`), `[REDACTED:token]`},
}

// Text removes recognized secret values while preserving ordinary diff text.
func Text(s string) string {
	for _, rule := range rules {
		s = rule.pattern.ReplaceAllString(s, rule.replace)
	}
	return s
}
