package write

import (
	_ "embed"
	"encoding/json"
)

const (
	WriteToolName        = "Write"
	WriteToolDescription = `Writes a new file to the local filesystem.

Usage:
- Use this tool ONLY to create new files or perform complete rewrites of existing files. For partial modifications, use the "Edit" tool instead.
- This tool will overwrite the existing file if there is one at the provided path.
- If this is an existing file you intend to fully rewrite, you MUST use the "Read" tool first to read the file's contents. This tool will fail if you did not read the file first.
- NEVER create documentationsm *.md/README files unless explicitly requested by the User.
- Only use emojis and dashes if the user explicitly requests it. Avoid writing emojis and dashes to files unless asked.`
)

//go:embed schema.json
var writeToolSchema json.RawMessage

func WriteToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), writeToolSchema...)
}
