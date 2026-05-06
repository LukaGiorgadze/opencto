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
		splitAt := bestTextSplit(remaining, cut)
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

func bestTextSplit(text string, limitBytes int) int {
	prefix := text[:limitBytes]
	if idx := lastMultiLineBoundary(prefix); idx > 0 {
		if idx = markdownHeadingSafeSplit(text, idx); idx > 0 {
			return idx
		}
	}
	if idx := lastLineBoundary(prefix); idx > 0 {
		if idx = markdownHeadingSafeSplit(text, idx); idx > 0 {
			return idx
		}
	}
	if idx := lastDotBoundary(text, limitBytes); idx > 0 {
		if idx = markdownHeadingSafeSplit(text, idx); idx > 0 {
			return idx
		}
	}
	if idx := lastWhitespaceBoundary(prefix); idx > 0 {
		if idx = markdownHeadingSafeSplit(text, idx); idx > 0 {
			return idx
		}
	}
	return 0
}

func markdownHeadingSafeSplit(text string, splitAt int) int {
	splitAt = avoidTrailingMarkdownHeading(text, splitAt)
	if leavesOnlyMarkdownHeading(text, splitAt) {
		return 0
	}
	return splitAt
}

func avoidTrailingMarkdownHeading(text string, splitAt int) int {
	if splitAt <= 0 || splitAt >= len(text) {
		return splitAt
	}
	candidate := strings.TrimRight(text[:splitAt], " \t\r\n")
	lineStart := strings.LastIndexByte(candidate, '\n') + 1
	if lineStart <= 0 {
		return splitAt
	}
	if isMarkdownHeading(strings.TrimSpace(candidate[lineStart:])) {
		return lineStart
	}
	return splitAt
}

func leavesOnlyMarkdownHeading(text string, splitAt int) bool {
	if splitAt <= 0 || splitAt >= len(text) {
		return false
	}
	candidate := strings.TrimSpace(text[:splitAt])
	return !strings.Contains(candidate, "\n") && isMarkdownHeading(candidate)
}

func isMarkdownHeading(line string) bool {
	count := 0
	for count < len(line) && line[count] == '#' {
		count++
	}
	return count > 0 && count <= 6 && (count == len(line) || line[count] == ' ' || line[count] == '\t')
}

func lastMultiLineBoundary(text string) int {
	best := 0
	runStart := -1
	lineBreaks := 0
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\r':
			if runStart < 0 {
				runStart = i
			}
		case '\n':
			if runStart < 0 {
				runStart = i
			}
			lineBreaks++
			if lineBreaks >= 2 && runStart > 0 {
				best = i + 1
			}
		default:
			runStart = -1
			lineBreaks = 0
		}
	}
	return best
}

func lastLineBoundary(text string) int {
	if idx := strings.LastIndexByte(text, '\n'); idx > 0 {
		return idx + 1
	}
	return 0
}

func lastDotBoundary(text string, limitBytes int) int {
	best := 0
	for idx, r := range text[:limitBytes] {
		if r != '.' {
			continue
		}
		_, size := utf8.DecodeRuneInString(text[idx:])
		end, ok := dotBoundaryEnd(text[idx+size:], idx+size, limitBytes)
		if ok {
			best = end
		}
	}
	return best
}

func dotBoundaryEnd(rest string, end, limitBytes int) (int, bool) {
	for rest != "" && end < limitBytes {
		r, size := utf8.DecodeRuneInString(rest)
		if !isClosingPunctuation(r) {
			break
		}
		end += size
		rest = rest[size:]
	}
	if rest == "" {
		return 0, false
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

func isClosingPunctuation(r rune) bool {
	switch r {
	case '"', '\'', ')', ']', '}':
		return true
	default:
		return false
	}
}
