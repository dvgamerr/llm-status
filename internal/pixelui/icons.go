// Package pixelui renders the native fixed-pixel framebuffer dashboard.
package pixelui

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/draw"
	"time"

	"github.com/dvgamerr/llm-status/internal/svganim"
	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
	xdraw "golang.org/x/image/draw"
)

const railIconSize = 150

// The SVGs stay as the source assets and are rasterized once when the
// renderer starts. That keeps the framebuffer render loop allocation-free
// while preserving one scalable source for both the 30 px header mark and
// the 120 px activity artwork.
//
//go:embed assets/anthropic.svg
var anthropicLogoSVG []byte

//go:embed assets/clawd-exclamation-mark.svg
var clawdExclamationMarkSVG []byte

//go:embed assets/clawd-exclamation-mark-2.svg
var clawdExclamationMark2SVG []byte

// These six states are each a single rigged SVG — a static body plus a few
// named groups animated with real SMIL (<animate>/<animateTransform>,
// see internal/svganim) instead of a Go-managed array of pre-rasterized
// traced-GIF frames. drawMascot evaluates and rasterizes them fresh every
// render tick (see resolveActivity/drawMascot in render.go), since the pose
// is a continuous function of time rather than a discrete frame index.
//
//go:embed assets/clawd-idle.svg
var clawdIdleSVG []byte

//go:embed assets/clawd-thinking.svg
var clawdThinkingSVG []byte

//go:embed assets/clawd-typing.svg
var clawdTypingSVG []byte

//go:embed assets/clawd-building.svg
var clawdBuildingSVG []byte

//go:embed assets/clawd-headphones-groove.svg
var clawdHeadphonesGrooveSVG []byte

//go:embed assets/clawd-juggling.svg
var clawdJugglingSVG []byte

// waiting_approval is the only state left on the original 2-rasterized-
// frame alternation system (typing hands, drifted Zzz, a pulsing alert dot
// baked into the artwork itself) — every other state now plays back a
// rigged SVG animation instead (see svganim.Evaluate).
type iconSet struct {
	logo            *image.RGBA
	waitingApproval [2]*image.RGBA
	idle            []byte
	thinking        []byte
	typing          []byte
	building        []byte
	subagentOne     []byte
	subagentMany    []byte
}

func loadIconSet() (iconSet, error) {
	load := func(name string, source []byte, width, height int) (*image.RGBA, error) {
		icon, err := rasterizeSVG(source, width, height)
		if err != nil {
			return nil, fmt.Errorf("render %s SVG: %w", name, err)
		}
		return icon, nil
	}

	var set iconSet
	var err error
	if set.logo, err = load("Anthropic logo", anthropicLogoSVG, 30, 30); err != nil {
		return iconSet{}, err
	}
	if set.waitingApproval[0], err = load("Clawd Exclamation Mark", clawdExclamationMarkSVG, railIconSize, railIconSize); err != nil {
		return iconSet{}, err
	}
	if set.waitingApproval[1], err = load("Clawd Exclamation Mark frame 2", clawdExclamationMark2SVG, railIconSize, railIconSize); err != nil {
		return iconSet{}, err
	}

	rigs := []struct {
		name        string
		source      []byte
		destination *[]byte
	}{
		{"Clawd Idle", clawdIdleSVG, &set.idle},
		{"Clawd Thinking", clawdThinkingSVG, &set.thinking},
		{"Clawd Typing", clawdTypingSVG, &set.typing},
		{"Clawd Building", clawdBuildingSVG, &set.building},
		{"Clawd Headphones Groove", clawdHeadphonesGrooveSVG, &set.subagentOne},
		{"Clawd Juggling", clawdJugglingSVG, &set.subagentMany},
	}
	for _, rig := range rigs {
		if err := smokeTestRig(rig.name, rig.source); err != nil {
			return iconSet{}, err
		}
		*rig.destination = rig.source
	}
	return set, nil
}

// smokeTestRig evaluates and rasterizes a rig once at startup, discarding
// the result — the point is to fail fast on a malformed asset at boot
// instead of on the first render tick.
func smokeTestRig(name string, source []byte) error {
	posed, err := svganim.Evaluate(source, time.Unix(0, 0))
	if err != nil {
		return fmt.Errorf("evaluate %s rig: %w", name, err)
	}
	if _, err := rasterizeSVG(posed, railIconSize, railIconSize); err != nil {
		return fmt.Errorf("render %s rig: %w", name, err)
	}
	return nil
}

func rasterizeSVG(source []byte, width, height int) (*image.RGBA, error) {
	icon, err := oksvg.ReadIconStream(bytes.NewReader(source))
	if err != nil {
		return nil, err
	}
	icon.SetTarget(0, 0, float64(width), float64(height))
	destination := image.NewRGBA(image.Rect(0, 0, width, height))
	scanner := rasterx.NewScannerGV(width, height, destination, destination.Bounds())
	icon.Draw(rasterx.NewDasher(width, height, scanner), 1)
	return destination, nil
}

func drawIconCentered(canvas *image.RGBA, icon *image.RGBA, centerX, centerY int) {
	drawIconScaledCentered(canvas, icon, centerX, centerY, icon.Bounds().Dx(), icon.Bounds().Dy())
}

func drawIconScaledCentered(canvas *image.RGBA, icon *image.RGBA, centerX, centerY, width, height int) {
	if width <= 0 || height <= 0 {
		return
	}
	destination := iconDestination(centerX, centerY, width, height)
	bounds := icon.Bounds()
	if width == bounds.Dx() && height == bounds.Dy() {
		draw.Draw(canvas, destination, icon, bounds.Min, draw.Over)
		return
	}
	// Keep the deliberately blocky Clawd artwork crisp while it breathes.
	// Bilinear scaling makes the one-pixel SVG edges look blurry on RGB565.
	xdraw.NearestNeighbor.Scale(canvas, destination, icon, bounds, draw.Over, nil)
}

func iconDestination(centerX, centerY, width, height int) image.Rectangle {
	left := centerX - width/2
	top := centerY - height/2
	return image.Rect(left, top, left+width, top+height)
}
