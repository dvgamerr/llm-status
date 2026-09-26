// Package envfile loads simple KEY=VALUE pairs from a local .env file into
// the process environment, so local development picks up settings like
// PORT, LOG_LEVEL, and LOG_FORMAT without exporting them in the shell.
// It is not used by the installed Windows Service or systemd unit, which
// configure those values through service.Config.Args/Env instead — this is
// strictly a developer convenience for `go run` / a locally built binary.
package envfile

import (
	"bufio"
	"os"
	"strings"
)

// Load reads path (typically ".env") and calls os.Setenv for each KEY=VALUE
// line whose KEY is not already set in the process environment — a real
// environment variable always wins over the file. A missing file is not an
// error; it just means there's nothing to load.
func Load(path string) error {
	file, err := os.Open(path) // #nosec G304 -- path is a fixed local dev convenience file, not user input.
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = unquote(strings.TrimSpace(value))
		if key == "" {
			continue
		}
		if _, set := os.LookupEnv(key); set {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// LoadDefault loads ".env" from the current working directory.
func LoadDefault() error {
	return Load(".env")
}

func unquote(value string) string {
	if len(value) < 2 {
		return value
	}
	first, last := value[0], value[len(value)-1]
	if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
		return value[1 : len(value)-1]
	}
	return value
}
