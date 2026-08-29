// Package mirror transports already-sanitized snapshots to another host.
package mirror

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/dvgamerr/claude-status/internal/limitio"
	"github.com/dvgamerr/claude-status/internal/model"
	"github.com/dvgamerr/claude-status/internal/winconsole"
)

// DefaultRemoteBinary is the conventional Raspberry Pi installation path.
const DefaultRemoteBinary = "/home/pi/.local/bin/claude-status"

var (
	hostPattern      = regexp.MustCompile(`^[A-Za-z0-9._@:-]+$`)
	remoteBinPattern = regexp.MustCompile(`^[A-Za-z0-9_./~-]+$`)
	commandContext   = exec.CommandContext
)

// SSH sends only the already-sanitized Snapshot to a trusted SSH target. The
// source provider payload and authentication material never enter this path.
func SSH(ctx context.Context, host, remoteBinary string, snapshot model.Snapshot) error {
	host = strings.TrimSpace(host)
	remoteBinary = strings.TrimSpace(remoteBinary)
	if host == "" {
		return errors.New("SSH mirror host is empty")
	}
	if !hostPattern.MatchString(host) || strings.HasPrefix(host, "-") {
		return fmt.Errorf("invalid SSH mirror host %q", host)
	}
	if remoteBinary == "" || !remoteBinPattern.MatchString(remoteBinary) {
		return fmt.Errorf("invalid remote binary path %q", remoteBinary)
	}

	data, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode sanitized snapshot: %w", err)
	}
	data = append(data, '\n')

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := commandContext(timeoutCtx, "ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=3",
		host, remoteBinary, "import",
	)
	// The relay runs unattended several times a minute; on Windows each
	// ssh child would otherwise be handed its own console window.
	winconsole.SuppressChildConsole(command)
	command.Stdin = bytes.NewReader(data)
	stderr := limitio.NewBuffer(limitio.DiagnosticLimit)
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if timeoutCtx.Err() != nil {
			return fmt.Errorf("mirror sanitized snapshot to %s: %w", host, timeoutCtx.Err())
		}
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return fmt.Errorf("mirror sanitized snapshot to %s: %w: %s", host, err, detail)
		}
		return fmt.Errorf("mirror sanitized snapshot to %s: %w", host, err)
	}
	return nil
}
