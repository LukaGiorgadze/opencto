package textclean

import "strings"

// TerminalOutput removes terminal control bytes from text intended for prompts or chat.
func TerminalOutput(text string) string {
	if text == "" {
		return ""
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	runes := []rune(text)
	var builder strings.Builder
	builder.Grow(len(text))
	for index := 0; index < len(runes); {
		current := runes[index]
		switch {
		case current == '\x1b':
			index = skipEscapeSequence(runes, index+1)
		case current == '\u009b':
			index = skipCSISequence(runes, index+1)
		case terminalControlRune(current):
			index++
		default:
			builder.WriteRune(current)
			index++
		}
	}
	return builder.String()
}

func skipEscapeSequence(runes []rune, index int) int {
	if index >= len(runes) {
		return len(runes)
	}
	switch runes[index] {
	case '[':
		return skipCSISequence(runes, index+1)
	case ']', 'P', '^', '_', 'X':
		return skipStringSequence(runes, index+1)
	default:
		for index < len(runes) && runes[index] >= 0x20 && runes[index] <= 0x2f {
			index++
		}
		if index < len(runes) && runes[index] >= 0x30 && runes[index] <= 0x7e {
			return index + 1
		}
		return index
	}
}

func skipCSISequence(runes []rune, index int) int {
	for index < len(runes) {
		if runes[index] >= 0x40 && runes[index] <= 0x7e {
			return index + 1
		}
		index++
	}
	return len(runes)
}

func skipStringSequence(runes []rune, index int) int {
	for index < len(runes) {
		if runes[index] == '\a' {
			return index + 1
		}
		if runes[index] == '\x1b' && index+1 < len(runes) && runes[index+1] == '\\' {
			return index + 2
		}
		index++
	}
	return len(runes)
}

func terminalControlRune(value rune) bool {
	if value == '\n' || value == '\t' {
		return false
	}
	return value < 0x20 || value == 0x7f || (value >= 0x80 && value <= 0x9f)
}
