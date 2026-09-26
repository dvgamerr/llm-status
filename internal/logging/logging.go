// Package logging gives every llm-status subcommand a consistent, leveled
// zerolog.Logger instead of ad-hoc fmt.Fprintf(stderr, ...) calls. Output
// shape and verbosity are controlled by two environment variables:
//
//   - LOG_FORMAT: "text" switches to zerolog's human-readable ConsoleWriter
//     (for a terminal or a tailed log file); anything else, including unset,
//     produces raw JSON lines suitable for a log aggregator. Defaults to json.
//   - LOG_LEVEL: any zerolog level name ("debug", "info", "warn", "error",
//     ...), case-insensitive. Defaults to "info" when unset or unrecognized.
package logging

import (
	"io"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

func init() {
	zerolog.TimeFieldFormat = time.RFC3339
}

// New returns a logger scoped to one subcommand (e.g. "ingest", "relay"),
// writing timestamped, leveled lines to w. NoColor is forced in text mode
// because w is often a redirected file (--log-file) or a piped stderr, where
// ANSI escapes would just be noise. LOG_LEVEL is applied globally
// (zerolog.SetGlobalLevel) since it's a single process-wide verbosity knob,
// not something that varies per subcommand.
func New(w io.Writer, cmd string) zerolog.Logger {
	zerolog.SetGlobalLevel(levelFromEnv())

	writer := w
	if strings.EqualFold(strings.TrimSpace(os.Getenv("LOG_FORMAT")), "text") {
		writer = zerolog.ConsoleWriter{
			Out:        w,
			NoColor:    true,
			TimeFormat: "2006/01/02 15:04:05.000000",
		}
	}
	return zerolog.New(writer).With().Timestamp().Str("cmd", cmd).Logger()
}

func levelFromEnv() zerolog.Level {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL")))
	if raw == "" {
		// zerolog.ParseLevel("") returns (NoLevel, nil) rather than an
		// error — NoLevel filters out every normal level, so an unset
		// LOG_LEVEL would otherwise silence all logging instead of
		// defaulting to info.
		return zerolog.InfoLevel
	}
	level, err := zerolog.ParseLevel(raw)
	if err != nil {
		return zerolog.InfoLevel
	}
	return level
}
