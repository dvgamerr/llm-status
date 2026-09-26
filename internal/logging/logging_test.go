package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestNewDefaultsToJSONAtInfoLevel(t *testing.T) {
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("LOG_FORMAT", "")

	var output bytes.Buffer
	logger := New(&output, "test-command")
	logger.Debug().Msg("should be filtered by the default info level")
	logger.Info().Str("key", "value").Msg("hello")

	got := strings.TrimSpace(output.String())
	lines := strings.Split(got, "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly one log line (debug filtered), got %d: %q", len(lines), got)
	}

	var entry map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("log line is not valid JSON: %v (%q)", err, lines[0])
	}
	if entry["message"] != "hello" || entry["cmd"] != "test-command" || entry["key"] != "value" || entry["level"] != "info" {
		t.Fatalf("unexpected JSON fields: %+v", entry)
	}
}

func TestNewTextFormatWritesPlainConsoleLog(t *testing.T) {
	t.Setenv("LOG_FORMAT", "text")

	var output bytes.Buffer
	logger := New(&output, "test-command")
	logger.Info().Str("key", "value").Msg("hello")
	got := output.String()
	for _, want := range []string{"INF", "hello", "cmd=test-command", "key=value"} {
		if !strings.Contains(got, want) {
			t.Fatalf("log output %q does not contain %q", got, want)
		}
	}
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("log output contains ANSI color: %q", got)
	}
}

func TestNewLogLevelFiltersBelowConfiguredLevel(t *testing.T) {
	t.Setenv("LOG_LEVEL", "warn")
	t.Setenv("LOG_FORMAT", "text")

	var output bytes.Buffer
	logger := New(&output, "test-command")
	logger.Info().Msg("should be filtered")
	logger.Warn().Msg("should appear")

	got := output.String()
	if strings.Contains(got, "should be filtered") {
		t.Fatalf("info line should have been filtered at warn level: %q", got)
	}
	if !strings.Contains(got, "should appear") {
		t.Fatalf("warn line missing: %q", got)
	}
}
