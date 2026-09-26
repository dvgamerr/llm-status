package pixelui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"time"

	"github.com/dvgamerr/llm-status/internal/model"
	"github.com/dvgamerr/llm-status/internal/systeminfo"
	"github.com/dvgamerr/llm-status/internal/touch"
)

// SnapshotLoader supplies sanitized snapshots to the renderer loop.
type SnapshotLoader interface {
	LoadAll() ([]model.Snapshot, error)
}

// MetricsReader supplies host metrics to the renderer loop.
type MetricsReader interface {
	Read() systeminfo.Stats
}

// Screen presents complete native dashboard frames.
type Screen interface {
	Present(image.Image) error
}

// RunConfig controls render frequency and snapshot staleness.
type RunConfig struct {
	RefreshInterval time.Duration
	StaleAfter      time.Duration
}

// statsInterval throttles CPU/RAM/GPU sampling independently of the render
// refresh rate: those are comparatively expensive /proc reads, and nothing
// about the Pi's own load needs to be resampled 15 times a second just
// because the mascot animates that fast.
const statsInterval = 500 * time.Millisecond

// Run renders frames on config.RefreshInterval. touches may be nil (no
// touch input available, e.g. the device failed to open) — a nil channel
// always blocks in a select with a default case, so touch feedback is
// simply absent rather than a special case to handle.
func Run(ctx context.Context, loader SnapshotLoader, metrics MetricsReader, screen Screen, renderer *Renderer, config RunConfig, touches <-chan touch.Point) error {
	if loader == nil {
		return errors.New("snapshot loader is nil")
	}
	if metrics == nil {
		return errors.New("metrics reader is nil")
	}
	if screen == nil {
		return errors.New("screen is nil")
	}
	if renderer == nil {
		return errors.New("renderer is nil")
	}
	if config.RefreshInterval <= 0 {
		config.RefreshInterval = time.Second
	}
	if config.StaleAfter <= 0 {
		config.StaleAfter = 15 * time.Second
	}
	now := time.Now()
	var active []touch.Point
	active = drainTouches(touches, active, now)
	stats := metrics.Read()
	lastStatsAt := now
	if err := renderFrame(loader, screen, renderer, config, now, active, stats); err != nil {
		return err
	}
	ticker := time.NewTicker(config.RefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			active = drainTouches(touches, active, now)
			if now.Sub(lastStatsAt) >= statsInterval {
				stats = metrics.Read()
				lastStatsAt = now
			}
			if err := renderFrame(loader, screen, renderer, config, now, active, stats); err != nil {
				return err
			}
		}
	}
}

// drainTouches collects any newly arrived touch points without blocking,
// then drops points too old for the ripple effect to still be visible.
func drainTouches(touches <-chan touch.Point, active []touch.Point, now time.Time) []touch.Point {
	for {
		select {
		case point, ok := <-touches:
			if !ok {
				touches = nil
				continue
			}
			active = append(active, point)
		default:
			kept := active[:0]
			for _, point := range active {
				if now.Sub(point.At) <= touchRippleLifetime {
					kept = append(kept, point)
				}
			}
			return kept
		}
	}
}

func renderFrame(loader SnapshotLoader, screen Screen, renderer *Renderer, config RunConfig, now time.Time, touches []touch.Point, stats systeminfo.Stats) error {
	snapshots, loadErr := loader.LoadAll()
	claude, codex := LatestProviders(snapshots)
	todayInput, todayOutput := model.TodayTokenTotals(snapshots, now)
	frame := renderer.Render(View{
		Claude:            claude,
		Codex:             codex,
		Stats:             stats,
		Now:               now,
		StaleAfter:        config.StaleAfter,
		SessionCount:      len(snapshots),
		TodayInputTokens:  todayInput,
		TodayOutputTokens: todayOutput,
		LoadError:         loadErr,
		Touches:           touches,
	})
	if err := screen.Present(frame); err != nil {
		return fmt.Errorf("present pixel dashboard: %w", err)
	}
	return nil
}

// LatestProviders keeps Claude and Codex independent so a newer Codex event
// cannot displace the Claude-first dashboard. Within each provider it
// returns the newest snapshot with any usage numbers it lacks backfilled
// from the newest snapshot that has them, so a hook-only VS Code session
// still drives the mascot without blanking the panel (model.LatestWithUsage).
func LatestProviders(snapshots []model.Snapshot) (*model.Snapshot, *model.Snapshot) {
	var claudeGroup, codexGroup []model.Snapshot
	for _, snapshot := range snapshots {
		switch model.CanonicalProvider(snapshot.Provider) {
		case model.ProviderClaude:
			claudeGroup = append(claudeGroup, snapshot)
		case model.ProviderCodex:
			codexGroup = append(codexGroup, snapshot)
		}
	}
	return latestWithUsage(claudeGroup), latestWithUsage(codexGroup)
}

func latestWithUsage(group []model.Snapshot) *model.Snapshot {
	snapshot, ok := model.LatestWithUsage(group)
	if !ok {
		return nil
	}
	return &snapshot
}
