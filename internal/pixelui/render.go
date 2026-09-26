package pixelui

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"strings"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"github.com/dvgamerr/llm-status/internal/model"
	"github.com/dvgamerr/llm-status/internal/svganim"
	"github.com/dvgamerr/llm-status/internal/systeminfo"
	"github.com/dvgamerr/llm-status/internal/touch"
)

const (
	// Width is the dashboard's required framebuffer width in pixels.
	Width = 800
	// Height is the dashboard's required framebuffer height in pixels.
	Height = 480
)

// activityStaleAfter bounds how long a hook-reported working/waiting state is
// trusted. If the source machine crashes or a Stop hook is missed, the
// mascot falls back to idle instead of animating "working" forever.
const activityStaleAfter = 10 * time.Minute

// touchRippleLifetime is how long a tap's ripple stays visible — this is
// the official Raspberry Pi touch display, so a tap should visibly answer
// back even though the dashboard has no other interactive controls.
const touchRippleLifetime = 500 * time.Millisecond

var (
	backgroundTop = rgb(20, 19, 17)
	cardColor     = rgb(30, 28, 25)
	cardStrong    = rgb(39, 36, 32)
	trackColor    = rgb(60, 55, 49)
	textPrimary   = rgb(246, 242, 234)
	textSecondary = rgb(188, 178, 164)
	textFaint     = rgb(126, 117, 105)
	claudeOrange  = rgb(217, 119, 87)
	claudePeach   = rgb(235, 166, 135)
	green         = rgb(52, 211, 153)
	purple        = rgb(167, 139, 250)
	yellow        = rgb(250, 204, 21)
	red           = rgb(248, 113, 113)
)

// Layout: an 18px page margin, a fixed-width status rail (mascot + activity
// state + Pi health) on the left, and a metrics column on the right. The
// rail is the visual anchor on every screen — Nielsen's "visibility of
// system status" heuristic calls for the mascot's animation and any pending
// approval to be the first thing noticed, not buried in a corner icon.
const (
	pageMargin     = 18
	railLeft       = pageMargin
	railRight      = 232
	contentLeft    = 246
	contentRight   = Width - pageMargin
	sectionsTop    = 78
	sectionsBottom = 446
	footerBaseline = 469
)

// View is one immutable set of provider, host, and touch data to render.
type View struct {
	Claude       *model.Snapshot
	Codex        *model.Snapshot
	Stats        systeminfo.Stats
	Now          time.Time
	StaleAfter   time.Duration
	SessionCount int
	// TodayInputTokens/TodayOutputTokens are the combined Claude+Codex token
	// totals for every snapshot captured today (see model.TodayTokenTotals),
	// not just the latest Claude session — the INPUT/OUTPUT chips read as a
	// daily total across both tools rather than one session's own count.
	TodayInputTokens  int64
	TodayOutputTokens int64
	LoadError         error
	Touches           []touch.Point
}

// Renderer owns preloaded fonts and vector assets reused across frames.
type Renderer struct {
	regular12 font.Face
	regular13 font.Face
	regular14 font.Face
	regular16 font.Face
	bold13    font.Face
	bold16    font.Face
	bold18    font.Face
	bold22    font.Face
	bold30    font.Face
	bold44    font.Face
	icons     iconSet
}

// NewRenderer parses embedded fonts and rasterizes embedded SVG assets.
func NewRenderer() (*Renderer, error) {
	regular, err := opentype.Parse(goregular.TTF)
	if err != nil {
		return nil, fmt.Errorf("parse regular UI font: %w", err)
	}
	bold, err := opentype.Parse(gobold.TTF)
	if err != nil {
		return nil, fmt.Errorf("parse bold UI font: %w", err)
	}
	face := func(parsed *opentype.Font, size float64) (font.Face, error) {
		return opentype.NewFace(parsed, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	}
	r := &Renderer{}
	for _, target := range []struct {
		destination *font.Face
		parsed      *opentype.Font
		size        float64
	}{
		{&r.regular12, regular, 12}, {&r.regular13, regular, 13}, {&r.regular14, regular, 14},
		{&r.regular16, regular, 16}, {&r.bold13, bold, 13}, {&r.bold16, bold, 16},
		{&r.bold18, bold, 18}, {&r.bold22, bold, 22}, {&r.bold30, bold, 30}, {&r.bold44, bold, 44},
	} {
		created, createErr := face(target.parsed, target.size)
		if createErr != nil {
			return nil, fmt.Errorf("create UI font %.0f: %w", target.size, createErr)
		}
		*target.destination = created
	}
	r.icons, err = loadIconSet()
	if err != nil {
		return nil, err
	}
	return r, nil
}

// Render draws one complete 800x480 RGBA frame.
func (r *Renderer) Render(view View) *image.RGBA {
	canvas := image.NewRGBA(image.Rect(0, 0, Width, Height))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(backgroundTop), image.Point{}, draw.Src)
	if view.Now.IsZero() {
		view.Now = time.Now()
	}
	if view.StaleAfter <= 0 {
		view.StaleAfter = 15 * time.Second
	}
	activity := resolveActivity(view.Claude, view.Now)
	if view.Claude == nil {
		r.renderWaiting(canvas, view, activity)
	} else {
		r.renderDashboard(canvas, view, *view.Claude, activity)
	}
	renderTouchRipples(canvas, view.Touches, view.Now)
	return canvas
}

// renderTouchRipples draws a fading, expanding ring at each recent tap so
// the touchscreen visibly answers back even though nothing on this
// dashboard is otherwise interactive. Drawn last so it's never hidden
// under a card.
func renderTouchRipples(canvas *image.RGBA, points []touch.Point, now time.Time) {
	for _, point := range points {
		elapsed := now.Sub(point.At)
		if elapsed < 0 || elapsed > touchRippleLifetime {
			continue
		}
		progress := float64(elapsed) / float64(touchRippleLifetime)
		radius := 6 + int(46*progress)
		alpha := uint8(180 * (1 - progress))
		blendRing(canvas, point.X, point.Y, radius, max(2, 10-int(8*progress)), claudePeach, alpha)
	}
}

// contentSplit is where the open left panel ends and the framed Codex card
// begins. codexTop/codexBottom are shared by both the dashboard and waiting
// screens. codexCard's own content (label, model, mode, CONTEXT label,
// percent block) only runs ~176px tall, so the card is sized to that content
// height and then anchored to sectionsBottom — flush with the rail's own
// bottom edge — rather than left pinned to the top with dead card background
// below it.
const (
	contentSplit = contentLeft + 260
	codexBottom  = sectionsBottom - 6
	codexTop     = codexBottom - 195
)

func (r *Renderer) renderDashboard(canvas *image.RGBA, view View, snapshot model.Snapshot, activity string) {
	r.renderHeader(canvas, view)
	r.renderRail(canvas, image.Rect(railLeft, sectionsTop, railRight, sectionsBottom), activity, &snapshot, view.Stats, view.Now)
	r.renderLimitsRow(canvas, snapshot, view.Now)
	r.renderClaudePanel(canvas, snapshot, view.TodayInputTokens, view.TodayOutputTokens)
	r.codexCard(canvas, image.Rect(contentSplit+14, codexTop, contentRight, codexBottom), view.Codex)

	footer := fmt.Sprintf("AUTO  •  %d SESSION", max(1, view.SessionCount))
	if view.SessionCount != 1 {
		footer += "S"
	}
	if view.LoadError != nil {
		footer = "STATE WARNING  •  " + fitText(r.regular12, view.LoadError.Error(), 500)
	}
	r.text(canvas, r.bold13, claudeOrange, 21, footerBaseline, footer)
}

// renderLimitsRow spans the full content width (not just the Claude panel)
// so the space above the Codex card — which deliberately starts lower,
// below this row — is never a dead gap. "5 HOUR" and "7 DAY" are stacked as
// two full-width rows (not two half-width columns): each reset countdown
// needs to sit next to its own label, and a shared row would either crowd
// the countdown against the percentage or make it ambiguous which window
// it belongs to.
// Each row spans label(top) -> bar(top+14..22) -> percent+reset(top+46); the
// 72px gap between the two rows' tops leaves a relaxed gap after row one's
// percent+reset baseline before row two's label starts.
func (r *Renderer) renderLimitsRow(canvas *image.RGBA, snapshot model.Snapshot, now time.Time) {
	fullWidth := contentRight - contentLeft
	r.limitLine(canvas, contentLeft, 84, fullWidth, "5 HOUR", snapshot.RateLimits.FiveHour, snapshot.RateLimits, claudeOrange, now)
	r.limitLine(canvas, contentLeft, 156, fullWidth, "7 DAY", snapshot.RateLimits.SevenDay, snapshot.RateLimits, claudePeach, now)
}

// renderClaudePanel is deliberately card-less (open on the base background):
// the rail already frames "you", the Codex card already frames "the other
// tool", so this middle panel reads as this session's own numbers rather
// than another boxed widget competing for attention. The header still
// deliberately omits session/model identity (see CLAUDE.md); this panel is
// where that identity — model name and reasoning level — actually lives.
func (r *Renderer) renderClaudePanel(canvas *image.RGBA, snapshot model.Snapshot, todayInput, todayOutput int64) {
	panelWidth := contentSplit - contentLeft

	// Model + reasoning level get their own small header, styled exactly
	// like the Codex card's own model/mode block (bold16 name over a
	// regular12 secondary line) so the two halves of the screen read as
	// the same kind of "who's running this" identity, just without a card
	// frame — this panel stays deliberately card-less (see below). It sits
	// right after the rate-limit rows and before "CONTEXT WINDOW", which is
	// where the reference layout places the model name.
	r.text(canvas, r.bold16, textPrimary, contentLeft, 238, fitText(r.bold16, strings.ToUpper(modelLabel(snapshot)), panelWidth))
	r.text(canvas, r.regular12, textSecondary, contentLeft, 256, modePrimary(snapshot))

	// Extra breathing room between the reasoning-level line and "CONTEXT
	// WINDOW" — a wider gap than the rest of this panel's rhythm on purpose,
	// so the two identity lines above read as their own group.
	r.text(canvas, r.bold13, textSecondary, contentLeft, 290, "CONTEXT WINDOW")

	percentText := percentLabel(snapshot.Context.UsedPercentage)
	r.text(canvas, r.bold44, textPrimary, contentLeft, 328, percentText)
	usedX := contentLeft + font.MeasureString(r.bold44, percentText).Ceil() + 10
	r.text(canvas, r.bold13, claudeOrange, usedX, centeredBaseline(328, r.bold44, r.bold13), "USED")

	r.text(canvas, r.regular12, textFaint, contentLeft, 346, fitText(r.regular12, contextFraction(snapshot.Context), panelWidth))
	progress(canvas, image.Rect(contentLeft, 354, contentSplit, 362), percentValue(snapshot.Context.UsedPercentage), claudeOrange)

	// Bottom-anchored to the same baseline as the Codex card (codexBottom)
	// instead of sitting right under the bar with dead space beneath —
	// both boxes now hug the rail's own bottom edge.
	chipWidth := (panelWidth - 14) / 2
	chipTop := codexBottom - 58
	chipBounds := image.Rect(contentLeft, chipTop, contentLeft+chipWidth, codexBottom)
	metricChip(canvas, r, chipBounds, "INPUT TODAY", tokenLabel(&todayInput), claudePeach)
	chipBounds = image.Rect(contentLeft+chipWidth+14, chipTop, contentSplit, codexBottom)
	metricChip(canvas, r, chipBounds, "OUTPUT TODAY", tokenLabel(&todayOutput), purple)
}

func (r *Renderer) renderWaiting(canvas *image.RGBA, view View, activity string) {
	r.renderHeader(canvas, view)
	r.renderRail(canvas, image.Rect(railLeft, sectionsTop, railRight, sectionsBottom), activity, nil, view.Stats, view.Now)

	panelWidth := contentSplit - contentLeft
	center := contentLeft + panelWidth/2
	r.textCentered(canvas, r.bold22, textPrimary, center, 150, "WAITING")
	r.textCentered(canvas, r.regular13, textSecondary, center, 174, fitText(r.regular13, "for the next statusLine event", panelWidth-20))
	progress(canvas, image.Rect(contentLeft+20, 202, contentSplit-20, 212), animationPercent(view.Now), claudeOrange)
	r.textCentered(canvas, r.regular12, textFaint, center, 280, fitText(r.regular12, "start a session on the source machine", panelWidth-20))

	r.codexCard(canvas, image.Rect(contentSplit+14, codexTop, contentRight, codexBottom), view.Codex)

	r.text(canvas, r.bold13, claudeOrange, 21, footerBaseline, "CLAUDE PRIMARY  •  WAITING")
	r.textRight(canvas, r.regular12, textFaint, contentRight, footerBaseline, "FRAMEBUFFER 800×480")
}

// limitLine draws one open (no card) rate-limit indicator as a full-width
// row: the label alone, then the bar, then the big percentage with its
// reset countdown on the same baseline (percent left, countdown right) —
// the bar reads as an at-a-glance gauge right under the label, with the
// exact numbers as supporting detail below it, rather than the numbers
// appearing before the gauge they describe.
// The reset countdown recomputes from the stored epoch against `now` every
// frame, so it counts down live without any separate timer state; once
// that epoch passes, the window has renewed server-side and the last known
// percentage is no longer true, so this shows unavailable instead of a
// stale number frozen at whatever it was when it expired.
func (r *Renderer) limitLine(canvas *image.RGBA, x, top, width int, label string, window model.RateWindow, limits model.RateLimits, accent color.RGBA, now time.Time) {
	r.text(canvas, r.bold13, textSecondary, x, top, label+" LIMIT")

	resetText := resetLabelShort(window.ResetsAt, now)
	expired := window.ResetsAt != nil && *window.ResetsAt > 0 && now.Unix() >= *window.ResetsAt
	if expired {
		resetText = "reset — awaiting new data"
	}
	percentBaseline := top + 46
	r.textRight(canvas, r.regular12, textFaint, x+width, centeredBaseline(percentBaseline, r.bold22, r.regular12), fitText(r.regular12, resetText, width/2))

	switch {
	case window.UsedPercentage == nil && limits.Unlimited != nil && *limits.Unlimited:
		progress(canvas, image.Rect(x, top+14, x+width, top+22), 100, green)
		r.text(canvas, r.bold22, green, x, percentBaseline, "UNMETERED")
	case expired:
		progress(canvas, image.Rect(x, top+14, x+width, top+22), 0, accent)
		r.text(canvas, r.bold22, textPrimary, x, percentBaseline, "--")
	default:
		pct := percentValue(window.UsedPercentage)
		progress(canvas, image.Rect(x, top+14, x+width, top+22), pct, thresholdColor(pct, accent))
		r.text(canvas, r.bold22, textPrimary, x, percentBaseline, percentLabel(window.UsedPercentage))
	}
}

// renderHeader intentionally contains no model, session ID/name, or effort:
// its top-left identity is only the bare Anthropic mark and CLAUDE label.
func (r *Renderer) renderHeader(canvas *image.RGBA, view View) {
	const logoCenterY = 36
	r.drawLogoMark(canvas, 33, logoCenterY)
	// "CLAUDE" is centered on the logo mark's own vertical center rather than
	// a hardcoded baseline — the logo is a fixed-size icon, not text, so its
	// visual middle doesn't move with font metrics the way a same-baseline
	// text pairing would (see centeredBaseline for that case).
	r.text(canvas, r.bold22, textPrimary, 54, logoCenterY+r.bold22.Metrics().Ascent.Ceil()/2, "CLAUDE")

	if view.Claude != nil {
		age := view.Now.Sub(view.Claude.CapturedAt)
		if age < 0 {
			age = 0
		}
		liveText, liveColor := "LIVE", green
		if age > view.StaleAfter {
			liveText, liveColor = "STALE", red
		}
		// Size the pill to the label instead of a fixed width: "STALE" is
		// wider than "LIVE".
		textWidth := font.MeasureString(r.bold13, liveText).Ceil()
		pillWidth := textWidth + 34
		pillLeft := contentRight - pillWidth
		fillRounded(canvas, image.Rect(pillLeft, 18, pillLeft+pillWidth, 46), 14, withAlpha(liveColor, 36))
		fillCircle(canvas, pillLeft+15, 32, 4, liveColor)
		r.text(canvas, r.bold13, liveColor, pillLeft+26, 37, liveText)
	}
}

// renderRail is the dashboard's focal point: a large animated mascot whose
// motion communicates whether the source session is working, idle, or
// blocked on a permission prompt, so that state is legible from across a
// room without reading any text.
func (r *Renderer) renderRail(canvas *image.RGBA, bounds image.Rectangle, activityState string, snapshot *model.Snapshot, stats systeminfo.Stats, now time.Time) {
	if activityState == model.ActivityWaitingApproval {
		fillRounded(canvas, bounds.Inset(-3), 23, withAlpha(yellow, 70))
	}
	card(canvas, bounds, 20)

	centerX := bounds.Min.X + bounds.Dx()/2
	radius := railIconSize / 2
	mascotY := bounds.Min.Y + 40 + radius
	r.drawMascot(canvas, centerX, mascotY, now, activityState)

	captionTop := mascotY + radius + 34
	if snapshot != nil {
		r.textCentered(canvas, r.regular12, textFaint, centerX, captionTop, activityCaption(*snapshot, activityState, now))
	} else {
		r.textCentered(canvas, r.regular13, textFaint, centerX, captionTop, "no active session")
	}

	dividerY := captionTop + 19
	fillRounded(canvas, image.Rect(bounds.Min.X+24, dividerY, bounds.Max.X-24, dividerY+2), 1, trackColor)

	healthTop := dividerY + 26
	r.text(canvas, r.bold13, textSecondary, bounds.Min.X+22, healthTop, "PI HEALTH")
	r.piHealthBox(canvas, image.Rect(bounds.Min.X+22, healthTop+14, bounds.Max.X-22, healthTop+70), stats)
}

// piHealthBox is one bordered row with three columns (CPU, MEM, TEMP),
// label above value, divided by thin vertical rules.
func (r *Renderer) piHealthBox(canvas *image.RGBA, bounds image.Rectangle, stats systeminfo.Stats) {
	fillRounded(canvas, bounds, 12, cardStrong)
	segmentWidth := bounds.Dx() / 3
	labels := [3]string{"CPU", "MEM", "TEMP"}
	values := [3]string{
		floatLabel(stats.CPUPercent, "%.0f%%"),
		memoryLabel(stats.MemoryUsedBytes, stats.MemoryTotalBytes),
		floatLabel(stats.TemperatureC, "%.0f°C"),
	}
	for index := range labels {
		centerX := bounds.Min.X + segmentWidth*index + segmentWidth/2
		if index > 0 {
			x := bounds.Min.X + segmentWidth*index
			fillRounded(canvas, image.Rect(x, bounds.Min.Y+8, x+1, bounds.Max.Y-8), 0, trackColor)
		}
		r.textCentered(canvas, r.regular12, textFaint, centerX, bounds.Min.Y+22, labels[index])
		r.textCentered(canvas, r.bold13, textPrimary, centerX, bounds.Min.Y+44, fitText(r.bold13, values[index], segmentWidth-6))
	}
}

// codexCard is the one framed card on screen: it deliberately looks like a
// boxed "other tool" widget next to the Claude panel's open background. It
// stays confined to the middle+bottom of the content area — session/model
// and context only, no rate-limit rows of its own.
// codexCard intentionally omits session name/id — Codex is "the other
// tool" here, so only what model/reasoning it's running and how much
// context it's using matters on this dashboard, not which thread.
func (r *Renderer) codexCard(canvas *image.RGBA, bounds image.Rectangle, snapshot *model.Snapshot) {
	card(canvas, bounds, 19)
	r.text(canvas, r.bold13, green, bounds.Min.X+18, bounds.Min.Y+24, "CODEX")
	if snapshot == nil {
		r.text(canvas, r.bold16, textPrimary, bounds.Min.X+18, bounds.Min.Y+52, "NO SESSION")
		r.text(canvas, r.regular12, textFaint, bounds.Min.X+18, bounds.Min.Y+76, "usage unavailable")
		return
	}

	innerWidth := bounds.Dx() - 36
	r.text(canvas, r.bold18, textPrimary, bounds.Min.X+18, bounds.Min.Y+52, fitText(r.bold18, strings.ToUpper(modelLabel(*snapshot)), innerWidth))
	r.text(canvas, r.regular12, textSecondary, bounds.Min.X+18, bounds.Min.Y+74, modePrimary(*snapshot))

	// A thin divider (same treatment as the rail's own dividerY) separates
	// "who" (model/mode identity) from "how much" (cost/tokens) instead of
	// the two blocks running together with only a label between them.
	dividerY := bounds.Min.Y + 92
	fillRounded(canvas, image.Rect(bounds.Min.X+18, dividerY, bounds.Max.X-18, dividerY+2), 1, trackColor)

	r.text(canvas, r.bold13, textSecondary, bounds.Min.X+18, bounds.Min.Y+118, "USAGE")
	r.codexUsageBlock(canvas, bounds.Min.X+18, bounds.Min.Y+150, innerWidth, *snapshot)
}

// codexUsageBlock replaces the context-percentage block Codex used to share
// with the Claude panel: Codex's card cares about spend, not context
// headroom (that's the Claude panel's own concern), so this leads with the
// estimated dollar cost — computed from reported input/output tokens and
// codexPricingTable's per-model rate, since providers price models
// differently — with the raw token counts as supporting detail beneath it.
func (r *Renderer) codexUsageBlock(canvas *image.RGBA, x, top, width int, snapshot model.Snapshot) {
	var inputTokens, outputTokens int64
	if value := snapshot.Context.InputTokens(); value != nil {
		inputTokens = *value
	}
	if value := snapshot.Context.OutputTokens(); value != nil {
		outputTokens = *value
	}
	cost, exact := codexEstimatedCostUSD(snapshot.Model.ID, inputTokens, outputTokens)

	costText := fmt.Sprintf("$%.2f", cost)
	r.text(canvas, r.bold30, textPrimary, x, top, costText)
	tagX := x + font.MeasureString(r.bold30, costText).Ceil() + 10
	tag := "EST. COST"
	if !exact {
		tag = "EST. COST*"
	}
	r.text(canvas, r.bold13, green, tagX, centeredBaseline(top, r.bold30, r.bold13), tag)

	total := inputTokens + outputTokens
	detail := fmt.Sprintf("%s tokens  ·  %s in / %s out", tokenLabel(&total), tokenLabel(&inputTokens), tokenLabel(&outputTokens))
	r.text(canvas, r.regular12, textFaint, x, top+26, fitText(r.regular12, detail, width))
}

func metricChip(canvas *image.RGBA, r *Renderer, bounds image.Rectangle, label, value string, accent color.RGBA) {
	fillRounded(canvas, bounds, 13, cardStrong)
	r.text(canvas, r.bold13, accent, bounds.Min.X+14, bounds.Min.Y+24, label)
	r.text(canvas, r.bold18, textPrimary, bounds.Min.X+14, bounds.Min.Y+50, fitText(r.bold18, value, bounds.Dx()-24))
}

func card(canvas *image.RGBA, bounds image.Rectangle, radius int) {
	fillRounded(canvas, bounds.Add(image.Pt(0, 3)), radius, rgb(4, 7, 14))
	fillRounded(canvas, bounds, radius, cardColor)
}

func progress(canvas *image.RGBA, bounds image.Rectangle, percentage float64, accent color.RGBA) {
	fillRounded(canvas, bounds, bounds.Dy()/2, trackColor)
	percentage = min(100, max(0, percentage))
	fillWidth := int(math.Round(float64(bounds.Dx()) * percentage / 100))
	if fillWidth > 0 {
		fillRounded(canvas, image.Rect(bounds.Min.X, bounds.Min.Y, bounds.Min.X+min(bounds.Dx(), max(bounds.Dy(), fillWidth)), bounds.Max.Y), bounds.Dy()/2, accent)
	}
}

func fillRounded(canvas *image.RGBA, bounds image.Rectangle, radius int, fill color.RGBA) {
	if bounds.Empty() {
		return
	}
	radius = min(radius, min(bounds.Dx()/2, bounds.Dy()/2))
	fillSource := image.NewUniform(fill)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		inset := 0
		if radius > 0 {
			var dy float64
			switch {
			case y < bounds.Min.Y+radius:
				dy = float64(bounds.Min.Y+radius-y) - 0.5
			case y >= bounds.Max.Y-radius:
				dy = float64(y-(bounds.Max.Y-radius-1)) - 0.5
			}
			if dy > 0 {
				inset = radius - int(math.Sqrt(max(0, float64(radius*radius)-dy*dy)))
			}
		}
		draw.Draw(canvas, image.Rect(bounds.Min.X+inset, y, bounds.Max.X-inset, y+1), fillSource, image.Point{}, draw.Src)
	}
}

func fillCircle(canvas *image.RGBA, centerX, centerY, radius int, fill color.RGBA) {
	fillRounded(canvas, image.Rect(centerX-radius, centerY-radius, centerX+radius, centerY+radius), radius, fill)
}

// blendRing alpha-blends a ring (annulus) of the given color and alpha over
// whatever is already on canvas, unlike withAlpha which only blends toward
// a fixed card background. Used for the touch ripple, which can appear over
// the rail, the open panel, or the Codex card.
func blendRing(canvas *image.RGBA, centerX, centerY, radius, thickness int, tint color.RGBA, alpha uint8) {
	if alpha == 0 || radius <= 0 {
		return
	}
	inner := radius - thickness
	bounds := image.Rect(centerX-radius, centerY-radius, centerX+radius+1, centerY+radius+1).Intersect(canvas.Bounds())
	factor := float64(alpha) / 255
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		dy := y - centerY
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			dx := x - centerX
			distanceSquared := dx*dx + dy*dy
			if distanceSquared > radius*radius || (inner > 0 && distanceSquared < inner*inner) {
				continue
			}
			existing := canvas.RGBAAt(x, y)
			canvas.SetRGBA(x, y, rgb(
				uint8(float64(tint.R)*factor+float64(existing.R)*(1-factor)),
				uint8(float64(tint.G)*factor+float64(existing.G)*(1-factor)),
				uint8(float64(tint.B)*factor+float64(existing.B)*(1-factor)),
			))
		}
	}
}

// waitingApprovalPeriod is the beat of the one remaining pose+alternation
// state (see mascotPoseForActivity/mascotFrameForActivity below) — every
// other state now plays back a rigged SVG animation instead (see
// internal/svganim), timed by each rig's own SMIL dur/values.
const waitingApprovalPeriod = 780 * time.Millisecond

// mascotPose is a nearest-neighbor transform applied to the rasterized SVG.
// Keeping it separate from the backdrop makes the invariant explicit: only
// this pose changes from frame to frame.
type mascotPose struct {
	xOffset int
	yOffset int
	width   int
	height  int
}

// mascotPoseForActivity computes waiting_approval's shake — the only state
// left on a single static pose plus procedural motion; every other state's
// motion comes from a rigged SVG's own SMIL animation instead (see
// animatedSVGForActivity/renderAnimatedIcon), so drawMascot never calls this
// for them.
func mascotPoseForActivity(now time.Time) mascotPose {
	periodMS := waitingApprovalPeriod.Milliseconds()
	phase := float64(now.UnixMilli()%periodMS) / float64(periodMS) * 2 * math.Pi
	// A tight three-beat shake matches the exclamation artwork and reads as
	// urgent even from across the room.
	shake := 3 * phase
	return mascotPose{
		width:   railIconSize,
		height:  railIconSize,
		xOffset: int(math.Round(4 * math.Sin(shake))),
		yOffset: -int(math.Round(2 * math.Abs(math.Sin(shake)))),
	}
}

// resolveActivity turns a possibly-stale, possibly-absent Activity field
// into the state the mascot should animate right now. When no snapshot
// exists, or when hooks haven't been installed yet, it degrades gracefully
// instead of leaving the mascot in a stuck or undefined state.
//
// A pending permission prompt always wins (a decision the user must make is
// more important than "how many subagents are running"). Otherwise, while
// Subagents is positive it takes priority over whatever the parent
// session's own last tool event happened to be — matching this project's
// reference for the mascot behavior, where subagent activity visually
// dominates the parent's own state until every subagent finishes.
func resolveActivity(snapshot *model.Snapshot, now time.Time) string {
	if snapshot == nil {
		return model.ActivityIdle
	}
	// Freshness is keyed on UpdatedAt, not on State being set: a session
	// whose very first merged hook was SubagentStart has Subagents>0 with
	// State still "" (activity.Run always stamps UpdatedAt when anything
	// matched, even a subagent-only delta), and that count must still win
	// below instead of silently falling through to the statusLine-freshness
	// fallback meant for sessions hooks have never touched at all.
	if !snapshot.Activity.UpdatedAt.IsZero() {
		age := now.Sub(snapshot.Activity.UpdatedAt)
		if age < 0 || age > activityStaleAfter {
			return model.ActivityIdle
		}
		if snapshot.Activity.State == model.ActivityWaitingApproval {
			return model.ActivityWaitingApproval
		}
		switch {
		case snapshot.Activity.Subagents >= 2:
			return model.ActivitySubagentMany
		case snapshot.Activity.Subagents == 1:
			return model.ActivitySubagentOne
		}
		if snapshot.Activity.State != "" {
			return snapshot.Activity.State
		}
	}
	age := now.Sub(snapshot.CapturedAt)
	if age >= 0 && age < 3*time.Second {
		return model.ActivityWorking
	}
	return model.ActivityIdle
}

func activityCaption(snapshot model.Snapshot, state string, now time.Time) string {
	if snapshot.Activity.State == "" || snapshot.Activity.UpdatedAt.IsZero() {
		return "waiting for the next hook event"
	}
	elapsed := now.Sub(snapshot.Activity.UpdatedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	switch state {
	case model.ActivityWorking, model.ActivityTyping:
		return "typing for " + durationLabel(elapsed)
	case model.ActivityThinking:
		return "thinking for " + durationLabel(elapsed)
	case model.ActivityBuilding:
		return "building for " + durationLabel(elapsed)
	case model.ActivitySubagentOne:
		return "1 subagent for " + durationLabel(elapsed)
	case model.ActivitySubagentMany:
		return "subagents for " + durationLabel(elapsed)
	case model.ActivityWaitingApproval:
		return "waiting " + durationLabel(elapsed)
	default:
		return "idle for " + durationLabel(elapsed)
	}
}

func (r *Renderer) drawMascot(canvas *image.RGBA, centerX, centerY int, now time.Time, state string) {
	if source := animatedSVGForActivity(&r.icons, state); source != nil {
		icon, err := renderAnimatedIcon(source, now)
		if err != nil {
			// A malformed or unrecognized animation must never take down the
			// primary display — same "secondary feature must never break the
			// primary display" pattern as touch and resolveActivity's
			// fallbacks. Skip drawing the mascot this tick and try again on
			// the next one.
			return
		}
		drawIconScaledCentered(canvas, icon, centerX, centerY, railIconSize, railIconSize)
		return
	}

	// Only waiting_approval reaches here — see animatedSVGForActivity.
	icon := r.icons.waitingApproval[mascotFrameForActivity(now)]
	pose := mascotPoseForActivity(now)
	drawIconScaledCentered(canvas, icon, centerX+pose.xOffset, centerY+pose.yOffset, pose.width, pose.height)
}

// animatedSVGForActivity returns the rigged-SVG source for state.
// waiting_approval is the only value that returns nil, signaling drawMascot
// to fall back to the legacy 2-frame pose+alternation system instead (see
// mascotPoseForActivity) — every other value, including an empty or
// otherwise unrecognized state, defaults to idle.
//
// ActivityWorking is the pre-Typing/Building legacy value (see its doc
// comment in internal/model/snapshot.go) and renders on the same rig as
// Typing, not the original static Clawd Coding pose.
func animatedSVGForActivity(icons *iconSet, state string) []byte {
	switch state {
	case model.ActivityWorking, model.ActivityTyping:
		return icons.typing
	case model.ActivityThinking:
		return icons.thinking
	case model.ActivityBuilding:
		return icons.building
	case model.ActivitySubagentOne:
		return icons.subagentOne
	case model.ActivitySubagentMany:
		return icons.subagentMany
	case model.ActivityWaitingApproval:
		return nil
	default:
		return icons.idle
	}
}

// renderAnimatedIcon bakes source's SMIL animation for the instant now (see
// internal/svganim) and rasterizes the result. This runs every render tick
// rather than once at startup, since the pose is a continuous function of
// time instead of a discrete frame index.
func renderAnimatedIcon(source []byte, now time.Time) (*image.RGBA, error) {
	posed, err := svganim.Evaluate(source, now)
	if err != nil {
		return nil, err
	}
	return rasterizeSVG(posed, railIconSize, railIconSize)
}

// mascotFrameForActivity picks between the two rasterized SVG poses for the
// current state: a plain square-wave alternation on each state's own period,
// so the second pose (typing hands, drifted Zzz, pulsing alert dot) reads as
// a deliberate beat rather than a stray flicker.
func mascotFrameForActivity(now time.Time) int {
	periodMS := waitingApprovalPeriod.Milliseconds()
	if (now.UnixMilli()%periodMS)*2 >= periodMS {
		return 1
	}
	return 0
}

// drawLogoMark is the white Anthropic mark, used only in the top-left header;
// the Clawd artwork remains exclusive to the activity rail.
func (r *Renderer) drawLogoMark(canvas *image.RGBA, centerX, centerY int) {
	drawIconCentered(canvas, r.icons.logo, centerX, centerY)
}

func animationPercent(now time.Time) float64 {
	phase := float64(now.UnixMilli()%2000) / 2000 * 2 * math.Pi
	return 35 + (math.Sin(phase)+1)*15
}

func (r *Renderer) text(canvas *image.RGBA, face font.Face, fill color.RGBA, x, baseline int, value string) {
	drawer := font.Drawer{Dst: canvas, Src: image.NewUniform(fill), Face: face, Dot: fixed.P(x, baseline)}
	drawer.DrawString(value)
}

func (r *Renderer) textRight(canvas *image.RGBA, face font.Face, fill color.RGBA, right, baseline int, value string) {
	r.text(canvas, face, fill, right-font.MeasureString(face, value).Ceil(), baseline, value)
}

func (r *Renderer) textCentered(canvas *image.RGBA, face font.Face, fill color.RGBA, center, baseline int, value string) {
	r.text(canvas, face, fill, center-font.MeasureString(face, value).Ceil()/2, baseline, value)
}

// centeredBaseline returns the baseline for smallFace text that shares a
// visual row with bigFace text at bigBaseline, so the two are centered on
// each other's vertical middle instead of sharing a literal baseline — two
// different-sized faces on one baseline reads as the small text pinned to
// the big glyph's bottom edge (e.g. "USED" glued to the foot of a 44pt "%"),
// not sitting beside its center.
func centeredBaseline(bigBaseline int, bigFace, smallFace font.Face) int {
	return bigBaseline - (bigFace.Metrics().Ascent.Ceil()-smallFace.Metrics().Ascent.Ceil())/2
}

func fitText(face font.Face, value string, maxWidth int) string {
	value = strings.TrimSpace(value)
	if maxWidth <= 0 {
		return ""
	}
	if font.MeasureString(face, value).Ceil() <= maxWidth {
		return value
	}
	runes := []rune(value)
	for len(runes) > 1 {
		runes = runes[:len(runes)-1]
		candidate := string(runes) + "…"
		if font.MeasureString(face, candidate).Ceil() <= maxWidth {
			return candidate
		}
	}
	return "…"
}

func providerName(provider string) string {
	if model.CanonicalProvider(provider) == model.ProviderCodex {
		return "CODEX"
	}
	return "CLAUDE"
}

func percentValue(value *float64) float64 {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) {
		return 0
	}
	return min(100, max(0, *value))
}

func percentLabel(value *float64) string {
	if value == nil {
		return "--"
	}
	return fmt.Sprintf("%.0f%%", percentValue(value))
}

func thresholdColor(value float64, fallback color.RGBA) color.RGBA {
	switch {
	case value >= 90:
		return red
	case value >= 70:
		return yellow
	default:
		return fallback
	}
}

func contextFraction(context model.Context) string {
	if context.WindowSize == nil {
		return "window unavailable"
	}
	used := int64(0)
	if input := context.InputTokens(); input != nil {
		used += *input
	}
	if output := context.OutputTokens(); output != nil {
		used += *output
	}
	if used == 0 && context.UsedPercentage != nil {
		used = int64(math.Round(float64(*context.WindowSize) * percentValue(context.UsedPercentage) / 100))
	}
	return tokenLabel(&used) + " / " + tokenLabel(context.WindowSize)
}

func tokenLabel(value *int64) string {
	if value == nil {
		return "--"
	}
	switch {
	case *value >= 1_000_000:
		return trimZero(fmt.Sprintf("%.1fM", float64(*value)/1_000_000))
	case *value >= 1_000:
		return trimZero(fmt.Sprintf("%.1fK", float64(*value)/1_000))
	default:
		return fmt.Sprintf("%d", *value)
	}
}

func trimZero(value string) string {
	value = strings.Replace(value, ".0M", "M", 1)
	return strings.Replace(value, ".0K", "K", 1)
}

func durationLabel(value time.Duration) string {
	if value < time.Minute {
		return fmt.Sprintf("%ds", int(value/time.Second))
	}
	if value < time.Hour {
		return fmt.Sprintf("%dm", int(value/time.Minute))
	}
	if value < 24*time.Hour {
		return fmt.Sprintf("%dh %dm", int(value/time.Hour), int(value%time.Hour/time.Minute))
	}
	return fmt.Sprintf("%dd %dh", int(value/(24*time.Hour)), int(value%(24*time.Hour)/time.Hour))
}

// resetLabelShort formats a rate-limit reset countdown for the compact
// stacked limitLine layout, where width is at a premium.
func resetLabelShort(epoch *int64, now time.Time) string {
	if epoch == nil || *epoch <= 0 {
		return "reset unavailable"
	}
	remaining := time.Unix(*epoch, 0).Sub(now)
	if remaining <= 0 {
		return "reset due"
	}
	return durationLabel(remaining) + " left"
}

func modelLabel(snapshot model.Snapshot) string {
	if snapshot.Model.DisplayName != "" {
		return snapshot.Model.DisplayName
	}
	if snapshot.Model.ID != "" {
		return snapshot.Model.ID
	}
	return providerName(snapshot.Provider)
}

func floatLabel(value *float64, format string) string {
	if value == nil {
		return "--"
	}
	return fmt.Sprintf(format, *value)
}

func memoryLabel(used, total *uint64) string {
	if used == nil || total == nil {
		return "--"
	}
	return fmt.Sprintf("%.1f/%.1fG", float64(*used)/(1<<30), float64(*total)/(1<<30))
}

func modePrimary(snapshot model.Snapshot) string {
	if snapshot.Effort != "" {
		return strings.ToUpper(snapshot.Effort) + " EFFORT"
	}
	if snapshot.ThinkingEnabled != nil && *snapshot.ThinkingEnabled {
		return "THINKING ON"
	}
	return "STANDARD"
}

func rgb(red, green, blue uint8) color.RGBA {
	return color.RGBA{R: red, G: green, B: blue, A: 255}
}

func withAlpha(value color.RGBA, alpha uint8) color.RGBA {
	base := cardColor
	factor := float64(alpha) / 255
	return rgb(
		uint8(float64(value.R)*factor+float64(base.R)*(1-factor)),
		uint8(float64(value.G)*factor+float64(base.G)*(1-factor)),
		uint8(float64(value.B)*factor+float64(base.B)*(1-factor)),
	)
}
