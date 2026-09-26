package dashboard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	statusmodel "github.com/dvgamerr/llm-status/internal/model"
	"github.com/dvgamerr/llm-status/internal/systeminfo"
)

type fakeLoader struct {
	snapshots []statusmodel.Snapshot
	err       error
}

func (f fakeLoader) LoadAll() ([]statusmodel.Snapshot, error) { return f.snapshots, f.err }

type fakeMetrics struct{ stats systeminfo.Stats }

func (f fakeMetrics) Read() systeminfo.Stats { return f.stats }

func TestDashboardViewAndSessionSelection(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.Local)
	pct5, pct7, contextPct := 51.0, 34.0, 72.0
	window := int64(200000)
	input := int64(140000)
	output := int64(4000)
	duration := int64(6_138_000)
	cost := 1.28
	added, removed := int64(186), int64(42)
	reset := now.Add(2 * time.Hour).Unix()
	snapshots := []statusmodel.Snapshot{
		{
			SchemaVersion: modelSchema(), CapturedAt: now.Add(-4 * time.Second),
			Session: statusmodel.Session{ID: "abc1234567890123", Name: "primary"},
			Model:   statusmodel.Model{DisplayName: "Opus"},
			RateLimits: statusmodel.RateLimits{
				FiveHour: statusmodel.RateWindow{UsedPercentage: &pct5, ResetsAt: &reset},
				SevenDay: statusmodel.RateWindow{UsedPercentage: &pct7, ResetsAt: &reset},
			},
			Context: statusmodel.Context{UsedPercentage: &contextPct, WindowSize: &window, TotalInputTokens: &input, TotalOutputTokens: &output},
			Cost:    statusmodel.Cost{TotalDurationMS: &duration, TotalCostUSD: &cost, TotalLinesAdded: &added, TotalLinesRemoved: &removed},
		},
		{SchemaVersion: modelSchema(), CapturedAt: now.Add(-time.Minute), Session: statusmodel.Session{ID: "second"}, Model: statusmodel.Model{DisplayName: "Sonnet"}},
	}
	cpu, temp := 18.0, 52.0
	used, total := uint64(1<<30), uint64(4<<30)
	uptime := 102*time.Minute + 18*time.Second
	m := NewModel(fakeLoader{snapshots: snapshots}, fakeMetrics{systeminfo.Stats{CPUPercent: &cpu, TemperatureC: &temp, MemoryUsedBytes: &used, MemoryTotalBytes: &total, Uptime: &uptime}}, Config{})
	m.now = func() time.Time { return now }
	updated, _ := m.Update(dataMsg{snapshots: snapshots, stats: m.metrics.Read()})
	m = updated.(Model)
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 66, Height: 20})
	m = updated.(Model)

	view := m.View()
	for _, want := range []string{"CLAUDE STATUS", "LIVE", "OPUS", "5-HOUR LIMIT", "7-DAY LIMIT", "51%", "34%", "CONTEXT", "72%", "144k / 200k", "↑ 140k", "↓ 4k", "$1.28", "+186  -42", "CPU 18%", "Temp 52°C", "Up 1h42m", "primary"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() does not contain %q:\n%s", want, view)
		}
	}
	if got := lipgloss.Width(view); got != 66 {
		t.Fatalf("Pi dashboard width = %d, want 66:\n%s", got, view)
	}
	if got := lipgloss.Height(view); got != 20 {
		t.Fatalf("Pi dashboard height = %d, want 20:\n%s", got, view)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = updated.(Model)
	if !strings.Contains(m.View(), "SESSIONS") || !strings.Contains(m.View(), "Sonnet") {
		t.Fatalf("sessions view is incomplete:\n%s", m.View())
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.selectedID != "second" || m.showSessions {
		t.Fatalf("session selection failed: selected=%q show=%v", m.selectedID, m.showSessions)
	}
	if !m.selectionPinned {
		t.Fatal("manual session selection did not pin the session")
	}
}

func TestDashboardAutoFollowsNewestSnapshot(t *testing.T) {
	now := time.Now()
	older := statusmodel.Snapshot{SchemaVersion: modelSchema(), CapturedAt: now.Add(-time.Minute), Session: statusmodel.Session{ID: "older"}}
	newer := statusmodel.Snapshot{SchemaVersion: modelSchema(), CapturedAt: now, Session: statusmodel.Session{ID: "newer"}}
	m := NewModel(fakeLoader{}, fakeMetrics{}, Config{})
	updated, _ := m.Update(dataMsg{snapshots: []statusmodel.Snapshot{older}})
	m = updated.(Model)
	if m.selectedID != "older" {
		t.Fatalf("initial auto selection = %q", m.selectedID)
	}
	updated, _ = m.Update(dataMsg{snapshots: []statusmodel.Snapshot{newer, older}})
	m = updated.(Model)
	if m.selectedID != "newer" {
		t.Fatalf("auto selection did not follow newest snapshot: %q", m.selectedID)
	}

	m.selectionPinned = true
	m.selectedID = "older"
	updated, _ = m.Update(dataMsg{snapshots: []statusmodel.Snapshot{newer, older}})
	m = updated.(Model)
	if m.selectedID != "older" {
		t.Fatalf("pinned selection moved to %q", m.selectedID)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = updated.(Model)
	if m.selectionPinned || m.selectedID != "newer" {
		t.Fatalf("auto key did not resume newest selection: pinned=%v selected=%q", m.selectionPinned, m.selectedID)
	}
}

func TestDashboardMarksOldSnapshotStale(t *testing.T) {
	now := time.Now()
	snapshot := statusmodel.Snapshot{SchemaVersion: modelSchema(), CapturedAt: now.Add(-time.Minute), Session: statusmodel.Session{ID: "old"}}
	m := NewModel(fakeLoader{}, fakeMetrics{}, Config{StaleAfter: 15 * time.Second})
	m.now = func() time.Time { return now }
	updated, _ := m.Update(dataMsg{snapshots: []statusmodel.Snapshot{snapshot}})
	m = updated.(Model)
	if !strings.Contains(m.View(), "STALE") {
		t.Fatalf("old snapshot was not marked stale:\n%s", m.View())
	}
}

func TestDashboardLabelsCodexSnapshot(t *testing.T) {
	now := time.Now()
	snapshot := statusmodel.Snapshot{
		SchemaVersion: modelSchema(),
		CapturedAt:    now,
		Provider:      "codex",
		ClientVersion: "0.146.0",
		Session:       statusmodel.Session{ID: "codex-thread"},
		Model:         statusmodel.Model{ID: "gpt-5.6-sol"},
	}
	m := NewModel(fakeLoader{}, fakeMetrics{}, Config{})
	m.now = func() time.Time { return now }
	updated, _ := m.Update(dataMsg{snapshots: []statusmodel.Snapshot{snapshot}})
	m = updated.(Model)
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 66, Height: 20})
	m = updated.(Model)
	view := m.View()
	for _, want := range []string{"CODEX STATUS", "GPT-5.6-SOL", "CODEX 0.146.0"} {
		if !strings.Contains(view, want) {
			t.Fatalf("Codex dashboard does not contain %q:\n%s", want, view)
		}
	}
}

func TestDashboardDisplaysPartialLoadWarning(t *testing.T) {
	now := time.Now()
	snapshot := statusmodel.Snapshot{SchemaVersion: modelSchema(), CapturedAt: now, Session: statusmodel.Session{ID: "valid"}}
	m := NewModel(fakeLoader{}, fakeMetrics{}, Config{})
	m.now = func() time.Time { return now }
	updated, _ := m.Update(dataMsg{snapshots: []statusmodel.Snapshot{snapshot}, err: errors.New("ignored 1 invalid session snapshot")})
	m = updated.(Model)
	view := m.View()
	if !strings.Contains(view, "ignored 1 invalid session snapshot") || !strings.Contains(view, "valid") {
		t.Fatalf("partial load warning or valid session missing:\n%s", view)
	}
}

func TestDashboardFitsNarrowTerminal(t *testing.T) {
	now := time.Now()
	contextPct, fivePct, sevenPct := 18.0, 27.0, 44.0
	input, cacheCreate, cacheRead, output := int64(500), int64(1000), int64(1500), int64(750)
	snapshot := statusmodel.Snapshot{
		SchemaVersion: modelSchema(),
		CapturedAt:    now,
		Session:       statusmodel.Session{ID: "narrow"},
		Context: statusmodel.Context{
			UsedPercentage: &contextPct,
			CurrentUsage: statusmodel.TokenUsage{
				InputTokens:              &input,
				CacheCreationInputTokens: &cacheCreate,
				CacheReadInputTokens:     &cacheRead,
				OutputTokens:             &output,
			},
		},
		RateLimits: statusmodel.RateLimits{
			FiveHour: statusmodel.RateWindow{UsedPercentage: &fivePct},
			SevenDay: statusmodel.RateWindow{UsedPercentage: &sevenPct},
		},
	}
	m := NewModel(fakeLoader{}, fakeMetrics{}, Config{})
	m.now = func() time.Time { return now }
	updated, _ := m.Update(dataMsg{snapshots: []statusmodel.Snapshot{snapshot}})
	m = updated.(Model)
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
	m = updated.(Model)
	view := m.View()
	if got := lipgloss.Width(view); got != 40 {
		t.Fatalf("dashboard width = %d, want 40:\n%s", got, view)
	}
	if got := lipgloss.Height(view); got != 20 {
		t.Fatalf("dashboard height = %d, want 20:\n%s", got, view)
	}
	for _, want := range []string{"CLAUDE STATUS", "CTX", "18%", "5H", "27%", "7D", "44%", "↑ 3k input", "↓ 750 output"} {
		if !strings.Contains(view, want) {
			t.Fatalf("compact dashboard does not contain %q:\n%s", want, view)
		}
	}
}

func TestSessionPickerPaginatesWithinPiDisplay(t *testing.T) {
	now := time.Now()
	snapshots := make([]statusmodel.Snapshot, 30)
	for i := range snapshots {
		snapshots[i] = statusmodel.Snapshot{
			SchemaVersion: modelSchema(),
			CapturedAt:    now.Add(-time.Duration(i) * time.Minute),
			Session:       statusmodel.Session{ID: fmt.Sprintf("session-%02d", i)},
		}
	}
	m := NewModel(fakeLoader{}, fakeMetrics{}, Config{})
	m.now = func() time.Time { return now }
	updated, _ := m.Update(dataMsg{snapshots: snapshots})
	m = updated.(Model)
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 66, Height: 20})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = updated.(Model)

	view := m.View()
	if got := lipgloss.Width(view); got != 66 {
		t.Fatalf("session picker width = %d, want 66:\n%s", got, view)
	}
	if got := lipgloss.Height(view); got != 20 {
		t.Fatalf("session picker height = %d, want 20:\n%s", got, view)
	}
	if !strings.Contains(view, "1–14 OF 30") {
		t.Fatalf("session pagination summary missing:\n%s", view)
	}
}

func TestWaitingFrameFillsPiDisplay(t *testing.T) {
	m := NewModel(fakeLoader{}, fakeMetrics{}, Config{})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 66, Height: 20})
	m = updated.(Model)
	view := m.View()
	if got := lipgloss.Width(view); got != 66 {
		t.Fatalf("waiting frame width = %d, want 66:\n%s", got, view)
	}
	if got := lipgloss.Height(view); got != 20 {
		t.Fatalf("waiting frame height = %d, want 20:\n%s", got, view)
	}
	for _, want := range []string{"WAITING FOR DATA", "STATUSLINE COMMAND", "PI HEALTH", "[r] Refresh"} {
		if !strings.Contains(view, want) {
			t.Fatalf("waiting frame does not contain %q:\n%s", want, view)
		}
	}
}

func TestClearTerminalWritesEraseBeforeRenderSequence(t *testing.T) {
	var output bytes.Buffer
	if err := clearTerminal(&output); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != clearScreenSequence {
		t.Fatalf("clearTerminal() = %q, want %q", got, clearScreenSequence)
	}
}

func TestModelCommandsLoadDataAndTick(t *testing.T) {
	want := []statusmodel.Snapshot{{SchemaVersion: modelSchema(), CapturedAt: time.Now(), Session: statusmodel.Session{ID: "loaded"}}}
	m := NewModel(fakeLoader{snapshots: want, err: errors.New("partial")}, fakeMetrics{}, Config{RefreshInterval: time.Millisecond})
	message := m.loadData()()
	loaded, ok := message.(dataMsg)
	if !ok || len(loaded.snapshots) != 1 || loaded.err == nil {
		t.Fatalf("loadData message = %#v", message)
	}
	if _, ok := m.nextTick()().(tickMsg); !ok {
		t.Fatalf("nextTick returned unexpected message")
	}
	if m.Init() == nil {
		t.Fatal("Init returned nil command")
	}
}

func TestRunHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	err := Run(ctx, strings.NewReader(""), &output, fakeLoader{}, fakeMetrics{}, Config{Inline: true})
	if err == nil {
		t.Fatal("Run unexpectedly succeeded with canceled context")
	}
	if !strings.HasPrefix(output.String(), clearScreenSequence) {
		t.Fatalf("output did not start with terminal clear: %q", output.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestClearTerminalPropagatesWriteError(t *testing.T) {
	err := clearTerminal(failingWriter{})
	if err == nil {
		t.Fatal("clearTerminal() succeeded with a failing writer, want error")
	}
	if !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("clearTerminal() error = %v, want wrapped write failed", err)
	}
}

func TestRunPropagatesClearTerminalError(t *testing.T) {
	err := Run(context.Background(), strings.NewReader(""), failingWriter{}, fakeLoader{}, fakeMetrics{}, Config{})
	if err == nil {
		t.Fatal("Run() succeeded despite a failing output writer, want error")
	}
	if !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("Run() error = %v, want wrapped write failed", err)
	}
}

func TestRunUsesAltScreenWhenNotInline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	err := Run(ctx, strings.NewReader(""), &output, fakeLoader{}, fakeMetrics{}, Config{Inline: false})
	if err == nil {
		t.Fatal("Run() unexpectedly succeeded with a canceled context")
	}
	if !strings.HasPrefix(output.String(), clearScreenSequence) {
		t.Fatalf("output did not start with terminal clear: %q", output.String())
	}
}

func TestUpdateTickReloadsDataAndReschedules(t *testing.T) {
	m := NewModel(fakeLoader{}, fakeMetrics{}, Config{RefreshInterval: time.Millisecond})
	updated, cmd := m.Update(tickMsg(time.Now()))
	if _, ok := updated.(Model); !ok {
		t.Fatalf("Update(tickMsg) returned %T, want Model", updated)
	}
	if cmd == nil {
		t.Fatal("Update(tickMsg) returned a nil command, want batched reload+tick")
	}
}

func TestUpdateKeyQuitsOnQOrCtrlC(t *testing.T) {
	m := NewModel(fakeLoader{}, fakeMetrics{}, Config{})
	for _, key := range []string{"q", "ctrl+c"} {
		_, cmd := m.updateKey(key)
		if cmd == nil {
			t.Fatalf("updateKey(%q) returned a nil command, want quit", key)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("updateKey(%q) command produced %T, want tea.QuitMsg", key, cmd())
		}
	}
}

func TestUpdateKeyEscClosesSessionsView(t *testing.T) {
	m := NewModel(fakeLoader{}, fakeMetrics{}, Config{})
	m.showSessions = true
	updated, cmd := m.updateKey("esc")
	next := updated.(Model)
	if next.showSessions {
		t.Fatal("esc did not close the sessions view")
	}
	if cmd == nil {
		t.Fatal("esc did not return a clear-screen command")
	}
}

func TestUpdateKeyEscIsNoOpOutsideSessionsView(t *testing.T) {
	m := NewModel(fakeLoader{}, fakeMetrics{}, Config{})
	updated, cmd := m.updateKey("esc")
	next := updated.(Model)
	if next.showSessions {
		t.Fatal("esc unexpectedly opened the sessions view")
	}
	if cmd != nil {
		t.Fatal("esc outside sessions view returned a non-nil command")
	}
}

func TestUpdateKeyRefreshClearsAndReloads(t *testing.T) {
	m := NewModel(fakeLoader{}, fakeMetrics{}, Config{})
	_, cmd := m.updateKey("r")
	if cmd == nil {
		t.Fatal("updateKey(\"r\") returned a nil command, want clear+reload")
	}
}

func TestUpdateKeyUpDownNavigateSessionsView(t *testing.T) {
	now := time.Now()
	snapshots := []statusmodel.Snapshot{
		{SchemaVersion: modelSchema(), CapturedAt: now, Session: statusmodel.Session{ID: "a"}},
		{SchemaVersion: modelSchema(), CapturedAt: now, Session: statusmodel.Session{ID: "b"}},
	}
	m := NewModel(fakeLoader{}, fakeMetrics{}, Config{})
	updated, _ := m.Update(dataMsg{snapshots: snapshots})
	m = updated.(Model)
	m.showSessions = true

	updated, _ = m.updateKey("up")
	m = updated.(Model)
	if m.sessionCursor != len(snapshots)-1 {
		t.Fatalf("updateKey(\"up\") cursor = %d, want %d", m.sessionCursor, len(snapshots)-1)
	}
	updated, _ = m.updateKey("down")
	m = updated.(Model)
	if m.sessionCursor != 0 {
		t.Fatalf("updateKey(\"down\") cursor = %d, want 0", m.sessionCursor)
	}
	updated, _ = m.updateKey("k")
	m = updated.(Model)
	if m.sessionCursor != len(snapshots)-1 {
		t.Fatalf("updateKey(\"k\") cursor = %d, want %d", m.sessionCursor, len(snapshots)-1)
	}
	updated, _ = m.updateKey("j")
	m = updated.(Model)
	if m.sessionCursor != 0 {
		t.Fatalf("updateKey(\"j\") cursor = %d, want 0", m.sessionCursor)
	}
}

func TestMoveSessionCursorNoOpOutsideSessionsViewOrEmptyList(t *testing.T) {
	now := time.Now()
	m := NewModel(fakeLoader{}, fakeMetrics{}, Config{})
	updated, _ := m.Update(dataMsg{snapshots: []statusmodel.Snapshot{
		{SchemaVersion: modelSchema(), CapturedAt: now, Session: statusmodel.Session{ID: "a"}},
	}})
	m = updated.(Model)

	// Sessions view closed: moveSessionCursor must no-op.
	updated, _ = m.updateKey("down")
	m = updated.(Model)
	if m.sessionCursor != 0 {
		t.Fatalf("cursor moved outside sessions view: %d", m.sessionCursor)
	}

	// Sessions view open but the list is empty: moveSessionCursor must no-op.
	m.showSessions = true
	m.snapshots = nil
	updated, _ = m.updateKey("down")
	m = updated.(Model)
	if m.sessionCursor != 0 {
		t.Fatalf("cursor moved with an empty session list: %d", m.sessionCursor)
	}
}

func TestReconcileSelectionResetsCursorOnEmptySnapshots(t *testing.T) {
	m := NewModel(fakeLoader{}, fakeMetrics{}, Config{})
	m.sessionCursor = 3
	updated, _ := m.Update(dataMsg{snapshots: nil})
	m = updated.(Model)
	if m.sessionCursor != 0 {
		t.Fatalf("reconcileSelection with no snapshots left cursor = %d, want 0", m.sessionCursor)
	}
}

func TestReconcileSelectionResetsAndUnpinsWhenPinnedSessionDisappears(t *testing.T) {
	now := time.Now()
	m := NewModel(fakeLoader{}, fakeMetrics{}, Config{})
	updated, _ := m.Update(dataMsg{snapshots: []statusmodel.Snapshot{
		{SchemaVersion: modelSchema(), CapturedAt: now, Session: statusmodel.Session{ID: "a"}},
	}})
	m = updated.(Model)
	m.selectionPinned = true
	m.selectedID = "gone"
	updated, _ = m.Update(dataMsg{snapshots: []statusmodel.Snapshot{
		{SchemaVersion: modelSchema(), CapturedAt: now, Session: statusmodel.Session{ID: "a"}},
	}})
	m = updated.(Model)
	if m.selectionPinned {
		t.Fatal("pinned selection was not cleared when the pinned session disappeared")
	}
	if m.selectedID != "a" {
		t.Fatalf("selectedID = %q, want %q", m.selectedID, "a")
	}
	if m.sessionCursor != 0 {
		t.Fatalf("sessionCursor = %d, want 0", m.sessionCursor)
	}
}

func TestDashboardViewClampsFutureCapturedAtToZeroAge(t *testing.T) {
	now := time.Now()
	snapshot := statusmodel.Snapshot{SchemaVersion: modelSchema(), CapturedAt: now.Add(time.Hour), Session: statusmodel.Session{ID: "future"}}
	m := NewModel(fakeLoader{}, fakeMetrics{}, Config{})
	m.now = func() time.Time { return now }
	updated, _ := m.Update(dataMsg{snapshots: []statusmodel.Snapshot{snapshot}})
	m = updated.(Model)
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 66, Height: 20})
	m = updated.(Model)
	if !strings.Contains(m.View(), "just now") {
		t.Fatalf("future CapturedAt was not clamped to zero age:\n%s", m.View())
	}
}

func TestFullDashboardFrameShowsModeText(t *testing.T) {
	now := time.Now()
	thinkingOn := true
	snapshot := statusmodel.Snapshot{
		SchemaVersion:   modelSchema(),
		CapturedAt:      now,
		Session:         statusmodel.Session{ID: "mode"},
		Effort:          "high",
		ThinkingEnabled: &thinkingOn,
	}
	m := NewModel(fakeLoader{}, fakeMetrics{}, Config{})
	m.now = func() time.Time { return now }
	updated, _ := m.Update(dataMsg{snapshots: []statusmodel.Snapshot{snapshot}})
	m = updated.(Model)
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 66, Height: 20})
	m = updated.(Model)
	view := m.View()
	for _, want := range []string{"effort high", "thinking on"} {
		if !strings.Contains(view, want) {
			t.Fatalf("full dashboard frame does not contain %q:\n%s", want, view)
		}
	}
}

func TestWaitingFrameShowsLoadError(t *testing.T) {
	m := NewModel(fakeLoader{}, fakeMetrics{}, Config{})
	updated, _ := m.Update(dataMsg{snapshots: nil, err: errors.New("boom load failure")})
	m = updated.(Model)
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 66, Height: 20})
	m = updated.(Model)
	if !strings.Contains(m.View(), "boom load failure") {
		t.Fatalf("waiting frame does not surface the load error:\n%s", m.View())
	}
}

func TestSystemTextIncludesLoadAverage(t *testing.T) {
	load := 1.23
	m := NewModel(fakeLoader{}, fakeMetrics{}, Config{})
	m.stats = systeminfo.Stats{Load1: &load}
	if got := m.systemText(); !strings.Contains(got, "Load 1.23") {
		t.Fatalf("systemText() = %q, want it to contain %q", got, "Load 1.23")
	}
}

func TestFinalizeFrameUsesLineCountWhenHeightIsZeroOrNegative(t *testing.T) {
	lines := []string{"a", "b", "c"}
	want := strings.Join(lines, "\n")
	if got := finalizeFrame(append([]string{}, lines...), 10, 0); got != want {
		t.Fatalf("finalizeFrame(height=0) = %q, want %q", got, want)
	}
	if got := finalizeFrame(append([]string{}, lines...), 10, -1); got != want {
		t.Fatalf("finalizeFrame(height=-1) = %q, want %q", got, want)
	}
}

func TestFinalizeFrameTruncatesOverflowingLines(t *testing.T) {
	lines := []string{"a", "b", "c", "d", "e"}
	got := finalizeFrame(lines, 10, 3)
	want := strings.Join([]string{"a", "b", bottomBorder(10)}, "\n")
	if got != want {
		t.Fatalf("finalizeFrame() = %q, want %q", got, want)
	}
}

func TestContextColorThresholds(t *testing.T) {
	red, yellow, blue := 90.0, 75.0, 74.9
	if got := contextColor(&red); got != colorRed {
		t.Fatalf("contextColor(90) = %v, want red", got)
	}
	if got := contextColor(&yellow); got != colorYellow {
		t.Fatalf("contextColor(75) = %v, want yellow", got)
	}
	if got := contextColor(&blue); got != colorBlue {
		t.Fatalf("contextColor(74.9) = %v, want blue", got)
	}
	if got := contextColor(nil); got != colorMuted {
		t.Fatalf("contextColor(nil) = %v, want muted", got)
	}
}

func TestResetCountdownVariants(t *testing.T) {
	now := time.Now()
	if got := resetCountdown(nil, now); got != "reset --" {
		t.Fatalf("resetCountdown(nil) = %q, want %q", got, "reset --")
	}
	zero := int64(0)
	if got := resetCountdown(&zero, now); got != "reset --" {
		t.Fatalf("resetCountdown(0) = %q, want %q", got, "reset --")
	}
	past := now.Add(-time.Minute).Unix()
	if got := resetCountdown(&past, now); got != "reset due" {
		t.Fatalf("resetCountdown(past) = %q, want %q", got, "reset due")
	}
	future := now.Add(90 * time.Second).Unix()
	if got := resetCountdown(&future, now); !strings.HasPrefix(got, "resets ") {
		t.Fatalf("resetCountdown(future) = %q, want prefix %q", got, "resets ")
	}
}

func TestDisplayModelNameFallsBackByProvider(t *testing.T) {
	if got := displayModelName(statusmodel.Snapshot{Provider: "codex"}); got != "Codex" {
		t.Fatalf("displayModelName(codex) = %q, want %q", got, "Codex")
	}
	if got := displayModelName(statusmodel.Snapshot{}); got != "Claude" {
		t.Fatalf("displayModelName(claude) = %q, want %q", got, "Claude")
	}
}

func TestTokenTextHandlesNil(t *testing.T) {
	if got := tokenText(nil); got != "--" {
		t.Fatalf("tokenText(nil) = %q, want %q", got, "--")
	}
}

func TestThresholdColorBoundaries(t *testing.T) {
	if got := thresholdColor(90); got != colorRed {
		t.Fatalf("thresholdColor(90) = %v, want red", got)
	}
	if got := thresholdColor(70); got != colorYellow {
		t.Fatalf("thresholdColor(70) = %v, want yellow", got)
	}
	if got := thresholdColor(50); got != colorGreen {
		t.Fatalf("thresholdColor(50) = %v, want green", got)
	}
}

func TestContextTextComputesUsedFromPercentageWhenTokensAreZero(t *testing.T) {
	window := int64(1000)
	pct := 50.0
	got := contextText(statusmodel.Context{WindowSize: &window, UsedPercentage: &pct})
	if got != "500 / 1k" {
		t.Fatalf("contextText() = %q, want %q", got, "500 / 1k")
	}
}

func TestHumanTokensFormatsMillions(t *testing.T) {
	if got := humanTokens(2_500_000); got != "2.5M" {
		t.Fatalf("humanTokens(2500000) = %q, want %q", got, "2.5M")
	}
	if got := humanTokens(3_000_000); got != "3M" {
		t.Fatalf("humanTokens(3000000) = %q, want %q", got, "3M")
	}
}

func TestFormatClockClampsNegativeDuration(t *testing.T) {
	if got := formatClock(-5 * time.Second); got != "00:00:00" {
		t.Fatalf("formatClock(-5s) = %q, want %q", got, "00:00:00")
	}
}

func TestModeTextVariants(t *testing.T) {
	if got := modeText(statusmodel.Snapshot{}); got != "" {
		t.Fatalf("modeText(empty) = %q, want empty", got)
	}
	if got := modeText(statusmodel.Snapshot{Effort: "high"}); got != "effort high" {
		t.Fatalf("modeText(effort) = %q, want %q", got, "effort high")
	}
	on, off := true, false
	if got := modeText(statusmodel.Snapshot{ThinkingEnabled: &on}); got != "thinking on" {
		t.Fatalf("modeText(thinking on) = %q, want %q", got, "thinking on")
	}
	if got := modeText(statusmodel.Snapshot{ThinkingEnabled: &off}); got != "thinking off" {
		t.Fatalf("modeText(thinking off) = %q, want %q", got, "thinking off")
	}
	if got := modeText(statusmodel.Snapshot{Effort: "low", ThinkingEnabled: &on}); got != "effort low · thinking on" {
		t.Fatalf("modeText(effort+thinking) = %q, want %q", got, "effort low · thinking on")
	}
}

func TestCompactDurationFormatsDays(t *testing.T) {
	if got := compactDuration(50*time.Hour + 15*time.Minute); got != "2d02h" {
		t.Fatalf("compactDuration(50h15m) = %q, want %q", got, "2d02h")
	}
}

func TestClipEdgeCases(t *testing.T) {
	if got := clip("hello", 0); got != "" {
		t.Fatalf("clip(width=0) = %q, want empty", got)
	}
	if got := clip("hello", -1); got != "" {
		t.Fatalf("clip(width=-1) = %q, want empty", got)
	}
	if got := clip("hello", 1); got != "…" {
		t.Fatalf("clip(width=1) = %q, want %q", got, "…")
	}
	if got := clip("hi", 5); got != "hi" {
		t.Fatalf("clip(short) = %q, want unchanged", got)
	}
	if got := clip("hello world", 5); got != "hell…" {
		t.Fatalf("clip(long) = %q, want %q", got, "hell…")
	}
}

func modelSchema() int { return statusmodel.CurrentSchemaVersion }
