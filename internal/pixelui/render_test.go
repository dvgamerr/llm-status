package pixelui

import (
	"errors"
	"image"
	"image/color"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/font"

	"github.com/dvgamerr/llm-status/internal/model"
	"github.com/dvgamerr/llm-status/internal/systeminfo"
	"github.com/dvgamerr/llm-status/internal/touch"
)

func baseSnapshot(now time.Time) model.Snapshot {
	contextPct, fivePct, sevenPct := 72.0, 51.0, 91.0
	window, input, output := int64(200000), int64(140000), int64(4000)
	resetFive, resetSeven := now.Add(90*time.Minute).Unix(), now.Add(72*time.Hour).Unix()
	return model.Snapshot{
		SchemaVersion: model.CurrentSchemaVersion,
		CapturedAt:    now.Add(-4 * time.Second),
		Provider:      "claude",
		ClientVersion: "2.1.220",
		Session:       model.Session{ID: "session-123", Name: "Pi dashboard demo"},
		Model:         model.Model{ID: "claude-opus", DisplayName: "Opus"},
		Context:       model.Context{UsedPercentage: &contextPct, WindowSize: &window, TotalInputTokens: &input, TotalOutputTokens: &output},
		RateLimits: model.RateLimits{
			FiveHour: model.RateWindow{UsedPercentage: &fivePct, ResetsAt: &resetFive},
			SevenDay: model.RateWindow{UsedPercentage: &sevenPct, ResetsAt: &resetSeven},
		},
		Effort: "high",
	}
}

func baseStats() systeminfo.Stats {
	cpu, temperature, load := 18.0, 52.0, 0.42
	usedMemory, totalMemory := uint64(1<<30), uint64(4<<30)
	return systeminfo.Stats{
		CPUPercent: &cpu, TemperatureC: &temperature, Load1: &load,
		MemoryUsedBytes: &usedMemory, MemoryTotalBytes: &totalMemory,
	}
}

func TestRenderDashboardAndWaitingFrames(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	snapshot := baseSnapshot(now)
	frame := renderer.Render(View{
		Claude: &snapshot, Stats: baseStats(), Now: now, StaleAfter: 15 * time.Second, SessionCount: 2,
	})
	if frame.Bounds() != image.Rect(0, 0, Width, Height) {
		t.Fatalf("frame bounds = %v", frame.Bounds())
	}
	// Header Anthropic mark, rail mascot/health, open Claude panel's limit/context
	// bars, and the framed Codex card (confined to the middle+bottom, not
	// the top row).
	for _, point := range []image.Point{{37, 27}, {80, 150}, {30, 400}, {300, 102}, {300, 358}, {600, 260}, {600, 400}} {
		if got := rgba(frame.At(point.X, point.Y)); got == backgroundTop {
			t.Fatalf("expected card/content at %v, got background %v", point, got)
		}
	}
	if got := rgba(frame.At(19, 22)); got != backgroundTop {
		t.Fatalf("Anthropic mark has a background or frame at its corner: %v", got)
	}
	if rgba(frame.At(600, 60)) != backgroundTop {
		t.Fatal("Codex card should not extend into the top row")
	}
	waiting := renderer.Render(View{Now: now, LoadError: errors.New("waiting")})
	if waiting.Bounds() != frame.Bounds() || rgba(waiting.At(600, 260)) == backgroundTop {
		t.Fatalf("waiting frame was not rendered")
	}
}

func TestHeaderOmitsModelAndSessionIdentity(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	first := baseSnapshot(now)
	second := first
	second.Model = model.Model{ID: "different-model", DisplayName: "Different Model"}
	second.Session = model.Session{ID: "different-session", Name: "Different Session"}
	second.Effort = "max"

	firstFrame := renderer.Render(View{Claude: &first, Now: now, StaleAfter: 15 * time.Second})
	secondFrame := renderer.Render(View{Claude: &second, Now: now, StaleAfter: 15 * time.Second})
	if !sameRegion(firstFrame, secondFrame, image.Rect(0, 0, Width, sectionsTop)) {
		t.Fatal("header still changes with model or session identity")
	}
}

func TestRenderDrawsFreshTouchRippleAndSkipsStaleOnes(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	snapshot := baseSnapshot(now)
	baseView := View{Claude: &snapshot, Now: now, StaleAfter: 15 * time.Second}

	without := renderer.Render(baseView)

	fresh := baseView
	fresh.Touches = []touch.Point{{X: 400, Y: 300, At: now}}
	withFresh := renderer.Render(fresh)
	if sameRegion(without, withFresh, image.Rect(380, 280, 420, 320)) {
		t.Fatal("a fresh touch point did not draw a ripple")
	}

	stale := baseView
	stale.Touches = []touch.Point{{X: 400, Y: 300, At: now.Add(-touchRippleLifetime - time.Millisecond)}}
	withStale := renderer.Render(stale)
	if !sameRegion(without, withStale, image.Rect(380, 280, 420, 320)) {
		t.Fatal("an expired touch point still drew a ripple")
	}
}

func TestRailMascotAnimatesOverTime(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(0, 0)
	snapshot := baseSnapshot(now)
	snapshot.Activity = model.Activity{State: model.ActivityWorking, UpdatedAt: now}
	frame := renderer.Render(View{Claude: &snapshot, Now: now, StaleAfter: 15 * time.Second})
	// ActivityWorking now plays a rigged SVG animation (see
	// renderAnimatedIcon) that's a continuous function of time, not a
	// discrete frame index, so any nonzero delta should show a difference.
	later := renderer.Render(View{Claude: &snapshot, Now: now.Add(50 * time.Millisecond), StaleAfter: 15 * time.Second})
	mascotRegion := image.Rect(railLeft+20, sectionsTop+20, railRight-20, sectionsTop+180)
	if sameRegion(frame, later, mascotRegion) {
		t.Fatal("mascot did not animate between frames")
	}
}

func TestEachActivityAnimatesOnlyTheMascot(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	centerX, centerY := 100, 100
	start := time.Unix(0, 0)
	tests := []struct {
		name  string
		state string
		later time.Time
		// fixedRect states rasterize into the same railIconSize square every
		// tick (see animatedSVGForActivity/renderAnimatedIcon) — only the
		// rasterized content changes, not the icon's position/scale on
		// screen — so movingBounds can't be derived from
		// mascotPoseForActivity for them.
		fixedRect bool
	}{
		{name: "typing rig", state: model.ActivityTyping, later: start.Add(300 * time.Millisecond), fixedRect: true},
		{name: "idle rig", state: model.ActivityIdle, later: start.Add(300 * time.Millisecond), fixedRect: true},
		{name: "approval shake", state: model.ActivityWaitingApproval, later: start.Add(65 * time.Millisecond)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := image.NewRGBA(image.Rect(0, 0, 200, 200))
			second := image.NewRGBA(first.Bounds())
			renderer.drawMascot(first, centerX, centerY, start, tt.state)
			renderer.drawMascot(second, centerX, centerY, tt.later, tt.state)
			if sameRegion(first, second, first.Bounds()) {
				t.Fatal("mascot artwork did not move")
			}

			var movingBounds image.Rectangle
			if tt.fixedRect {
				movingBounds = iconDestination(centerX, centerY, railIconSize, railIconSize)
			} else {
				firstPose := mascotPoseForActivity(start)
				secondPose := mascotPoseForActivity(tt.later)
				movingBounds = iconDestination(
					centerX+firstPose.xOffset, centerY+firstPose.yOffset, firstPose.width, firstPose.height,
				).Union(iconDestination(
					centerX+secondPose.xOffset, centerY+secondPose.yOffset, secondPose.width, secondPose.height,
				))
			}
			if !sameOutsideRegion(first, second, movingBounds) {
				t.Fatal("mascot animation changed the static backdrop")
			}
		})
	}
}

// BenchmarkDrawMascot covers the cost this refactor moved from "once at
// startup" to every render tick (~66ms in production): evaluating a rig's
// SMIL animation for the current instant and rasterizing the result. See
// icons.go's startup comment and the maintenance log in CLAUDE.md.
func BenchmarkDrawMascot(b *testing.B) {
	renderer, err := NewRenderer()
	if err != nil {
		b.Fatal(err)
	}
	canvas := image.NewRGBA(image.Rect(0, 0, 200, 200))
	now := time.Unix(0, 0)
	for i := 0; b.Loop(); i++ {
		renderer.drawMascot(canvas, 100, 100, now.Add(time.Duration(i)*time.Millisecond), model.ActivityTyping)
	}
}

func TestResolveActivityStates(t *testing.T) {
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	if got := resolveActivity(nil, now); got != model.ActivityIdle {
		t.Fatalf("resolveActivity(nil) = %q", got)
	}

	fresh := model.Snapshot{CapturedAt: now.Add(-1 * time.Second)}
	if got := resolveActivity(&fresh, now); got != model.ActivityWorking {
		t.Fatalf("resolveActivity(fresh statusLine) = %q, want working proxy", got)
	}

	old := model.Snapshot{CapturedAt: now.Add(-30 * time.Second)}
	if got := resolveActivity(&old, now); got != model.ActivityIdle {
		t.Fatalf("resolveActivity(stale statusLine) = %q, want idle", got)
	}

	waiting := model.Snapshot{Activity: model.Activity{State: model.ActivityWaitingApproval, UpdatedAt: now.Add(-1 * time.Minute)}}
	if got := resolveActivity(&waiting, now); got != model.ActivityWaitingApproval {
		t.Fatalf("resolveActivity(waiting_approval) = %q", got)
	}

	stuck := model.Snapshot{Activity: model.Activity{State: model.ActivityWorking, UpdatedAt: now.Add(-1 * time.Hour)}}
	if got := resolveActivity(&stuck, now); got != model.ActivityIdle {
		t.Fatalf("resolveActivity(stuck working) = %q, want idle fallback", got)
	}

	subagentOne := model.Snapshot{Activity: model.Activity{State: model.ActivityTyping, UpdatedAt: now.Add(-1 * time.Second), Subagents: 1}}
	if got := resolveActivity(&subagentOne, now); got != model.ActivitySubagentOne {
		t.Fatalf("resolveActivity(1 subagent) = %q", got)
	}

	subagentMany := model.Snapshot{Activity: model.Activity{State: model.ActivityBuilding, UpdatedAt: now.Add(-1 * time.Second), Subagents: 3}}
	if got := resolveActivity(&subagentMany, now); got != model.ActivitySubagentMany {
		t.Fatalf("resolveActivity(3 subagents) = %q, want the 2+ tier", got)
	}

	approvalOverridesSubagents := model.Snapshot{Activity: model.Activity{State: model.ActivityWaitingApproval, UpdatedAt: now.Add(-1 * time.Second), Subagents: 2}}
	if got := resolveActivity(&approvalOverridesSubagents, now); got != model.ActivityWaitingApproval {
		t.Fatalf("resolveActivity(approval + subagents) = %q, want a pending approval to win", got)
	}

	// A SubagentStart merged before any state-setting hook ever fired for
	// this session leaves State == "" with Subagents already positive —
	// the count must still win instead of falling through to the
	// statusLine-freshness fallback, which would ignore it entirely.
	subagentBeforeAnyState := model.Snapshot{
		CapturedAt: now.Add(-30 * time.Second),
		Activity:   model.Activity{UpdatedAt: now.Add(-1 * time.Second), Subagents: 1},
	}
	if got := resolveActivity(&subagentBeforeAnyState, now); got != model.ActivitySubagentOne {
		t.Fatalf("resolveActivity(subagent before any state) = %q, want subagent_one", got)
	}
}

// TestNewActivityStatesRenderDistinctly covers Typing/Thinking/Building/
// Subagent-count, each of which plays back its own traced GIF frame
// sequence (see internal/pixelui/assets/README.md).
func TestNewActivityStatesRenderDistinctly(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	centerX, centerY := 100, 100
	now := time.Unix(0, 0)

	states := []string{
		model.ActivityTyping,
		model.ActivityThinking,
		model.ActivityBuilding,
		model.ActivitySubagentOne,
		model.ActivitySubagentMany,
	}
	frames := make(map[string]*image.RGBA, len(states))
	for _, state := range states {
		canvas := image.NewRGBA(image.Rect(0, 0, 200, 200))
		renderer.drawMascot(canvas, centerX, centerY, now, state)
		frames[state] = canvas
	}
	for i, a := range states {
		for _, b := range states[i+1:] {
			if sameRegion(frames[a], frames[b], frames[a].Bounds()) {
				t.Fatalf("%s and %s rendered identically", a, b)
			}
		}
	}

	// A single fixed delta can coincidentally round back to the same integer
	// pose offset for some state's period (e.g. 300ms happens to land back
	// on a zero offset for a 620ms period) — check a couple of deltas and
	// only fail if none of them show movement.
	deltas := []time.Duration{150 * time.Millisecond, 300 * time.Millisecond, 470 * time.Millisecond}
	for _, state := range states {
		first := image.NewRGBA(image.Rect(0, 0, 200, 200))
		renderer.drawMascot(first, centerX, centerY, now, state)
		animated := false
		for _, delta := range deltas {
			second := image.NewRGBA(first.Bounds())
			renderer.drawMascot(second, centerX, centerY, now.Add(delta), state)
			if !sameRegion(first, second, first.Bounds()) {
				animated = true
				break
			}
		}
		if !animated {
			t.Fatalf("%s did not animate across any sampled delta", state)
		}
	}
}

func TestRenderUsesContextSpecificClawdIcons(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	snapshot := baseSnapshot(now)
	snapshot.Activity = model.Activity{State: model.ActivityWaitingApproval, UpdatedAt: now.Add(-5 * time.Second)}

	idle := baseSnapshot(now)
	idle.Activity = model.Activity{State: model.ActivityIdle, UpdatedAt: now.Add(-5 * time.Second)}
	working := baseSnapshot(now)
	working.Activity = model.Activity{State: model.ActivityWorking, UpdatedAt: now.Add(-5 * time.Second)}

	waitingFrame := renderer.Render(View{Claude: &snapshot, Now: now, StaleAfter: 15 * time.Second})
	idleFrame := renderer.Render(View{Claude: &idle, Now: now, StaleAfter: 15 * time.Second})
	workingFrame := renderer.Render(View{Claude: &working, Now: now, StaleAfter: 15 * time.Second})

	mascotRegion := image.Rect(railLeft+20, sectionsTop+20, railRight-10, sectionsTop+160)
	if sameRegion(waitingFrame, idleFrame, mascotRegion) {
		t.Fatal("Clawd Exclamation Mark did not replace Clawd Sleeping")
	}
	if sameRegion(workingFrame, idleFrame, mascotRegion) {
		t.Fatal("Clawd Coding did not replace Clawd Sleeping")
	}
	if sameRegion(workingFrame, waitingFrame, mascotRegion) {
		t.Fatal("working and approval rendered the same Clawd icon")
	}
}

func TestRenderClaudePrimaryWithCodexCompact(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	claudePct, codexPct := 72.0, 42.5
	claude := model.Snapshot{
		SchemaVersion: model.CurrentSchemaVersion,
		CapturedAt:    now.Add(-2 * time.Second),
		Provider:      "claude",
		Session:       model.Session{ID: "claude-session", Name: "Primary Claude work"},
		Model:         model.Model{DisplayName: "Opus"},
		Context:       model.Context{UsedPercentage: &claudePct},
	}
	codex := model.Snapshot{
		SchemaVersion:   model.CurrentSchemaVersion,
		CapturedAt:      now.Add(-time.Minute),
		Provider:        "codex",
		Session:         model.Session{ID: strings.Repeat("a", 40)},
		Model:           model.Model{ID: "gpt-5.6-sol"},
		Context:         model.Context{UsedPercentage: &codexPct},
		ThinkingEnabled: boolPointer(true),
	}
	frame := renderer.Render(View{Claude: &claude, Codex: &codex, Now: now, StaleAfter: 15 * time.Second, SessionCount: 2})
	if frame.Bounds().Dx() != Width || frame.Bounds().Dy() != Height {
		t.Fatalf("unexpected frame size %v", frame.Bounds())
	}
	if rgba(frame.At(600, 400)) == backgroundTop {
		t.Fatal("Codex compact card was not rendered")
	}
}

func TestFormattingHelpers(t *testing.T) {
	thousand, million := int64(1200), int64(2_000_000)
	if tokenLabel(&thousand) != "1.2K" || tokenLabel(&million) != "2M" || tokenLabel(nil) != "--" {
		t.Fatalf("token labels are incorrect")
	}
	if got := durationLabel(26*time.Hour + 3*time.Minute); got != "1d 2h" {
		t.Fatalf("durationLabel() = %q", got)
	}
	if got := thresholdColor(95, claudeOrange); got != red {
		t.Fatalf("thresholdColor(95) = %v", got)
	}
	if got := thresholdColor(75, claudeOrange); got != yellow {
		t.Fatalf("thresholdColor(75) = %v", got)
	}
}

func TestLimitLineHandlesUnlimitedAndUnavailableValues(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	canvas := image.NewRGBA(image.Rect(0, 0, Width, Height))
	unlimited := true
	renderer.limitLine(canvas, 20, 40, 200, "5 HOUR", model.RateWindow{}, model.RateLimits{Unlimited: &unlimited, Plan: "enterprise"}, claudeOrange, time.Now())
	renderer.limitLine(canvas, 260, 40, 200, "7 DAY", model.RateWindow{}, model.RateLimits{}, claudePeach, time.Now())
	// The progress bar (drawn even for "no data") is a reliable filled region
	// regardless of glyph shapes, unlike checking a single text pixel.
	if rgba(canvas.At(25, 58)) == (color.RGBA{}) || rgba(canvas.At(265, 58)) == (color.RGBA{}) {
		t.Fatal("limit lines were not painted")
	}
}

func TestLimitLineShowsUnavailableOnceTheStoredResetPasses(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	highPct := 93.0

	pastReset := now.Add(-time.Minute).Unix()
	expired := image.NewRGBA(image.Rect(0, 0, Width, Height))
	renderer.limitLine(expired, 20, 40, 200, "5 HOUR", model.RateWindow{UsedPercentage: &highPct, ResetsAt: &pastReset}, model.RateLimits{}, claudeOrange, now)

	futureReset := now.Add(time.Hour).Unix()
	active := image.NewRGBA(image.Rect(0, 0, Width, Height))
	renderer.limitLine(active, 20, 40, 200, "5 HOUR", model.RateWindow{UsedPercentage: &highPct, ResetsAt: &futureReset}, model.RateLimits{}, claudeOrange, now)

	// At 93%, the bar reaches well past x=170; once the stored reset has
	// passed, that percentage is stale (the window renewed server-side) so
	// the bar should reset to empty instead of staying frozen at 93%.
	barPoint := image.Point{X: 170, Y: 58}
	if rgba(expired.At(barPoint.X, barPoint.Y)) == rgba(active.At(barPoint.X, barPoint.Y)) {
		t.Fatal("expired window's bar should differ from an active window's bar at the same stored percentage")
	}
}

func TestResetCountdownRecomputesLiveFromNow(t *testing.T) {
	epoch := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).Unix()
	early := resetLabelShort(&epoch, time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC))
	later := resetLabelShort(&epoch, time.Date(2026, 8, 1, 11, 0, 0, 0, time.UTC))
	if early == later {
		t.Fatalf("resetLabelShort() did not change as time advanced toward the same epoch: %q", early)
	}
}

func TestProviderNameBranches(t *testing.T) {
	tests := []struct {
		provider string
		want     string
	}{
		{provider: model.ProviderClaude, want: "CLAUDE"},
		{provider: "CLAUDE", want: "CLAUDE"},
		{provider: model.ProviderCodex, want: "CODEX"},
		{provider: "openai", want: "CODEX"},
		{provider: "", want: "CLAUDE"},
		{provider: "some-unrecognized-provider", want: "CLAUDE"},
	}
	for _, tt := range tests {
		if got := providerName(tt.provider); got != tt.want {
			t.Errorf("providerName(%q) = %q, want %q", tt.provider, got, tt.want)
		}
	}
}

func TestActivityCaptionBranches(t *testing.T) {
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	if got := activityCaption(model.Snapshot{}, model.ActivityIdle, now); got != "waiting for the next hook event" {
		t.Fatalf("activityCaption(no activity at all) = %q", got)
	}
	zeroUpdatedAt := model.Snapshot{Activity: model.Activity{State: model.ActivityIdle}}
	if got := activityCaption(zeroUpdatedAt, model.ActivityIdle, now); got != "waiting for the next hook event" {
		t.Fatalf("activityCaption(zero UpdatedAt) = %q", got)
	}

	future := model.Snapshot{Activity: model.Activity{State: model.ActivityTyping, UpdatedAt: now.Add(time.Minute)}}
	if got := activityCaption(future, model.ActivityTyping, now); got != "typing for 0s" {
		t.Fatalf("activityCaption(UpdatedAt after now) = %q, want elapsed clamped to 0", got)
	}

	tests := []struct {
		state string
		want  string
	}{
		{model.ActivityWorking, "typing for 5s"},
		{model.ActivityTyping, "typing for 5s"},
		{model.ActivityThinking, "thinking for 5s"},
		{model.ActivityBuilding, "building for 5s"},
		{model.ActivitySubagentOne, "1 subagent for 5s"},
		{model.ActivitySubagentMany, "subagents for 5s"},
		{model.ActivityWaitingApproval, "waiting 5s"},
		{model.ActivityIdle, "idle for 5s"},
		{"", "idle for 5s"},
	}
	snapshot := model.Snapshot{Activity: model.Activity{State: model.ActivityTyping, UpdatedAt: now.Add(-5 * time.Second)}}
	for _, tt := range tests {
		if got := activityCaption(snapshot, tt.state, now); got != tt.want {
			t.Errorf("activityCaption(rendered state=%q) = %q, want %q", tt.state, got, tt.want)
		}
	}
}

func TestFitTextBoundaries(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	face := renderer.regular12
	value := "Hello world"
	exact := font.MeasureString(face, value).Ceil()

	if got := fitText(face, value, exact); got != value {
		t.Fatalf("fitText at exact width = %q, want unmodified %q", got, value)
	}
	if got := fitText(face, value, exact+10); got != value {
		t.Fatalf("fitText under width = %q, want unmodified %q", got, value)
	}
	if got := fitText(face, value, exact-1); got == value || got == "" {
		t.Fatalf("fitText just over width = %q, want a truncated ellipsis", got)
	}
	if got := fitText(face, value, 0); got != "" {
		t.Fatalf("fitText width<=0 = %q, want empty", got)
	}
	if got := fitText(face, value, -5); got != "" {
		t.Fatalf("fitText negative width = %q, want empty", got)
	}
	// The ellipsis glyph itself is wider than this budget, so the trimming
	// loop must exhaust down to a single rune and still fall back to a bare
	// ellipsis instead of an empty string or a panic.
	if got := fitText(face, value, 1); got != "…" {
		t.Fatalf("fitText width smaller than the ellipsis itself = %q, want bare ellipsis", got)
	}
	if got := fitText(face, "  padded  ", 500); got != "padded" {
		t.Fatalf("fitText did not trim surrounding whitespace: %q", got)
	}
}

func TestModelLabelFallsBackToProviderName(t *testing.T) {
	if got := modelLabel(model.Snapshot{Provider: model.ProviderCodex}); got != "CODEX" {
		t.Fatalf("modelLabel(no model, codex) = %q", got)
	}
	if got := modelLabel(model.Snapshot{Provider: model.ProviderClaude}); got != "CLAUDE" {
		t.Fatalf("modelLabel(no model, claude) = %q", got)
	}
	if got := modelLabel(model.Snapshot{Model: model.Model{ID: "claude-opus"}}); got != "claude-opus" {
		t.Fatalf("modelLabel(ID only) = %q", got)
	}
	if got := modelLabel(model.Snapshot{Model: model.Model{DisplayName: "Opus"}}); got != "Opus" {
		t.Fatalf("modelLabel(DisplayName) = %q", got)
	}
}

func TestDurationLabelBoundaries(t *testing.T) {
	tests := []struct {
		value time.Duration
		want  string
	}{
		{0, "0s"},
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{90 * time.Minute, "1h 30m"},
		{26*time.Hour + 3*time.Minute, "1d 2h"},
	}
	for _, tt := range tests {
		if got := durationLabel(tt.value); got != tt.want {
			t.Errorf("durationLabel(%v) = %q, want %q", tt.value, got, tt.want)
		}
	}
}

func TestContextFractionBranches(t *testing.T) {
	if got := contextFraction(model.Context{}); got != "window unavailable" {
		t.Fatalf("contextFraction(no window) = %q", got)
	}

	window := int64(200000)
	pct := 50.0
	derivedUsed := int64(100000)
	want := tokenLabel(&derivedUsed) + " / " + tokenLabel(&window)
	if got := contextFraction(model.Context{WindowSize: &window, UsedPercentage: &pct}); got != want {
		t.Fatalf("contextFraction(derived from percentage) = %q, want %q", got, want)
	}

	input, output := int64(1000), int64(2000)
	total := int64(3000)
	wantDirect := tokenLabel(&total) + " / " + tokenLabel(&window)
	direct := model.Context{WindowSize: &window, TotalInputTokens: &input, TotalOutputTokens: &output}
	if got := contextFraction(direct); got != wantDirect {
		t.Fatalf("contextFraction(direct tokens) = %q, want %q", got, wantDirect)
	}

	// A huge window/percentage pair must not overflow into a garbled label.
	hugeWindow := int64(1_000_000_000)
	fullPct := 100.0
	hugeUsed := int64(1_000_000_000)
	wantHuge := tokenLabel(&hugeUsed) + " / " + tokenLabel(&hugeWindow)
	if got := contextFraction(model.Context{WindowSize: &hugeWindow, UsedPercentage: &fullPct}); got != wantHuge {
		t.Fatalf("contextFraction(huge values) = %q, want %q", got, wantHuge)
	}
}

func TestCodexUsageBlockUnmatchedModelWithRealTokens(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	canvas := image.NewRGBA(image.Rect(0, 0, 400, 100))
	input, output := int64(1000), int64(2000)
	snapshot := model.Snapshot{
		Model:   model.Model{ID: "some-unrecognized-vendor-model"},
		Context: model.Context{TotalInputTokens: &input, TotalOutputTokens: &output},
	}
	renderer.codexUsageBlock(canvas, 10, 40, 300, snapshot)
	if !regionHasNonZeroPixel(canvas, image.Rect(0, 0, 400, 100)) {
		t.Fatal("codexUsageBlock did not paint anything for real tokens + an unmatched model")
	}

	// Nil token pointers (a Context with nothing filled in) must not panic
	// and should still render some cost figure.
	canvasNilTokens := image.NewRGBA(image.Rect(0, 0, 400, 100))
	renderer.codexUsageBlock(canvasNilTokens, 10, 40, 300, model.Snapshot{Model: model.Model{ID: "gpt-4o"}})
	if !regionHasNonZeroPixel(canvasNilTokens, image.Rect(0, 0, 400, 100)) {
		t.Fatal("codexUsageBlock did not paint anything for nil token pointers")
	}
}

func regionHasNonZeroPixel(canvas *image.RGBA, bounds image.Rectangle) bool {
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if rgba(canvas.At(x, y)) != (color.RGBA{}) {
				return true
			}
		}
	}
	return false
}

func TestRenderDefaultsZeroNowToCurrentTime(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	frame := renderer.Render(View{})
	if frame.Bounds() != image.Rect(0, 0, Width, Height) {
		t.Fatalf("frame bounds = %v", frame.Bounds())
	}
}

func TestRenderDashboardShowsStateWarningOnLoadError(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	snapshot := baseSnapshot(now)
	footerRegion := image.Rect(0, footerBaseline-14, 700, footerBaseline+4)

	without := renderer.Render(View{Claude: &snapshot, Now: now, StaleAfter: 15 * time.Second, SessionCount: 1})
	withError := renderer.Render(View{
		Claude: &snapshot, Now: now, StaleAfter: 15 * time.Second, SessionCount: 1,
		LoadError: errors.New("partial read failure"),
	})
	if sameRegion(without, withError, footerRegion) {
		t.Fatal("a LoadError on the dashboard path (Claude present) did not change the footer")
	}
}

func TestRenderHeaderStaleAndFutureCapturedAt(t *testing.T) {
	renderer, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	pillRegion := image.Rect(contentRight-100, 18, contentRight, 46)

	fresh := baseSnapshot(now)
	fresh.CapturedAt = now
	freshFrame := renderer.Render(View{Claude: &fresh, Now: now, StaleAfter: time.Second})

	stale := baseSnapshot(now)
	stale.CapturedAt = now.Add(-10 * time.Second)
	staleFrame := renderer.Render(View{Claude: &stale, Now: now, StaleAfter: time.Second})
	if sameRegion(freshFrame, staleFrame, pillRegion) {
		t.Fatal("STALE pill did not differ from a LIVE pill")
	}

	// A CapturedAt after Now (clock skew or a stored future timestamp) must
	// clamp age to 0 instead of going negative, rendering identically to the
	// age==0 fresh case (still LIVE) rather than something else.
	future := baseSnapshot(now)
	future.CapturedAt = now.Add(time.Hour)
	futureFrame := renderer.Render(View{Claude: &future, Now: now, StaleAfter: time.Second})
	if !sameRegion(freshFrame, futureFrame, pillRegion) {
		t.Fatal("a CapturedAt in the future was not clamped to age 0 (still LIVE)")
	}
}

func TestDrawMascotSkipsFrameOnAnimationError(t *testing.T) {
	// A whitebox Renderer built directly (rather than via NewRenderer) with
	// a deliberately malformed rig source, so this exercises drawMascot's
	// "never take down the primary display" fallback without corrupting any
	// real embedded asset.
	renderer := &Renderer{icons: iconSet{idle: []byte("<svg><rect")}}
	canvas := image.NewRGBA(image.Rect(0, 0, 200, 200))
	renderer.drawMascot(canvas, 100, 100, time.Unix(0, 0), model.ActivityIdle)
	if !sameRegion(canvas, image.NewRGBA(canvas.Bounds()), canvas.Bounds()) {
		t.Fatal("drawMascot drew something despite a malformed animation source")
	}
}

func TestRenderAnimatedIconPropagatesEvaluateError(t *testing.T) {
	if _, err := renderAnimatedIcon([]byte("<svg><rect"), time.Unix(0, 0)); err == nil {
		t.Fatal("renderAnimatedIcon accepted a truncated/malformed SVG source")
	}
}

func TestFillRoundedBoundaryCases(t *testing.T) {
	canvas := image.NewRGBA(image.Rect(0, 0, 50, 50))

	// An empty rect must be a no-op, not a panic.
	fillRounded(canvas, image.Rect(10, 10, 10, 10), 5, red)
	if rgba(canvas.At(10, 10)) != (color.RGBA{}) {
		t.Fatal("fillRounded painted an empty rect")
	}

	// Zero radius is a plain filled rectangle, corners included.
	fillRounded(canvas, image.Rect(0, 0, 10, 10), 0, red)
	if rgba(canvas.At(0, 0)) != red {
		t.Fatal("fillRounded(radius=0) did not fill the corner")
	}
}

func TestBlendRingBoundaryCases(t *testing.T) {
	canvas := image.NewRGBA(image.Rect(0, 0, 50, 50))
	before := image.NewRGBA(canvas.Bounds())
	copy(before.Pix, canvas.Pix)

	blendRing(canvas, 25, 25, 10, 3, claudePeach, 0)
	if !sameRegion(canvas, before, canvas.Bounds()) {
		t.Fatal("blendRing(alpha=0) changed the canvas")
	}

	blendRing(canvas, 25, 25, 0, 3, claudePeach, 200)
	if !sameRegion(canvas, before, canvas.Bounds()) {
		t.Fatal("blendRing(radius=0) changed the canvas")
	}

	blendRing(canvas, 25, 25, -5, 3, claudePeach, 200)
	if !sameRegion(canvas, before, canvas.Bounds()) {
		t.Fatal("blendRing(negative radius) changed the canvas")
	}

	// A ring centered far outside the canvas must clip harmlessly instead of
	// panicking on an out-of-bounds pixel access.
	blendRing(canvas, 1000, 1000, 20, 5, claudePeach, 200)
	if !sameRegion(canvas, before, canvas.Bounds()) {
		t.Fatal("blendRing(off-canvas center) touched the canvas")
	}

	// A ring straddling the edge should still paint the on-canvas portion.
	blendRing(canvas, 0, 0, 10, 3, claudePeach, 200)
	if sameRegion(canvas, before, canvas.Bounds()) {
		t.Fatal("blendRing(straddling the edge) did not paint anything")
	}
}

func rgba(value color.Color) color.RGBA {
	red, green, blue, alpha := value.RGBA()
	return color.RGBA{R: uint8(red >> 8), G: uint8(green >> 8), B: uint8(blue >> 8), A: uint8(alpha >> 8)}
}

func sameRegion(left, right *image.RGBA, bounds image.Rectangle) bool {
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if rgba(left.At(x, y)) != rgba(right.At(x, y)) {
				return false
			}
		}
	}
	return true
}

func sameOutsideRegion(left, right *image.RGBA, excluded image.Rectangle) bool {
	for y := left.Bounds().Min.Y; y < left.Bounds().Max.Y; y++ {
		for x := left.Bounds().Min.X; x < left.Bounds().Max.X; x++ {
			if image.Pt(x, y).In(excluded) {
				continue
			}
			if rgba(left.At(x, y)) != rgba(right.At(x, y)) {
				return false
			}
		}
	}
	return true
}

func boolPointer(value bool) *bool { return &value }
