package pixelui

import (
	"context"
	"errors"
	"image"
	"testing"
	"time"

	"github.com/dvgamerr/llm-status/internal/model"
	"github.com/dvgamerr/llm-status/internal/systeminfo"
	"github.com/dvgamerr/llm-status/internal/touch"
)

type testLoader struct {
	snapshots []model.Snapshot
	err       error
}

func (loader testLoader) LoadAll() ([]model.Snapshot, error) { return loader.snapshots, loader.err }

type testMetrics struct{ stats systeminfo.Stats }

func (metrics testMetrics) Read() systeminfo.Stats { return metrics.stats }

type testScreen struct {
	frames int
	err    error
}

func (screen *testScreen) Present(frame image.Image) error {
	if frame.Bounds() != image.Rect(0, 0, Width, Height) {
		return errors.New("wrong frame size")
	}
	screen.frames++
	return screen.err
}

func TestRunRendersImmediatelyAndStopsWithContext(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	screen := &testScreen{}
	err = Run(ctx, testLoader{}, testMetrics{}, screen, renderer, RunConfig{RefreshInterval: time.Second}, nil)
	if err != nil || screen.frames != 1 {
		t.Fatalf("Run() error=%v frames=%d", err, screen.frames)
	}
}

type countingMetrics struct{ reads int }

func (metrics *countingMetrics) Read() systeminfo.Stats {
	metrics.reads++
	return systeminfo.Stats{}
}

func TestRunThrottlesStatsSamplingIndependentlyOfRefresh(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	metrics := &countingMetrics{}
	screen := &testScreen{}
	// The window is generous relative to the 10ms refresh interval because
	// drawMascot now evaluates and rasterizes a rig's SMIL animation on every
	// tick instead of blitting a pre-rasterized frame (see icons.go); under
	// -race on a loaded CI runner a single tick can cost tens of ms, so a
	// tight window flakes on frame count even though throttling itself is
	// working. 800ms stays comfortably under two 500ms stats-interval
	// boundaries, so metrics.reads is still expected to land at 1-2.
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	if err := Run(ctx, testLoader{}, metrics, screen, renderer, RunConfig{RefreshInterval: 10 * time.Millisecond}, nil); err != nil {
		t.Fatal(err)
	}
	if screen.frames < 8 {
		t.Fatalf("frames = %d, want several at a 10ms refresh over 800ms", screen.frames)
	}
	if metrics.reads > 2 {
		t.Fatalf("Stats.Read() called %d times in 800ms; want throttling to ~1-2 calls at the 500ms stats interval", metrics.reads)
	}
}

func TestRenderFramePropagatesPresentError(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	screen := &testScreen{err: errors.New("screen failed")}
	err = renderFrame(testLoader{err: errors.New("partial state")}, screen, renderer, RunConfig{}, time.Now(), nil, systeminfo.Stats{})
	if err == nil || !errors.Is(err, screen.err) {
		t.Fatalf("renderFrame() error = %v", err)
	}
}

func TestDrainTouchesCollectsAndExpiresPoints(t *testing.T) {
	now := time.Now()
	channel := make(chan touch.Point, 2)
	channel <- touch.Point{X: 1, Y: 2, At: now}
	channel <- touch.Point{X: 3, Y: 4, At: now}

	active := drainTouches(channel, nil, now)
	if len(active) != 2 {
		t.Fatalf("drainTouches() collected %d points, want 2", len(active))
	}

	active = drainTouches(channel, active, now.Add(touchRippleLifetime+time.Millisecond))
	if len(active) != 0 {
		t.Fatalf("drainTouches() kept %d expired points, want 0", len(active))
	}
}

func TestDrainTouchesHandlesClosedChannel(t *testing.T) {
	channel := make(chan touch.Point)
	close(channel)
	if got := drainTouches(channel, nil, time.Now()); len(got) != 0 {
		t.Fatalf("drainTouches() added zero values from a closed channel: %+v", got)
	}
}

func TestRunRejectsNilDependencies(t *testing.T) {
	tests := []struct {
		name    string
		loader  SnapshotLoader
		metrics MetricsReader
		screen  Screen
	}{
		{name: "loader", metrics: testMetrics{}, screen: &testScreen{}},
		{name: "metrics", loader: testLoader{}, screen: &testScreen{}},
		{name: "screen", loader: testLoader{}, metrics: testMetrics{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Run(context.Background(), tt.loader, tt.metrics, tt.screen, nil, RunConfig{}, nil); err == nil {
				t.Fatal("Run() accepted a nil dependency")
			}
		})
	}
}

func TestRunRejectsNilRenderer(t *testing.T) {
	err := Run(context.Background(), testLoader{}, testMetrics{}, &testScreen{}, nil, RunConfig{}, nil)
	if err == nil {
		t.Fatal("Run() accepted a nil renderer")
	}
}

func TestRunDefaultsRefreshIntervalWhenUnset(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	screen := &testScreen{}
	// A zero RunConfig must fall back to the 1s/15s defaults instead of a
	// zero-interval ticker (which would panic) or an always-stale check.
	if err := Run(ctx, testLoader{}, testMetrics{}, screen, renderer, RunConfig{}, nil); err != nil || screen.frames != 1 {
		t.Fatalf("Run() with a zero RunConfig error=%v frames=%d", err, screen.frames)
	}
}

func TestRunReturnsErrorFromInitialRender(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	screen := &testScreen{err: errors.New("initial present failed")}
	err = Run(context.Background(), testLoader{}, testMetrics{}, screen, renderer, RunConfig{RefreshInterval: time.Second}, nil)
	if err == nil || !errors.Is(err, screen.err) {
		t.Fatalf("Run() error = %v, want the initial Present error surfaced before the ticker loop starts", err)
	}
}

// flakySreen succeeds for its first failAfter calls, then fails every call
// after that — used to reach the ticker loop's own render-error branch,
// which is otherwise unreachable from a screen that fails immediately.
type flakyScreen struct {
	frames    int
	failAfter int
	err       error
}

func (screen *flakyScreen) Present(frame image.Image) error {
	screen.frames++
	if screen.frames > screen.failAfter {
		return screen.err
	}
	return nil
}

func TestRunReturnsErrorFromTickRender(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	screen := &flakyScreen{failAfter: 1, err: errors.New("tick present failed")}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = Run(ctx, testLoader{}, testMetrics{}, screen, renderer, RunConfig{RefreshInterval: 5 * time.Millisecond}, nil)
	if err == nil || !errors.Is(err, screen.err) {
		t.Fatalf("Run() error = %v, want the ticker loop's own Present error", err)
	}
	if screen.frames < 2 {
		t.Fatalf("Run() stopped after %d frame(s), want at least 2 (one success, one failure)", screen.frames)
	}
}

func TestLatestProvidersKeepsClaudePrimaryWhenCodexIsNewer(t *testing.T) {
	now := time.Now()
	snapshots := []model.Snapshot{
		{Provider: "codex", CapturedAt: now, Session: model.Session{ID: "codex-new"}},
		{Provider: "claude", CapturedAt: now.Add(-time.Hour), Session: model.Session{ID: "claude-old"}},
		{Provider: "claude", CapturedAt: now.Add(-time.Minute), Session: model.Session{ID: "claude-current"}},
		{Provider: "openai", CapturedAt: now.Add(time.Minute), Session: model.Session{ID: "codex-alias"}},
	}
	claude, codex := LatestProviders(snapshots)
	if claude == nil || claude.Session.ID != "claude-current" {
		t.Fatalf("Claude selection = %+v", claude)
	}
	if codex == nil || codex.Session.ID != "codex-alias" {
		t.Fatalf("Codex selection = %+v", codex)
	}
}

func TestLatestProvidersBackfillsHookOnlySession(t *testing.T) {
	now := time.Now()
	fiveHour := 12.0
	snapshots := []model.Snapshot{
		{
			Provider:   "claude",
			CapturedAt: now.Add(-time.Hour),
			Session:    model.Session{ID: "terminal"},
			Model:      model.Model{ID: "claude-opus-5"},
			RateLimits: model.RateLimits{FiveHour: model.RateWindow{UsedPercentage: &fiveHour}},
		},
		{
			Provider:   "claude",
			CapturedAt: now,
			Session:    model.Session{ID: "vscode"},
			Activity:   model.Activity{State: model.ActivityBuilding, UpdatedAt: now},
		},
	}
	claude, codex := LatestProviders(snapshots)
	if codex != nil {
		t.Fatalf("Codex selection = %+v, want none", codex)
	}
	if claude == nil || claude.Session.ID != "vscode" || claude.Activity.State != model.ActivityBuilding {
		t.Fatalf("Claude selection = %+v", claude)
	}
	if got := claude.RateLimits.FiveHour.UsedPercentage; got == nil || *got != fiveHour {
		t.Fatalf("rate limits were not backfilled: %v", got)
	}
	if claude.Model.ID != "claude-opus-5" {
		t.Fatalf("model was not backfilled: %+v", claude.Model)
	}
}
