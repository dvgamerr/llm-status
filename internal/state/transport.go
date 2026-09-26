package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/dvgamerr/llm-status/internal/model"
)

const maxSnapshotBytes = 256 << 10

// DecodeSnapshot accepts only the deliberately small Snapshot schema. This is
// the trust boundary used by the Pi: raw Claude/Codex payloads, transcripts,
// credentials, and future arbitrary fields are rejected instead of persisted.
func DecodeSnapshot(input io.Reader) (model.Snapshot, error) {
	var snapshot model.Snapshot
	data, err := io.ReadAll(io.LimitReader(input, maxSnapshotBytes+1))
	if err != nil {
		return snapshot, fmt.Errorf("read sanitized snapshot: %w", err)
	}
	if len(data) > maxSnapshotBytes {
		return snapshot, fmt.Errorf("sanitized snapshot exceeds %d bytes", maxSnapshotBytes)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return snapshot, errors.New("sanitized snapshot is empty")
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return snapshot, fmt.Errorf("decode sanitized snapshot: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return snapshot, errors.New("sanitized snapshot contains multiple JSON values")
		}
		return snapshot, fmt.Errorf("decode trailing sanitized snapshot data: %w", err)
	}
	return snapshot, nil
}
