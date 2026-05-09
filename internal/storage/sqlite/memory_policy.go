package sqlite

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/opencto/opencto/internal/storage"
)

const maxMemoryContentChars = 2000

var (
	secretPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)-----BEGIN [A-Z ]*PRIVATE KEY-----`),
		regexp.MustCompile(`(?i)\b(api[_-]?key|secret|password|passwd|token|access[_-]?token|refresh[_-]?token)\b\s*[:=]\s*["']?[A-Za-z0-9._~+/=-]{8,}`),
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`),
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9_]{20,}\b`),
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	}
	diffHeaderPattern    = regexp.MustCompile(`(?m)^(@@ |\+\+\+ |--- |diff --git )`)
	stackTracePattern    = regexp.MustCompile(`(?i)(traceback \(most recent call last\)|\bpanic:|goroutine \d+ \[|exception in thread|^\s*at [\w.$/<>]+\()`)
	commandOutputPattern = regexp.MustCompile(`(?i)(^|\n)(stdout|stderr|exit code|command|args|requested_action):\s*`)
)

func validateMemoryPolicy(content, kind string) error {
	if err := validateMemoryContent(content); err != nil {
		return err
	}
	if _, err := normalizeMemoryKind(kind); err != nil {
		return err
	}
	return nil
}

func validateMemoryContent(content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return memoryPolicyError("content is required")
	}
	if len([]rune(content)) > maxMemoryContentChars {
		return memoryPolicyError("content is too long: got %d characters, max %d", len([]rune(content)), maxMemoryContentChars)
	}
	if lowSignalMemoryContent(content) {
		return memoryPolicyError("content is too short or low-signal")
	}
	if containsObviousSecret(content) {
		return memoryPolicyError("content appears to contain a secret")
	}
	if looksLikeRawArtifact(content) {
		return memoryPolicyError("content looks like raw logs, diffs, stack traces, or command output")
	}
	if looksTemporaryMemory(content) {
		return memoryPolicyError("content appears to describe temporary task state")
	}
	return nil
}

func normalizeMemoryKind(kind string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "fact", "facts":
		return "fact", nil
	case "pref", "prefs", "preference", "preferences":
		return "preference", nil
	case "instruction", "instructions", "rule", "rules", "standing_instruction":
		return "instruction", nil
	case "decision", "decisions":
		return "decision", nil
	case "constraint", "constraints", "safety", "security":
		return "constraint", nil
	case "identity", "profile":
		return "identity", nil
	case "workflow", "workflows", "process":
		return "workflow", nil
	case "reference", "references", "pointer", "link":
		return "reference", nil
	case "feedback", "communication", "style":
		return "feedback", nil
	default:
		return "", memoryPolicyError("unsupported memory kind %q", kind)
	}
}

func normalizedMemoryContent(content string) string {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(content)))
	return strings.Join(fields, " ")
}

func memoryPolicyError(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{storage.ErrMemoryPolicyRejected}, args...)...)
}

func lowSignalMemoryContent(content string) bool {
	lettersOrDigits := 0
	words := 0
	for _, field := range strings.Fields(content) {
		hasSignal := false
		for _, r := range field {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				lettersOrDigits++
				hasSignal = true
			}
		}
		if hasSignal {
			words++
		}
	}
	if lettersOrDigits < 8 || words < 3 {
		return true
	}
	switch normalizedMemoryContent(content) {
	case "remember this", "save this", "use this", "do this", "ok thanks", "yes please", "no thanks":
		return true
	default:
		return false
	}
}

func containsObviousSecret(content string) bool {
	for _, pattern := range secretPatterns {
		if pattern.MatchString(content) {
			return true
		}
	}
	return false
}

func looksLikeRawArtifact(content string) bool {
	if diffHeaderPattern.MatchString(content) || stackTracePattern.MatchString(content) {
		return true
	}
	if commandOutputPattern.MatchString(content) && strings.Count(content, "\n") >= 2 {
		return true
	}
	return false
}

func looksTemporaryMemory(content string) bool {
	normalized := normalizedMemoryContent(content)
	temporaryPhrases := []string{
		"for this task",
		"for this request",
		"for this run",
		"for this migration",
		"for now",
		"today",
		"tonight",
		"right now",
		"just this once",
		"this time only",
		"temporarily",
		"temporary",
		"while debugging",
		"current task",
		"current run",
	}
	for _, phrase := range temporaryPhrases {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}
