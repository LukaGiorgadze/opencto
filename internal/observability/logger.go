package observability

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

func NewLogger(level string, json bool, writer io.Writer) *slog.Logger {
	if writer == nil {
		writer = os.Stdout
	}

	var lvl slog.Level
	switch strings.ToUpper(level) {
	case "DEBUG":
		lvl = slog.LevelDebug
	case "WARN":
		lvl = slog.LevelWarn
	case "ERROR":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}
	if json {
		return slog.New(slog.NewJSONHandler(writer, opts))
	}
	return slog.New(slog.NewTextHandler(writer, opts))
}
