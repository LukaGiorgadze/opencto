package discord

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitDiscordMessageByLines(t *testing.T) {
	message := strings.Repeat("a", 3990) + "\n" + strings.Repeat("b", 50)

	chunks := splitDiscordMessage(message, discordMessageMaxLength)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	for i, chunk := range chunks {
		if utf8.RuneCountInString(chunk) > discordMessageMaxLength {
			t.Fatalf("chunk %d exceeds limit: %d", i, utf8.RuneCountInString(chunk))
		}
	}
	if chunks[0] != strings.Repeat("a", 3990) {
		t.Fatalf("unexpected first chunk length/content")
	}
	if chunks[1] != strings.Repeat("b", 50) {
		t.Fatalf("unexpected second chunk length/content")
	}
}

func TestSplitDiscordMessageFallsBackToHardSplit(t *testing.T) {
	message := strings.Repeat("x", discordMessageMaxLength+25)

	chunks := splitDiscordMessage(message, discordMessageMaxLength)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if utf8.RuneCountInString(chunks[0]) != discordMessageMaxLength {
		t.Fatalf("unexpected first chunk size: %d", utf8.RuneCountInString(chunks[0]))
	}
	if utf8.RuneCountInString(chunks[1]) != 25 {
		t.Fatalf("unexpected second chunk size: %d", utf8.RuneCountInString(chunks[1]))
	}
	if strings.Join(chunks, "") != message {
		t.Fatalf("split/join did not preserve content")
	}
}
