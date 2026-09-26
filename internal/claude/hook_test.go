package claude

import (
	"errors"
	"strings"
	"testing"

	"github.com/dvgamerr/llm-status/internal/model"
)

// errReader is an io.Reader that always fails, used to exercise the
// read-error branch of DecodeHook/Decode without any production seam.
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func TestDecodeHookAcceptsUnknownFieldsAndTrailingData(t *testing.T) {
	raw := `{"session_id":"abc","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm -rf /"},"transcript_path":"/secret.jsonl"}`
	hook, err := DecodeHook(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("DecodeHook() error = %v", err)
	}
	if hook.SessionID != "abc" || hook.HookEventName != "PreToolUse" {
		t.Fatalf("DecodeHook() = %+v", hook)
	}

	if _, err := DecodeHook(strings.NewReader(`{"session_id":"a"}{"session_id":"b"}`)); err == nil {
		t.Fatal("expected error for multiple JSON values")
	}
	if _, err := DecodeHook(strings.NewReader(`   `)); err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestDecodeHookReportsReadError(t *testing.T) {
	wantErr := errors.New("boom")
	_, err := DecodeHook(errReader{err: wantErr})
	if err == nil || !strings.Contains(err.Error(), "read hook input") {
		t.Fatalf("DecodeHook() error = %v, want read-error wrap", err)
	}
}

func TestDecodeHookRejectsOversizedInput(t *testing.T) {
	raw := `{"padding":"` + strings.Repeat("x", maxHookInputBytes) + `"}`
	_, err := DecodeHook(strings.NewReader(raw))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("DecodeHook() error = %v, want size error", err)
	}
}

func TestDecodeHookRejectsInvalidJSON(t *testing.T) {
	_, err := DecodeHook(strings.NewReader(`{`))
	if err == nil || !strings.Contains(err.Error(), "decode hook JSON") {
		t.Fatalf("DecodeHook() error = %v, want decode error", err)
	}
}

func TestDecodeHookRejectsMalformedTrailingData(t *testing.T) {
	_, err := DecodeHook(strings.NewReader(`{} {invalid`))
	if err == nil || !strings.Contains(err.Error(), "trailing hook data") {
		t.Fatalf("DecodeHook() error = %v, want trailing-data error", err)
	}
}

func TestActivityForHook(t *testing.T) {
	cases := []struct {
		event    string
		message  string
		toolName string
		want     string
		wantOK   bool
	}{
		{event: "UserPromptSubmit", want: model.ActivityThinking, wantOK: true},
		{event: "PreToolUse", toolName: "Bash", want: model.ActivityBuilding, wantOK: true},
		{event: "PreToolUse", toolName: "Edit", want: model.ActivityTyping, wantOK: true},
		{event: "PreToolUse", toolName: "Read", want: model.ActivityTyping, wantOK: true},
		{event: "PreToolUse", toolName: "Task", want: model.ActivityTyping, wantOK: true},
		{event: "PreToolUse", toolName: "", want: model.ActivityTyping, wantOK: true},
		{event: "Stop", want: model.ActivityIdle, wantOK: true},
		{event: "Notification", message: "Claude needs your permission to use Bash", want: model.ActivityWaitingApproval, wantOK: true},
		{event: "Notification", message: "Claude is waiting for your input", want: "", wantOK: false},
		{event: "SessionStart", want: "", wantOK: false},
		{event: "SubagentStart", want: "", wantOK: false},
		{event: "SubagentStop", want: "", wantOK: false},
		{event: "", want: "", wantOK: false},
	}
	for _, testCase := range cases {
		state, ok := ActivityForHook(HookInput{HookEventName: testCase.event, Message: testCase.message, ToolName: testCase.toolName})
		if state != testCase.want || ok != testCase.wantOK {
			t.Errorf("ActivityForHook(%q, tool=%q, %q) = (%q, %v), want (%q, %v)", testCase.event, testCase.toolName, testCase.message, state, ok, testCase.want, testCase.wantOK)
		}
	}
}

func TestSubagentDeltaForHook(t *testing.T) {
	cases := []struct {
		event     string
		wantDelta int
		wantOK    bool
	}{
		{event: "SubagentStart", wantDelta: 1, wantOK: true},
		{event: "SubagentStop", wantDelta: -1, wantOK: true},
		{event: "PreToolUse", wantDelta: 0, wantOK: false},
		{event: "Stop", wantDelta: 0, wantOK: false},
		{event: "", wantDelta: 0, wantOK: false},
	}
	for _, testCase := range cases {
		delta, ok := SubagentDeltaForHook(HookInput{HookEventName: testCase.event})
		if delta != testCase.wantDelta || ok != testCase.wantOK {
			t.Errorf("SubagentDeltaForHook(%q) = (%d, %v), want (%d, %v)", testCase.event, delta, ok, testCase.wantDelta, testCase.wantOK)
		}
	}
}
