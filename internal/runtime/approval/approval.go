package approval

import (
	"regexp"
	"strings"
)

var legacyTicketPattern = regexp.MustCompile(`(?i)\b[QP]-[0-9a-f]{8}\b`)

var approvedPhrases = map[string]struct{}{
	"approve":         {},
	"approved":        {},
	"confirmed":       {},
	"continue":        {},
	"do it":           {},
	"execute":         {},
	"go":              {},
	"go ahead":        {},
	"go for it":       {},
	"implement it":    {},
	"lgtm":            {},
	"looks good":      {},
	"ok":              {},
	"okay":            {},
	"please proceed":  {},
	"proceed":         {},
	"run it":          {},
	"ship it":         {},
	"sounds good":     {},
	"y":               {},
	"yeah":            {},
	"yep":             {},
	"yes":             {},
	"yes please":      {},
	"please do it":    {},
	"please continue": {},
}

func IsApprovalPhrase(body string) bool {
	normalized := NormalizePhrase(body)
	if normalized == "" {
		return false
	}
	if _, ok := approvedPhrases[normalized]; ok {
		return true
	}
	withoutTicket := strings.TrimSpace(legacyTicketPattern.ReplaceAllString(normalized, ""))
	withoutTicket = strings.Join(strings.Fields(withoutTicket), " ")
	if withoutTicket == normalized {
		return false
	}
	_, ok := approvedPhrases[withoutTicket]
	return ok
}

func NormalizePhrase(body string) string {
	body = strings.ToLower(strings.TrimSpace(body))
	var builder strings.Builder
	lastSpace := false
	for _, r := range body {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
			lastSpace = false
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastSpace = false
		case r == '-' && builder.Len() > 0:
			builder.WriteRune(r)
			lastSpace = false
		default:
			if !lastSpace {
				builder.WriteByte(' ')
				lastSpace = true
			}
		}
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}
