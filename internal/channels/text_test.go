package channels

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitTextPrefersNewline(t *testing.T) {
	message := strings.Repeat("a", 16) + "\n" + strings.Repeat("b", 8)

	chunks := SplitText(message, 20)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if chunks[0] != strings.Repeat("a", 16) {
		t.Fatalf("unexpected first chunk: %q", chunks[0])
	}
	if chunks[1] != strings.Repeat("b", 8) {
		t.Fatalf("unexpected second chunk: %q", chunks[1])
	}
}

func TestSplitTextPrefersMultipleLineBreaksBeforeSingleLineBreakOrDot(t *testing.T) {
	message := "intro paragraph\n\n\nmiddle line\nlater sentence. tail continues"

	chunks := SplitText(message, 45)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if chunks[0] != "intro paragraph" {
		t.Fatalf("unexpected first chunk: %q", chunks[0])
	}
	if chunks[1] != "middle line\nlater sentence. tail continues" {
		t.Fatalf("unexpected second chunk: %q", chunks[1])
	}
}

func TestSplitTextPrefersSentenceBoundaryBeforeWhitespace(t *testing.T) {
	message := "First sentence. Second sentence keeps going"

	chunks := SplitText(message, 30)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if chunks[0] != "First sentence." {
		t.Fatalf("unexpected first chunk: %q", chunks[0])
	}
	if chunks[1] != "Second sentence keeps going" {
		t.Fatalf("unexpected second chunk: %q", chunks[1])
	}
}

func TestSplitTextDoesNotLeaveMarkdownHeadingAtChunkEnd(t *testing.T) {
	message := "previous line\n## Notes\nKeep this with the heading."

	chunks := SplitText(message, len("previous line\n## Notes\nKeep this with the"))
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if chunks[0] != "previous line" {
		t.Fatalf("unexpected first chunk: %q", chunks[0])
	}
	if chunks[1] != "## Notes\nKeep this with the heading." {
		t.Fatalf("unexpected second chunk: %q", chunks[1])
	}
}

func TestSplitTextDoesNotSendMarkdownHeadingAloneAtChunkStart(t *testing.T) {
	message := "## Notes\nKeep this with the heading body."

	chunks := SplitText(message, len("## Notes\nKeep this"))
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}
	if chunks[0] != "## Notes\nKeep" {
		t.Fatalf("unexpected first chunk: %q", chunks[0])
	}
}

func TestSplitTextFallsBackToWhitespace(t *testing.T) {
	message := "alpha beta gamma delta"

	chunks := SplitText(message, 16)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if chunks[0] != "alpha beta" {
		t.Fatalf("unexpected first chunk: %q", chunks[0])
	}
	if chunks[1] != "gamma delta" {
		t.Fatalf("unexpected second chunk: %q", chunks[1])
	}
}

func TestSplitTextFallsBackToHardSplitForLongWord(t *testing.T) {
	message := strings.Repeat("x", 25)

	chunks := SplitText(message, 10)
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}
	if strings.Join(chunks, "") != message {
		t.Fatalf("split/join did not preserve content")
	}
}

func TestSplitTextRespectsRuneLimit(t *testing.T) {
	message := strings.Repeat("界", 7) + " " + strings.Repeat("a", 4)

	chunks := SplitText(message, 8)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	for i, chunk := range chunks {
		if utf8.RuneCountInString(chunk) > 8 {
			t.Fatalf("chunk %d exceeds limit: %d", i, utf8.RuneCountInString(chunk))
		}
	}
}

func TestSplitTextTwentyThousandCharactersAtTwoThousandLimit(t *testing.T) {
	message := strings.Repeat("x", 20000)

	chunks := SplitText(message, 2000)
	if len(chunks) != 10 {
		t.Fatalf("expected 10 chunks, got %d", len(chunks))
	}
	for i, chunk := range chunks {
		if utf8.RuneCountInString(chunk) != 2000 {
			t.Fatalf("chunk %d has unexpected size: %d", i, utf8.RuneCountInString(chunk))
		}
	}
}
