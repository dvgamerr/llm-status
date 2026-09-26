package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dvgamerr/llm-status/internal/model"
	"github.com/dvgamerr/llm-status/internal/sanitize"
)

const (
	maxInputBytes     = 4 << 20
	maxSessionIDRunes = 256
)

// Input models only the official statusLine fields used by this application.
// Unknown fields are intentionally accepted for forward compatibility and are
// discarded when a Snapshot is created.
type Input struct {
	SessionID     string         `json:"session_id"`
	SessionName   string         `json:"session_name"`
	Model         Model          `json:"model"`
	Version       string         `json:"version"`
	Cost          *Cost          `json:"cost"`
	ContextWindow *ContextWindow `json:"context_window"`
	RateLimits    *RateLimits    `json:"rate_limits"`
	Exceeds200K   *bool          `json:"exceeds_200k_tokens"`
	Effort        *Effort        `json:"effort"`
	Thinking      *Thinking      `json:"thinking"`
}

// Model is the model identity subset accepted from statusLine input.
type Model struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// Cost is the optional aggregate metrics subset accepted from statusLine input.
type Cost struct {
	TotalCostUSD       *float64 `json:"total_cost_usd"`
	TotalDurationMS    *int64   `json:"total_duration_ms"`
	TotalAPIDurationMS *int64   `json:"total_api_duration_ms"`
	TotalLinesAdded    *int64   `json:"total_lines_added"`
	TotalLinesRemoved  *int64   `json:"total_lines_removed"`
}

// ContextWindow is the optional context usage subset accepted from statusLine.
type ContextWindow struct {
	TotalInputTokens    *int64      `json:"total_input_tokens"`
	TotalOutputTokens   *int64      `json:"total_output_tokens"`
	ContextWindowSize   *int64      `json:"context_window_size"`
	UsedPercentage      *float64    `json:"used_percentage"`
	RemainingPercentage *float64    `json:"remaining_percentage"`
	CurrentUsage        *TokenUsage `json:"current_usage"`
}

// TokenUsage is the provider's current token breakdown.
type TokenUsage struct {
	InputTokens              *int64 `json:"input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
}

// RateLimits contains optional five-hour and seven-day quota windows.
type RateLimits struct {
	FiveHour *RateWindow `json:"five_hour"`
	SevenDay *RateWindow `json:"seven_day"`
}

// RateWindow contains one optional quota percentage and reset time.
type RateWindow struct {
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       *int64   `json:"resets_at"`
}

// Effort contains the provider-reported reasoning level.
type Effort struct {
	Level string `json:"level"`
}

// Thinking reports whether extended thinking is enabled.
type Thinking struct {
	Enabled *bool `json:"enabled"`
}

// Decode reads one size-limited statusLine payload and rejects trailing values.
func Decode(r io.Reader) (Input, error) {
	var input Input
	data, err := io.ReadAll(io.LimitReader(r, maxInputBytes+1))
	if err != nil {
		return input, fmt.Errorf("read statusLine input: %w", err)
	}
	if len(data) > maxInputBytes {
		return input, fmt.Errorf("statusLine input exceeds %d bytes", maxInputBytes)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return input, errors.New("statusLine input is empty")
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&input); err != nil {
		return input, fmt.Errorf("decode statusLine JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return input, errors.New("statusLine input contains multiple JSON values")
		}
		return input, fmt.Errorf("decode trailing statusLine data: %w", err)
	}
	return input, nil
}

// CleanSessionID sanitizes a session identifier the same way statusLine
// input is sanitized, so hook events and statusLine events agree on which
// session a snapshot belongs to.
func CleanSessionID(value string) string {
	sessionID := sanitize.Text(value, maxSessionIDRunes)
	if sessionID == "" {
		sessionID = "unknown"
	}
	return sessionID
}

// ToSnapshot converts provider input into the allowlisted persisted schema.
func ToSnapshot(input Input, now time.Time) model.Snapshot {
	sessionID := CleanSessionID(input.SessionID)

	snapshot := model.Snapshot{
		SchemaVersion:     model.CurrentSchemaVersion,
		CapturedAt:        now.UTC(),
		Provider:          model.ProviderClaude,
		ClientVersion:     sanitize.Text(input.Version, 80),
		Session:           model.Session{ID: sessionID, Name: sanitize.Text(input.SessionName, 120)},
		Model:             model.Model{ID: sanitize.Text(input.Model.ID, 120), DisplayName: sanitize.Text(input.Model.DisplayName, 120)},
		ClaudeCodeVersion: sanitize.Text(input.Version, 80),
		Context:           model.Context{Exceeds200KTokens: sanitize.Bool(input.Exceeds200K)},
	}

	if input.ContextWindow != nil {
		context := input.ContextWindow
		snapshot.Context.UsedPercentage = sanitize.Percentage(context.UsedPercentage)
		snapshot.Context.RemainingPercentage = sanitize.Percentage(context.RemainingPercentage)
		snapshot.Context.WindowSize = sanitize.NonNegativeInt64(context.ContextWindowSize)
		snapshot.Context.TotalInputTokens = sanitize.NonNegativeInt64(context.TotalInputTokens)
		snapshot.Context.TotalOutputTokens = sanitize.NonNegativeInt64(context.TotalOutputTokens)
		if context.CurrentUsage != nil {
			snapshot.Context.CurrentUsage = model.TokenUsage{
				InputTokens:              sanitize.NonNegativeInt64(context.CurrentUsage.InputTokens),
				OutputTokens:             sanitize.NonNegativeInt64(context.CurrentUsage.OutputTokens),
				CacheCreationInputTokens: sanitize.NonNegativeInt64(context.CurrentUsage.CacheCreationInputTokens),
				CacheReadInputTokens:     sanitize.NonNegativeInt64(context.CurrentUsage.CacheReadInputTokens),
			}
		}
	}

	if input.RateLimits != nil {
		snapshot.RateLimits.FiveHour = convertRateWindow(input.RateLimits.FiveHour)
		snapshot.RateLimits.SevenDay = convertRateWindow(input.RateLimits.SevenDay)
	}

	if input.Cost != nil {
		snapshot.Cost = model.Cost{
			TotalCostUSD:       sanitize.NonNegativeFloat64(input.Cost.TotalCostUSD),
			TotalDurationMS:    sanitize.NonNegativeInt64(input.Cost.TotalDurationMS),
			TotalAPIDurationMS: sanitize.NonNegativeInt64(input.Cost.TotalAPIDurationMS),
			TotalLinesAdded:    sanitize.NonNegativeInt64(input.Cost.TotalLinesAdded),
			TotalLinesRemoved:  sanitize.NonNegativeInt64(input.Cost.TotalLinesRemoved),
		}
	}
	if input.Effort != nil {
		snapshot.Effort = sanitize.Text(input.Effort.Level, 24)
	}
	if input.Thinking != nil {
		snapshot.ThinkingEnabled = sanitize.Bool(input.Thinking.Enabled)
	}

	return snapshot
}

// StatusLine formats the short line returned to Claude Code.
func StatusLine(snapshot model.Snapshot) string {
	name := snapshot.Model.DisplayName
	if name == "" {
		name = snapshot.Model.ID
	}
	if name == "" {
		name = "Claude"
	}

	parts := make([]string, 0, 3)
	if p := snapshot.RateLimits.FiveHour.UsedPercentage; p != nil {
		parts = append(parts, fmt.Sprintf("5h %.0f%%", *p))
	}
	if p := snapshot.RateLimits.SevenDay.UsedPercentage; p != nil {
		parts = append(parts, fmt.Sprintf("7d %.0f%%", *p))
	}
	if p := snapshot.Context.UsedPercentage; p != nil {
		parts = append(parts, fmt.Sprintf("ctx %.0f%%", *p))
	}
	if len(parts) == 0 {
		parts = append(parts, "waiting for first response")
	}
	return fmt.Sprintf("[%s] %s", name, strings.Join(parts, " · "))
}

func convertRateWindow(input *RateWindow) model.RateWindow {
	if input == nil {
		return model.RateWindow{}
	}
	return model.RateWindow{
		UsedPercentage: sanitize.Percentage(input.UsedPercentage),
		ResetsAt:       sanitize.PositiveInt64(input.ResetsAt),
	}
}
