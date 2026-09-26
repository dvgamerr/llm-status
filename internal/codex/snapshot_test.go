package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dvgamerr/llm-status/internal/model"
)

func TestDecodeNotificationAllowlist(t *testing.T) {
	raw := `{
      "type":"agent-turn-complete",
      "thread-id":"019fb916-7c07-70b1-9462-2802d57220a2",
      "turn-id":"turn-1",
      "input-messages":["do not persist"],
      "last-assistant-message":"also private"
    }`
	notification, err := DecodeNotification(raw)
	if err != nil {
		t.Fatalf("DecodeNotification() error = %v", err)
	}
	if notification.Type != "agent-turn-complete" || notification.ThreadID != "019fb916-7c07-70b1-9462-2802d57220a2" {
		t.Fatalf("notification = %+v", notification)
	}
	encoded, err := json.Marshal(notification)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"do not persist", "also private", "input-messages"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("allowlisted notification contains %q: %s", forbidden, encoded)
		}
	}
}

func TestDecodeNotificationRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: " ", want: "empty"},
		{name: "invalid JSON", raw: "{", want: "decode"},
		{name: "multiple", raw: `{"thread-id":"x"} {}`, want: "multiple"},
		{name: "missing thread", raw: `{}`, want: "thread-id"},
		{name: "unsafe thread", raw: `{"thread-id":"../../secret"}`, want: "thread-id"},
		{name: "unsupported type", raw: `{"type":"other","thread-id":"thread_123"}`, want: "notification type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeNotification(tt.raw)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("DecodeNotification() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestSnapshotFromNotificationReadsOnlyUsageMetadata(t *testing.T) {
	home := t.TempDir()
	threadID := "019fb916-7c07-70b1-9462-2802d57220a2"
	rollout := filepath.Join(home, "sessions", "2026", "08", "01", "rollout-"+threadID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o700); err != nil {
		t.Fatal(err)
	}
	tooLongPrivateLine := `{"type":"response_item","payload":{"message":"` + strings.Repeat("private", maxRolloutLineBytes) + `"}}` + "\n"
	content := strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"` + threadID + `","cli_version":"0.146.0","cwd":"C:/private/project"}}`,
		`{"type":"response_item","payload":{"type":"message","text":"private prompt"}}`,
		`{"type":"turn_context","payload":{"model":"gpt-5.6-sol","effort":"xhigh","cwd":"C:/private/project"}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":50000,"cached_input_tokens":32000,"output_tokens":2000,"reasoning_output_tokens":1000,"total_tokens":52000},"model_context_window":200000},"rate_limits":{"primary":{"used_percent":51,"window_minutes":300,"resets_at":1785500280},"secondary":{"used_percent":34,"window_minutes":10080,"resets_at":1785675600}}}}`,
	}, "\n") + "\n" + tooLongPrivateLine +
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":60000,"cached_input_tokens":40000,"output_tokens":4000,"total_tokens":64000},"model_context_window":200000},"rate_limits":{"primary":{"used_percent":52,"window_minutes":300,"resets_at":1785500281},"secondary":{"used_percent":35,"window_minutes":10080,"resets_at":1785675601},"credits":{"unlimited":true},"plan_type":"enterprise_cbp_usage_based"}}}` + "\n"
	if err := os.WriteFile(rollout, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.FixedZone("ICT", 7*60*60))
	snapshot, err := SnapshotFromNotification(Notification{ThreadID: threadID}, home, now)
	if err != nil {
		t.Fatalf("SnapshotFromNotification() error = %v", err)
	}
	if snapshot.Provider != "codex" || snapshot.Model.ID != "gpt-5.6-sol" || snapshot.Effort != "xhigh" {
		t.Fatalf("identity metadata = %+v", snapshot)
	}
	if snapshot.Context.UsedPercentage == nil || *snapshot.Context.UsedPercentage != 32 {
		t.Fatalf("context used = %v, want 32", snapshot.Context.UsedPercentage)
	}
	if snapshot.Context.TotalInputTokens == nil || *snapshot.Context.TotalInputTokens != 60000 || snapshot.Context.TotalOutputTokens == nil || *snapshot.Context.TotalOutputTokens != 4000 {
		t.Fatalf("token usage = %+v", snapshot.Context)
	}
	if snapshot.RateLimits.FiveHour.UsedPercentage == nil || *snapshot.RateLimits.FiveHour.UsedPercentage != 52 || snapshot.RateLimits.SevenDay.UsedPercentage == nil || *snapshot.RateLimits.SevenDay.UsedPercentage != 35 {
		t.Fatalf("rate limits = %+v", snapshot.RateLimits)
	}
	if snapshot.RateLimits.Unlimited == nil || !*snapshot.RateLimits.Unlimited || snapshot.RateLimits.Plan != "enterprise_cbp_usage_based" {
		t.Fatalf("account metadata = %+v", snapshot.RateLimits)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private prompt", "private project", "C:/private"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("snapshot contains private rollout data %q: %s", forbidden, encoded)
		}
	}
}

func TestSnapshotFromNotificationHandlesMissingAndReorderedWindows(t *testing.T) {
	home := t.TempDir()
	threadID := "thread_123"
	rollout := filepath.Join(home, "sessions", "rollout-"+threadID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"output_tokens":5},"model_context_window":0},"rate_limits":{"primary":{"used_percent":150,"window_minutes":10080},"secondary":{"used_percent":-5,"window_minutes":300}}}}`
	if err := os.WriteFile(rollout, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := SnapshotFromNotification(Notification{ThreadID: threadID}, home, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Context.UsedPercentage != nil {
		t.Fatalf("zero context window produced percentage %v", snapshot.Context.UsedPercentage)
	}
	if got := *snapshot.RateLimits.SevenDay.UsedPercentage; got != 100 {
		t.Fatalf("7-day percentage = %v", got)
	}
	if got := *snapshot.RateLimits.FiveHour.UsedPercentage; got != 0 {
		t.Fatalf("5-hour percentage = %v", got)
	}
}

func TestSnapshotFromNotificationReportsMissingRollout(t *testing.T) {
	_, err := SnapshotFromNotification(Notification{ThreadID: "thread-404"}, t.TempDir(), time.Now())
	if err == nil || !strings.Contains(err.Error(), "no Codex rollout") {
		t.Fatalf("error = %v", err)
	}
}

func TestSnapshotFromNotificationDoesNotMatchThreadSubstring(t *testing.T) {
	home := t.TempDir()
	sessions := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessions, "rollout-prefix-thread_123-suffix.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := SnapshotFromNotification(Notification{ThreadID: "thread_123"}, home, time.Now())
	if err == nil || !strings.Contains(err.Error(), "no Codex rollout") {
		t.Fatalf("error = %v", err)
	}
}

func TestDefaultHomeHonorsEnvironment(t *testing.T) {
	want := filepath.Join(t.TempDir(), "codex-home")
	t.Setenv("CODEX_HOME", want)
	got, err := DefaultHome()
	if err != nil || got != filepath.Clean(want) {
		t.Fatalf("DefaultHome() = %q, %v; want %q", got, err, want)
	}
}

func TestDefaultHomeFallsBackToUserHomeDir(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no user home directory available in this environment")
	}
	got, err := DefaultHome()
	if err != nil {
		t.Fatalf("DefaultHome() error = %v", err)
	}
	want := filepath.Join(home, ".codex")
	if got != want {
		t.Fatalf("DefaultHome() = %q, want %q", got, want)
	}
}

func TestDefaultHomeReportsUserHomeDirError(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", "")
	} else {
		t.Setenv("HOME", "")
	}
	_, err := DefaultHome()
	if err == nil || !strings.Contains(err.Error(), "find Codex home") {
		t.Fatalf("DefaultHome() error = %v, want find-Codex-home error", err)
	}
}

func TestFindRolloutSelectsMostRecentlyModified(t *testing.T) {
	home := t.TempDir()
	threadID := "thread_recent"
	sessions := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(sessions, "rollout-old-"+threadID+".jsonl")
	newer := filepath.Join(sessions, "rollout-new-"+threadID+".jsonl")
	if err := os.WriteFile(older, []byte(`{"type":"session_meta","payload":{"id":"old"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte(`{"type":"session_meta","payload":{"id":"new"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(older, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	got, err := findRollout(home, threadID)
	if err != nil {
		t.Fatalf("findRollout() error = %v", err)
	}
	if got != newer {
		t.Fatalf("findRollout() = %q, want %q", got, newer)
	}
}

func TestReadRolloutReportsOpenError(t *testing.T) {
	_, err := readRollout(filepath.Join(t.TempDir(), "missing.jsonl"))
	if err == nil || !strings.Contains(err.Error(), "open Codex rollout") {
		t.Fatalf("readRollout() error = %v, want open error", err)
	}
}

func TestReadRolloutReportsReadError(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "adir")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := readRollout(sub)
	if err == nil || !strings.Contains(err.Error(), "read Codex rollout") {
		t.Fatalf("readRollout() error = %v, want read error", err)
	}
}

func TestReadRolloutSkipsUnknownAndMalformedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	content := strings.Join([]string{
		`{"type":123}`,
		`{"type":"event_msg","payload":{"type":"other"}}`,
		`{"type":"unknown_kind","payload":{"type":"whatever"}}`,
		`{"type":"session_meta","payload":{"id":"sess-1","cli_version":"1.2.3"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	values, err := readRollout(path)
	if err != nil {
		t.Fatalf("readRollout() error = %v", err)
	}
	if values.SessionID != "sess-1" || values.ClientVersion != "1.2.3" {
		t.Fatalf("readRollout() values = %+v, want session metadata preserved despite malformed/unknown lines", values)
	}
}

func TestDecodeNotificationRejectsOversizedInput(t *testing.T) {
	huge := strings.Repeat("a", maxNotificationBytes+1)
	_, err := DecodeNotification(huge)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("DecodeNotification() error = %v, want exceeds-bytes error", err)
	}
}

func TestDecodeNotificationRejectsMalformedTrailingData(t *testing.T) {
	raw := `{"type":"agent-turn-complete","thread-id":"thread_123"} {not-json`
	_, err := DecodeNotification(raw)
	if err == nil || !strings.Contains(err.Error(), "trailing codex notification data") {
		t.Fatalf("DecodeNotification() error = %v, want trailing-data error", err)
	}
}

func TestSnapshotFromNotificationRejectsInvalidThreadID(t *testing.T) {
	_, err := SnapshotFromNotification(Notification{ThreadID: "../evil"}, t.TempDir(), time.Now())
	if err == nil || !strings.Contains(err.Error(), "thread-id") {
		t.Fatalf("SnapshotFromNotification() error = %v, want thread-id error", err)
	}
}

func TestSnapshotFromNotificationReportsReadRolloutError(t *testing.T) {
	home := t.TempDir()
	threadID := "thread_broken"
	sessions := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(sessions, "rollout-"+threadID+".jsonl")
	target := filepath.Join(sessions, "missing-target.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported in this environment: %v", err)
	}
	_, err := SnapshotFromNotification(Notification{ThreadID: threadID}, home, time.Now())
	if err == nil {
		t.Fatal("SnapshotFromNotification() error = nil, want an error from the broken rollout symlink")
	}
}

func TestAssignRateWindowFallsBackByPrimaryFlag(t *testing.T) {
	var limits model.RateLimits
	oddMinutes := int64(1000)
	primaryUsed := 42.0
	assignRateWindow(&limits, &rateWindow{UsedPercent: &primaryUsed, WindowMinutes: &oddMinutes}, true)
	if limits.FiveHour.UsedPercentage == nil || *limits.FiveHour.UsedPercentage != 42 {
		t.Fatalf("primary fallback = %+v, want FiveHour set", limits.FiveHour)
	}
	secondaryUsed := 24.0
	assignRateWindow(&limits, &rateWindow{UsedPercent: &secondaryUsed, WindowMinutes: &oddMinutes}, false)
	if limits.SevenDay.UsedPercentage == nil || *limits.SevenDay.UsedPercentage != 24 {
		t.Fatalf("secondary fallback = %+v, want SevenDay set", limits.SevenDay)
	}
}

func TestAssignRateWindowIgnoresNilInput(t *testing.T) {
	var limits model.RateLimits
	assignRateWindow(&limits, nil, true)
	assignRateWindow(&limits, nil, false)
	if limits.FiveHour.UsedPercentage != nil || limits.SevenDay.UsedPercentage != nil {
		t.Fatalf("assignRateWindow(nil) mutated limits = %+v", limits)
	}
}

func TestSumReturnsNilWithoutValidValues(t *testing.T) {
	if got := sum(); got != nil {
		t.Fatalf("sum() = %v, want nil", got)
	}
	if got := sum(nil, nil); got != nil {
		t.Fatalf("sum(nil, nil) = %v, want nil", got)
	}
	negative := int64(-5)
	if got := sum(&negative); got != nil {
		t.Fatalf("sum(negative) = %v, want nil", got)
	}
}
