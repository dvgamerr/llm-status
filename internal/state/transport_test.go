package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dvgamerr/llm-status/internal/model"
)

func TestDecodeSnapshotAcceptsSanitizedSchema(t *testing.T) {
	want := model.Snapshot{
		SchemaVersion: model.CurrentSchemaVersion,
		CapturedAt:    time.Unix(123, 0).UTC(),
		Provider:      "codex",
		Session:       model.Session{ID: "thread-123"},
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeSnapshot(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("DecodeSnapshot() error = %v", err)
	}
	if got.Provider != want.Provider || got.Session.ID != want.Session.ID || !got.CapturedAt.Equal(want.CapturedAt) {
		t.Fatalf("DecodeSnapshot() = %+v, want %+v", got, want)
	}
}

func TestDecodeSnapshotRejectsRawOrMalformedPayloads(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", input: " ", want: "empty"},
		{name: "unknown raw field", input: `{"schema_version":1,"prompt":"secret"}`, want: "unknown field"},
		{name: "multiple", input: `{}` + "\n" + `{}`, want: "multiple"},
		{name: "trailing garbage", input: `{}` + "not-json", want: "decode trailing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeSnapshot(strings.NewReader(tt.input))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("DecodeSnapshot() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestDecodeSnapshotRejectsOversizedPayload(t *testing.T) {
	oversized := bytes.Repeat([]byte("x"), maxSnapshotBytes+1)
	_, err := DecodeSnapshot(bytes.NewReader(oversized))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("DecodeSnapshot() error = %v, want size-limit error", err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, errors.New("simulated read failure")
}

func TestDecodeSnapshotPropagatesReadError(t *testing.T) {
	_, err := DecodeSnapshot(errReader{})
	if err == nil || !strings.Contains(err.Error(), "read sanitized snapshot") {
		t.Fatalf("DecodeSnapshot() error = %v, want read error", err)
	}
}
