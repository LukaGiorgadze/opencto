package activities

import (
	"log/slog"
)

func (a *Activities) activityLogger() *slog.Logger {
	if a.Logger != nil {
		return a.Logger
	}
	return slog.Default()
}

func (a *Activities) logActivityStep(activity, step string, attrs ...any) {
	base := []any{
		slog.String("activity", activity),
		slog.String("step", step),
	}
	a.activityLogger().Info("runtime activity trace", append(base, attrs...)...)
}
