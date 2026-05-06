package channels

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type MessageLimits struct {
	MaxChars int
}

func SplitText(text string, limit int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{""}
	}
	if limit <= 0 || utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}

	chunks := make([]string, 0, (utf8.RuneCountInString(text)/limit)+1)
	remaining := text
	for len(remaining) > 0 {
		if utf8.RuneCountInString(remaining) <= limit {
			chunks = append(chunks, remaining)
			break
		}

		cut := longestPrefixByRunes(remaining, limit)
		prefix := remaining[:cut]
		splitAt := bestTextSplit(prefix)
		if splitAt <= 0 {
			splitAt = cut
		}

		chunk := strings.TrimSpace(remaining[:splitAt])
		if chunk == "" {
			chunk = strings.TrimSpace(prefix)
			splitAt = len(prefix)
		}
		chunks = append(chunks, chunk)
		remaining = strings.TrimSpace(remaining[splitAt:])
	}

	return chunks
}

func longestPrefixByRunes(text string, limit int) int {
	if limit <= 0 {
		return 0
	}
	runes := 0
	for idx := range text {
		if runes == limit {
			return idx
		}
		runes++
	}
	return len(text)
}

func bestTextSplit(text string) int {
	if idx := strings.LastIndexByte(text, '\n'); idx > 0 {
		return idx + 1
	}
	if idx := lastSentenceBoundary(text); idx > 0 {
		return idx
	}
	if idx := lastWhitespaceBoundary(text); idx > 0 {
		return idx
	}
	return 0
}

func lastSentenceBoundary(text string) int {
	best := 0
	for idx, r := range text {
		if !isSentencePunctuation(r) {
			continue
		}
		_, size := utf8.DecodeRuneInString(text[idx:])
		end, ok := sentenceBoundaryEnd(text[idx+size:], idx+size)
		if ok {
			best = end
		}
	}
	return best
}

func sentenceBoundaryEnd(rest string, end int) (int, bool) {
	for rest != "" {
		r, size := utf8.DecodeRuneInString(rest)
		if !isClosingPunctuation(r) {
			break
		}
		end += size
		rest = rest[size:]
	}
	if rest == "" {
		return end, true
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return end, unicode.IsSpace(r)
}

func lastWhitespaceBoundary(text string) int {
	best := 0
	for idx, r := range text {
		if idx > 0 && unicode.IsSpace(r) {
			_, size := utf8.DecodeRuneInString(text[idx:])
			best = idx + size
		}
	}
	return best
}

func isSentencePunctuation(r rune) bool {
	switch r {
	case '.', '!', '?':
		return true
	default:
		return false
	}
}

func isClosingPunctuation(r rune) bool {
	switch r {
	case '"', '\'', ')', ']', '}':
		return true
	default:
		return false
	}
}
