package activities

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/opencto/opencto/internal/textclean"
	edittool "github.com/opencto/opencto/internal/tools/edit"
	globtool "github.com/opencto/opencto/internal/tools/glob"
	greptool "github.com/opencto/opencto/internal/tools/grep"
	readtool "github.com/opencto/opencto/internal/tools/read"
	skilltool "github.com/opencto/opencto/internal/tools/skill"
	writetool "github.com/opencto/opencto/internal/tools/write"
)

func resultCodeForError(err error) string {
	if err != nil {
		return "1"
	}
	return "0"
}

func readObservation(result readtool.Result, err error) string {
	if len(result.Actions) > 0 {
		return readBatchObservation(result, err)
	}
	if err != nil {
		return fullObservation("", "", err)
	}
	var builder strings.Builder
	_, _ = fmt.Fprintf(
		&builder, "file: %s\nlines: %d/%d\nbytes: %d\ntruncated: %t",
		result.FilePath,
		result.LinesRead,
		result.TotalLines,
		result.BytesRead,
		result.Truncated,
	)
	if result.Content != "" {
		builder.WriteString("\ncontent:\n")
		builder.WriteString(result.Content)
	}
	return builder.String()
}

func readBatchObservation(result readtool.Result, err error) string {
	var builder strings.Builder
	_, _ = fmt.Fprintf(
		&builder, "files: %d\nlines: %d/%d\nbytes: %d\ntruncated: %t",
		len(result.Actions),
		result.LinesRead,
		result.TotalLines,
		result.BytesRead,
		result.Truncated,
	)
	for _, action := range result.Actions {
		_, _ = fmt.Fprintf(
			&builder, "\n\nfile: %s\nlines: %d/%d\nbytes: %d\ntruncated: %t",
			action.FilePath,
			action.LinesRead,
			action.TotalLines,
			action.BytesRead,
			action.Truncated,
		)
		if action.Content != "" {
			builder.WriteString("\ncontent:\n")
			builder.WriteString(action.Content)
		}
	}
	if err != nil {
		builder.WriteString("\n\nerror:\n")
		builder.WriteString(err.Error())
	}
	return builder.String()
}

func editObservation(result edittool.Result, err error) string {
	if err != nil {
		return fullObservation("", "", err)
	}
	return fmt.Sprintf("edited: %s\nreplacements: %d\nbytes_written: %d", result.FilePath, result.Replacements, result.BytesWritten)
}

func writeObservation(result writetool.Result, err error) string {
	if err != nil {
		return fullObservation("", "", err)
	}
	return fmt.Sprintf("wrote: %s\nbytes_written: %d\noverwritten: %t", result.FilePath, result.BytesWritten, result.Overwritten)
}

func globObservation(result globtool.Result, err error) string {
	if len(result.Actions) > 0 {
		return globBatchObservation(result, err)
	}
	if err != nil {
		return fullObservation("", "", err)
	}
	if len(result.Matches) == 0 {
		return fmt.Sprintf("pattern: %s\npath: %s\nmatches: 0", result.Pattern, result.Root)
	}
	return fmt.Sprintf(
		"pattern: %s\npath: %s\nmatches: %d\n%s",
		result.Pattern,
		result.Root,
		len(result.Matches),
		strings.Join(result.Matches, "\n"),
	)
}

func globBatchObservation(result globtool.Result, err error) string {
	var builder strings.Builder
	_, _ = fmt.Fprintf(&builder, "patterns: %d\nmatches: %d", len(result.Actions), len(result.Matches))
	for _, action := range result.Actions {
		_, _ = fmt.Fprintf(
			&builder, "\n\npattern: %s\npath: %s\nmatches: %d",
			action.Pattern,
			action.Root,
			len(action.Matches),
		)
		if len(action.Matches) > 0 {
			builder.WriteString("\n")
			builder.WriteString(strings.Join(action.Matches, "\n"))
		}
	}
	if err != nil {
		builder.WriteString("\n\nerror:\n")
		builder.WriteString(err.Error())
	}
	return builder.String()
}

func grepObservation(result greptool.Result, err error) string {
	if err != nil {
		return fullObservation(result.Stdout, result.Stderr, err)
	}
	if strings.TrimSpace(result.Stdout) == "" && strings.TrimSpace(result.Stderr) == "" && result.ExitCode == 1 {
		return "No matches found."
	}
	return fullObservation(result.Stdout, result.Stderr, nil)
}

func skillObservation(result skilltool.Result, err error) string {
	if err != nil {
		return fullObservation("", "", err)
	}
	return fmt.Sprintf(
		"<skill_content name=%q>\n%s\n\nSkill directory: %s\nRelative paths in this skill are relative to the skill directory.\n</skill_content>",
		result.SkillID,
		strings.TrimSpace(result.Content),
		filepath.Dir(result.Path),
	)
}

func resolveRelativeToolPath(path, base string) string {
	path = strings.TrimSpace(path)
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	if strings.TrimSpace(base) == "" {
		return path
	}
	return filepath.Join(base, path)
}

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}

func fullObservation(stdout, stderr string, err error) string {
	stdout = strings.TrimSpace(textclean.TerminalOutput(stdout))
	stderr = strings.TrimSpace(textclean.TerminalOutput(stderr))
	var parts []string
	if stdout != "" {
		parts = append(parts, "stdout:\n"+stdout)
	}
	if stderr != "" {
		parts = append(parts, "stderr:\n"+stderr)
	}
	if err != nil {
		parts = append(parts, "error:\n"+strings.TrimSpace(textclean.TerminalOutput(err.Error())))
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n\n")
	}
	return "Execution completed."
}
