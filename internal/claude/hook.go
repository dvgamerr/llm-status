// Package claude decodes and sanitizes Claude Code status-line and hook input.
package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/dvgamerr/llm-status/internal/model"
)

const maxHookInputBytes = 256 << 10

// HookInput models only the Claude Code hook fields this app needs to
// classify activity. Unknown fields (transcript_path, tool_input, cwd, ...)
// are intentionally accepted for forward compatibility and discarded once an
// activity state is derived; the message text itself is never persisted.
type HookInput struct {
	SessionID     string `json:"session_id"`
	HookEventName string `json:"hook_event_name"`
	Message       string `json:"message"`
	// ToolName is only present on PreToolUse and only used to pick a
	// specific visual (Typing vs Building) for the generic "a tool is
	// running" state; it is never persisted.
	ToolName string `json:"tool_name"`
}

// DecodeHook reads one size-limited hook payload and rejects trailing values.
func DecodeHook(r io.Reader) (HookInput, error) {
	var input HookInput
	data, err := io.ReadAll(io.LimitReader(r, maxHookInputBytes+1))
	if err != nil {
		return input, fmt.Errorf("read hook input: %w", err)
	}
	if len(data) > maxHookInputBytes {
		return input, fmt.Errorf("hook input exceeds %d bytes", maxHookInputBytes)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return input, errors.New("hook input is empty")
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&input); err != nil {
		return input, fmt.Errorf("decode hook JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return input, errors.New("hook input contains multiple JSON values")
		}
		return input, fmt.Errorf("decode trailing hook data: %w", err)
	}
	return input, nil
}

// ActivityForHook classifies a hook event into a dashboard activity state.
// It inspects the Notification message text only to detect a permission
// prompt (e.g. "Claude needs your permission to use Bash"); the message
// itself is discarded either way and never returned or persisted. Events
// that don't map to a meaningful state (e.g. an idle-nudge notification, or
// a subagent start/stop — see SubagentDeltaForHook) report ok=false so the
// caller leaves the last known state untouched.
func ActivityForHook(input HookInput) (state string, ok bool) {
	switch input.HookEventName {
	case "UserPromptSubmit":
		return model.ActivityThinking, true
	case "PreToolUse":
		return workingStateForTool(input.ToolName), true
	case "Stop":
		return model.ActivityIdle, true
	case "Notification":
		if strings.Contains(strings.ToLower(input.Message), "permission") {
			return model.ActivityWaitingApproval, true
		}
		return "", false
	default:
		return "", false
	}
}

// workingStateForTool picks the Typing/Building visual for a PreToolUse
// event. Bash is the one built-in tool whose animation should read as
// "running a command" rather than "editing"; everything else (Edit/Write,
// Read/Grep/Glob, WebFetch, Task, an empty/unrecognized tool_name, ...)
// falls back to Typing, the more general "doing something" visual.
func workingStateForTool(toolName string) string {
	if toolName == "Bash" {
		return model.ActivityBuilding
	}
	return model.ActivityTyping
}

// SubagentDeltaForHook reports how a hook event should adjust the running
// count of concurrent Task-tool subagents for a session: +1 when one
// starts, -1 when one stops. Every other event reports ok=false — the
// caller must not touch the stored counter for those.
func SubagentDeltaForHook(input HookInput) (delta int, ok bool) {
	switch input.HookEventName {
	case "SubagentStart":
		return 1, true
	case "SubagentStop":
		return -1, true
	default:
		return 0, false
	}
}
