package runner

import (
	"regexp"
	"strings"
)

var sensitivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*bearer\s+)[^\s"']+`),
	regexp.MustCompile(`(?i)((?:api[_-]?key|token|password|secret)\s*[:=]\s*)[^\s,"']+`),
}

func redactText(value string) string {
	result := value
	for _, pattern := range sensitivePatterns {
		result = pattern.ReplaceAllString(result, `${1}[REDACTED]`)
	}
	return result
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		clean := make(map[string]any, len(typed))
		for key, item := range typed {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "password") || strings.Contains(lower, "secret") ||
				strings.Contains(lower, "token") || strings.Contains(lower, "api_key") ||
				strings.Contains(lower, "apikey") {
				clean[key] = "[REDACTED]"
				continue
			}
			clean[key] = redactValue(item)
		}
		return clean
	case []any:
		clean := make([]any, len(typed))
		for index, item := range typed {
			clean[index] = redactValue(item)
		}
		return clean
	case string:
		return redactText(typed)
	default:
		return value
	}
}
