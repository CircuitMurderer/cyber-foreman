package verification

import (
	"regexp"
	"strings"
)

var credentialAssignment = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*(?:bearer\s+)?|(?:api[_-]?key|token|secret|password)\s*[:=]\s*)[^\s,;]+`)

func redact(text string, environment []string, extraValues []string) string {
	values := append([]string(nil), extraValues...)
	for _, item := range environment {
		key, value, ok := strings.Cut(item, "=")
		if !ok || len(value) < 4 || !looksSensitiveKey(key) {
			continue
		}
		values = append(values, value)
	}
	for _, value := range values {
		if len(value) >= 4 {
			text = strings.ReplaceAll(text, value, "[REDACTED]")
		}
	}
	return credentialAssignment.ReplaceAllString(text, `${1}[REDACTED]`)
}

func looksSensitiveKey(key string) bool {
	key = strings.ToUpper(key)
	for _, marker := range []string{"KEY", "TOKEN", "SECRET", "PASSWORD", "AUTH"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}
