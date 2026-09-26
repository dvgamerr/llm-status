// Package model defines the provider-neutral, sanitized state shared by all
// ingestion, persistence, relay, and presentation layers.
package model

import (
	"strings"
	"time"
)

// CurrentSchemaVersion is the on-disk and relay schema version.
const CurrentSchemaVersion = 1

const (
	// ProviderClaude identifies snapshots produced from Claude Code.
	ProviderClaude = "claude"
	// ProviderCodex identifies snapshots produced from Codex.
	ProviderCodex = "codex"
)

// Activity states describe what the source session is doing right now, as
// reported by Claude Code hooks. They are derived, boolean-ish signals only
// — never the hook's raw message/prompt text.
//
// ActivityWorking predates the Typing/Building split and is kept only so
// older persisted snapshots (and the statusLine-freshness fallback in
// pixelui.resolveActivity) still resolve to a valid, known state; the
// dashboard renders it identically to ActivityTyping.
const (
	ActivityWorking         = "working"
	ActivityIdle            = "idle"
	ActivityWaitingApproval = "waiting_approval"
	ActivityThinking        = "thinking"
	ActivityTyping          = "typing"
	ActivityBuilding        = "building"
	ActivitySubagentOne     = "subagent_one"
	ActivitySubagentMany    = "subagent_many"
)

// Snapshot is the deliberately small, sanitized representation persisted by
// llm-status. It must never contain prompts, transcripts, credentials, or
// arbitrary fields copied from Claude or Codex provider input.
type Snapshot struct {
	SchemaVersion int       `json:"schema_version"`
	CapturedAt    time.Time `json:"captured_at"`
	Provider      string    `json:"provider,omitempty"`
	ClientVersion string    `json:"client_version,omitempty"`
	Session       Session   `json:"session"`
	Model         Model     `json:"model"`
	// ClaudeCodeVersion is retained and populated for compatibility with older
	// snapshots/readers; provider-neutral consumers should use ClientVersion.
	ClaudeCodeVersion string     `json:"claude_code_version,omitempty"`
	Context           Context    `json:"context"`
	RateLimits        RateLimits `json:"rate_limits"`
	Cost              Cost       `json:"cost"`
	Effort            string     `json:"effort,omitempty"`
	ThinkingEnabled   *bool      `json:"thinking_enabled,omitempty"`
	Activity          Activity   `json:"activity,omitempty"`
}

// Activity is set by Claude Code hook events, independent of the statusLine
// refresh cycle, so the dashboard can animate work-in-progress vs idle vs a
// pending permission prompt.
type Activity struct {
	State     string    `json:"state,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	// Subagents is the number of Task-tool subagents currently running for
	// this session (incremented on SubagentStart, decremented on
	// SubagentStop, floored at 0). It takes rendering priority over State
	// while positive — see pixelui.resolveActivity — so the dashboard shows
	// "1 subagent"/"2+ subagents" instead of whatever the parent session's
	// own last tool event happened to be.
	Subagents int `json:"subagents,omitempty"`
}

// Session identifies one provider conversation without exposing workspace data.
type Session struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// Model contains the provider's safe model identifiers.
type Model struct {
	ID          string `json:"id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

// Context describes context-window utilization and token counts.
type Context struct {
	UsedPercentage      *float64   `json:"used_percentage,omitempty"`
	RemainingPercentage *float64   `json:"remaining_percentage,omitempty"`
	WindowSize          *int64     `json:"window_size,omitempty"`
	TotalInputTokens    *int64     `json:"total_input_tokens,omitempty"`
	TotalOutputTokens   *int64     `json:"total_output_tokens,omitempty"`
	CurrentUsage        TokenUsage `json:"current_usage"`
	Exceeds200KTokens   *bool      `json:"exceeds_200k_tokens,omitempty"`
}

// InputTokens returns the provider's total input count when available, or a
// sum of the current input/cache counters as a fallback.
func (context Context) InputTokens() *int64 {
	if context.TotalInputTokens != nil {
		return context.TotalInputTokens
	}
	parts := []*int64{
		context.CurrentUsage.InputTokens,
		context.CurrentUsage.CacheCreationInputTokens,
		context.CurrentUsage.CacheReadInputTokens,
	}
	var total int64
	found := false
	for _, part := range parts {
		if part != nil {
			total += *part
			found = true
		}
	}
	if !found {
		return nil
	}
	return &total
}

// OutputTokens returns the provider's total output count when available, or
// the current output count as a fallback.
func (context Context) OutputTokens() *int64 {
	if context.TotalOutputTokens != nil {
		return context.TotalOutputTokens
	}
	return context.CurrentUsage.OutputTokens
}

// TokenUsage contains the current provider-reported token categories.
type TokenUsage struct {
	InputTokens              *int64 `json:"input_tokens,omitempty"`
	OutputTokens             *int64 `json:"output_tokens,omitempty"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens,omitempty"`
}

// RateLimits contains the account-level five-hour and seven-day windows.
type RateLimits struct {
	FiveHour  RateWindow `json:"five_hour"`
	SevenDay  RateWindow `json:"seven_day"`
	Plan      string     `json:"plan,omitempty"`
	Unlimited *bool      `json:"unlimited,omitempty"`
}

// RateWindow describes one optional quota percentage and reset time.
type RateWindow struct {
	UsedPercentage *float64 `json:"used_percentage,omitempty"`
	ResetsAt       *int64   `json:"resets_at,omitempty"`
}

// Cost contains optional aggregate cost, timing, and line-change metrics.
type Cost struct {
	TotalCostUSD       *float64 `json:"total_cost_usd,omitempty"`
	TotalDurationMS    *int64   `json:"total_duration_ms,omitempty"`
	TotalAPIDurationMS *int64   `json:"total_api_duration_ms,omitempty"`
	TotalLinesAdded    *int64   `json:"total_lines_added,omitempty"`
	TotalLinesRemoved  *int64   `json:"total_lines_removed,omitempty"`
}

// CanonicalProvider normalizes supported provider aliases. Older imported
// snapshots may identify Codex as "openai", so that alias remains readable.
func CanonicalProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case ProviderClaude:
		return ProviderClaude
	case ProviderCodex, "openai":
		return ProviderCodex
	default:
		return ""
	}
}

// SnapshotIsNewer orders snapshots consistently across relay and UI code.
func SnapshotIsNewer(candidate, current Snapshot) bool {
	if !candidate.CapturedAt.Equal(current.CapturedAt) {
		return candidate.CapturedAt.After(current.CapturedAt)
	}
	if !candidate.Activity.UpdatedAt.Equal(current.Activity.UpdatedAt) {
		return candidate.Activity.UpdatedAt.After(current.Activity.UpdatedAt)
	}
	return candidate.Session.ID > current.Session.ID
}

// TodayTokenTotals sums input and output tokens across every snapshot —
// Claude and Codex alike — captured on now's local calendar day. Each stored
// snapshot already carries its own session's cumulative totals, so this is
// a sum across sessions/providers, not a running per-turn accumulator; a
// Codex snapshot in particular only ever reflects its latest reported turn
// (see internal/codex), so a long Codex session that keeps overwriting its
// one stored file under-counts relative to a Claude session of equivalent
// length.
func TodayTokenTotals(snapshots []Snapshot, now time.Time) (input, output int64) {
	year, month, day := now.Local().Date()
	for _, snapshot := range snapshots {
		capturedYear, capturedMonth, capturedDay := snapshot.CapturedAt.Local().Date()
		if capturedYear != year || capturedMonth != month || capturedDay != day {
			continue
		}
		if value := snapshot.Context.InputTokens(); value != nil {
			input += *value
		}
		if value := snapshot.Context.OutputTokens(); value != nil {
			output += *value
		}
	}
	return input, output
}
