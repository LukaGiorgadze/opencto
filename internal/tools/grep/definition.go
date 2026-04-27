package grep

import (
	_ "embed"
	"encoding/json"
)

const (
	GrepToolName        = "Grep"
	GrepToolDescription = `A powerful search tool built on ripgrep

  Usage:
  - ALWAYS use Grep for search tasks. NEVER invoke grep or rg as a Shell/Bash command. The Grep tool has been optimized for correct permissions and access.
  - Supports full regex syntax (e.g., "log.*Error", "functions+w+")
  - Filter files with glob parameter (e.g., "*.go", "**/*.tsx") or type parameter (e.g., "js", "py", "rust", "go")
  - Output modes: "content" shows matching lines, "files_with_matches" shows only file paths (default), "count" shows match counts
  - Pattern syntax: Uses ripgrep (not grep) - literal braces need escaping (use interface{} to find interface{} in Go code)
  - Multiline matching: By default patterns match within single lines only. For cross-line patterns like struct {[sS]*?field, use multiline: true`
)

//go:embed schema.json
var grepToolSchema json.RawMessage

func GrepToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), grepToolSchema...)
}
