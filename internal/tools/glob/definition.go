package glob

import (
	_ "embed"
	"encoding/json"
)

const (
	GlobToolName        = "Glob"
	GlobToolDescription = `Fast file pattern matching tool that works with any codebase size. Use this tool when you need to find files by name patterns

- Supports glob patterns like "*.go", "**/*.go", or "src/**/*.ts"
- Searches from the optional path parameter, or the current working directory when path is omitted
- Returns absolute matching file paths sorted by modification time, newest first`
)

//go:embed schema.json
var globToolSchema json.RawMessage

func GlobToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), globToolSchema...)
}
