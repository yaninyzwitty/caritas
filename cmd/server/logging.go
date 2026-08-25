package main

import (
	"log/slog"
	"os"
)

// exitOnError preserves process-fail-fast startup behavior while using slog's
// structured output. Without it, startup failures would either keep using
// log.Fatalf or repeat slog.Error plus os.Exit at every fatal branch.
func exitOnError(message string, err error) {
	if err == nil {
		return
	}
	slog.Error(message, "error", err)
	os.Exit(1)
}
