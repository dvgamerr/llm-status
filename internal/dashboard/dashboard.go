// Package dashboard provides the terminal fallback user interface.
package dashboard

import (
	"context"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	statusmodel "github.com/dvgamerr/llm-status/internal/model"
	"github.com/dvgamerr/llm-status/internal/systeminfo"
)

// Config controls terminal refresh, staleness, selection, and screen mode.
type Config struct {
	RefreshInterval time.Duration
	StaleAfter      time.Duration
	InitialSession  string
	Inline          bool
}

type snapshotLoader interface {
	LoadAll() ([]statusmodel.Snapshot, error)
}

type metricsReader interface {
	Read() systeminfo.Stats
}

// Model is the Bubble Tea state for the terminal dashboard.
type Model struct {
	loader          snapshotLoader
	metrics         metricsReader
	config          Config
	width           int
	height          int
	snapshots       []statusmodel.Snapshot
	stats           systeminfo.Stats
	selectedID      string
	selectionPinned bool
	sessionCursor   int
	showSessions    bool
	lastError       error
	now             func() time.Time
}

type tickMsg time.Time

type dataMsg struct {
	snapshots []statusmodel.Snapshot
	stats     systeminfo.Stats
	err       error
}

// These hexes are the same warm Claude-brand palette internal/pixelui uses
// (claudeOrange, claudePeach, green, yellow, red, textSecondary, trackColor
// in render.go) so this terminal fallback and the physical Pi dashboard
// read as one consistent theme instead of two unrelated color schemes.
var (
	colorOrange = lipgloss.Color("#D97757")
	colorBlue   = lipgloss.Color("#EBA687")
	colorGreen  = lipgloss.Color("#34D399")
	colorYellow = lipgloss.Color("#FACC15")
	colorRed    = lipgloss.Color("#F87171")
	colorMuted  = lipgloss.Color("#BCB2A4")
	colorBorder = lipgloss.Color("#3C3731")

	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(colorOrange)
	labelStyle = lipgloss.NewStyle().Foreground(colorMuted)
	helpStyle  = lipgloss.NewStyle().Foreground(colorMuted)
	errorStyle = lipgloss.NewStyle().Foreground(colorRed)
)

const clearScreenSequence = "\x1b[2J\x1b[H"

// Run starts the terminal dashboard until cancellation or user exit.
func Run(ctx context.Context, input io.Reader, output io.Writer, loader snapshotLoader, metrics metricsReader, config Config) error {
	// Linux consoles do not provide a reliable alternate screen buffer. Clear
	// tty1 synchronously before Bubble Tea can paint its initial View so boot
	// messages can never remain underneath a short or waiting-state frame.
	if err := clearTerminal(output); err != nil {
		return err
	}
	model := NewModel(loader, metrics, config)
	options := []tea.ProgramOption{tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output)}
	if !config.Inline {
		options = append(options, tea.WithAltScreen())
	}
	program := tea.NewProgram(model, options...)
	_, err := program.Run()
	return err
}

func clearTerminal(output io.Writer) error {
	if _, err := io.WriteString(output, clearScreenSequence); err != nil {
		return fmt.Errorf("clear terminal before render: %w", err)
	}
	return nil
}

// NewModel applies defaults and constructs terminal dashboard state.
func NewModel(loader snapshotLoader, metrics metricsReader, config Config) Model {
	if config.RefreshInterval <= 0 {
		config.RefreshInterval = time.Second
	}
	if config.StaleAfter <= 0 {
		config.StaleAfter = 15 * time.Second
	}
	return Model{
		loader:          loader,
		metrics:         metrics,
		config:          config,
		selectedID:      config.InitialSession,
		selectionPinned: config.InitialSession != "",
		now:             time.Now,
	}
}

// Init requests the initial data load and refresh timer.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.loadData(), m.nextTick())
}

// Update applies one Bubble Tea message.
func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tickMsg:
		return m, tea.Batch(m.loadData(), m.nextTick())
	case dataMsg:
		m.snapshots = msg.snapshots
		m.stats = msg.stats
		m.lastError = msg.err
		m.reconcileSelection()
	case tea.KeyMsg:
		return m.updateKey(msg.String())
	}
	return m, nil
}

func (m Model) updateKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "s":
		m.showSessions = !m.showSessions
		m.reconcileSelection()
		return m, tea.ClearScreen
	case "esc":
		if m.showSessions {
			m.showSessions = false
			return m, tea.ClearScreen
		}
	case "r":
		return m, tea.Sequence(tea.ClearScreen, m.loadData())
	case "a":
		m.selectionPinned = false
		m.reconcileSelection()
		return m, tea.ClearScreen
	case "up", "k":
		m.moveSessionCursor(-1)
	case "down", "j":
		m.moveSessionCursor(1)
	case "enter":
		if m.showSessions && len(m.snapshots) > 0 {
			m.selectedID = m.snapshots[m.sessionCursor].Session.ID
			m.selectionPinned = true
			m.showSessions = false
			return m, tea.ClearScreen
		}
	}
	return m, nil
}

func (m *Model) moveSessionCursor(delta int) {
	if !m.showSessions || len(m.snapshots) == 0 {
		return
	}
	m.sessionCursor = (m.sessionCursor + delta + len(m.snapshots)) % len(m.snapshots)
}

// View renders the current terminal frame.
func (m Model) View() string {
	if m.showSessions {
		return m.sessionsView()
	}
	return m.dashboardView()
}

func (m Model) loadData() tea.Cmd {
	return func() tea.Msg {
		snapshots, err := m.loader.LoadAll()
		return dataMsg{snapshots: snapshots, stats: m.metrics.Read(), err: err}
	}
}

func (m Model) nextTick() tea.Cmd {
	return tea.Tick(m.config.RefreshInterval, func(at time.Time) tea.Msg { return tickMsg(at) })
}

func (m *Model) reconcileSelection() {
	if len(m.snapshots) == 0 {
		m.sessionCursor = 0
		return
	}
	if !m.selectionPinned {
		m.selectedID = m.snapshots[0].Session.ID
		m.sessionCursor = 0
		return
	}
	for i, snapshot := range m.snapshots {
		if snapshot.Session.ID == m.selectedID {
			m.sessionCursor = i
			return
		}
	}
	m.selectedID = m.snapshots[0].Session.ID
	m.sessionCursor = 0
	m.selectionPinned = false
}

func (m Model) selectedSnapshot() (statusmodel.Snapshot, bool) {
	for _, snapshot := range m.snapshots {
		if snapshot.Session.ID == m.selectedID {
			return snapshot, true
		}
	}
	return statusmodel.Snapshot{}, false
}

func (m Model) dashboardView() string {
	width, height := m.frameSize()
	now := m.now()

	snapshot, found := m.selectedSnapshot()
	if !found {
		return m.waitingFrame(now, width, height)
	}

	age := now.Sub(snapshot.CapturedAt)
	if age < 0 {
		age = 0
	}
	statusText := "● LIVE"
	statusColor := colorGreen
	if age > m.config.StaleAfter {
		statusText = "● STALE"
		statusColor = colorRed
	}
	status := lipgloss.NewStyle().Bold(true).Foreground(statusColor).Render(statusText)

	if width >= 58 && height >= 20 {
		return m.fullDashboardFrame(snapshot, now, age, status, width, height)
	}
	return m.compactDashboardFrame(snapshot, now, age, status, width, height)
}

// fullDashboardFrame fills the Raspberry Pi console's complete 66x20 grid.
// Keeping every state full-height also overwrites any boot log that might have
// been on tty1 before the service started.
func (m Model) fullDashboardFrame(snapshot statusmodel.Snapshot, now time.Time, age time.Duration, status string, width, height int) string {
	inner := width - 2
	leftWidth, rightWidth := splitWidths(width)
	modelName := strings.ToUpper(displayModelName(snapshot))
	headerLeft := titleStyle.Render(providerTitle(snapshot)+" STATUS") + "  " + lipgloss.NewStyle().Bold(true).Foreground(colorBlue).Render(modelName)
	headerRight := status + "  " + labelStyle.Render(now.Format("15:04"))

	contextSummary := labelStyle.Render("CONTEXT  ") +
		lipgloss.NewStyle().Bold(true).Foreground(contextColor(snapshot.Context.UsedPercentage)).Render(percentageText(snapshot.Context.UsedPercentage)+" USED")
	todayInput, todayOutput := statusmodel.TodayTokenTotals(m.snapshots, now)
	tokens := labelStyle.Render("INPUT TODAY ") + lipgloss.NewStyle().Bold(true).Foreground(colorBlue).Render("↑ "+humanTokens(todayInput)) +
		labelStyle.Render("     OUTPUT TODAY ") + lipgloss.NewStyle().Bold(true).Foreground(colorOrange).Render("↓ "+humanTokens(todayOutput))

	modelDetail := snapshot.Model.ID
	if modelDetail == "" {
		modelDetail = displayModelName(snapshot)
	}
	if mode := modeText(snapshot); mode != "" {
		modelDetail += "  ·  " + mode
	}
	updateText := ageText(age) + " ago"
	if version := clientVersion(snapshot); version != "" {
		updateText += "  ·  " + providerTitle(snapshot) + " " + version
	}
	if m.lastError != nil {
		updateText = m.lastError.Error()
	}

	lines := []string{
		topBorder(width),
		frameLine(betweenRendered(headerLeft, headerRight, inner), width),
		separator(width),
		frameLine(betweenRendered(contextSummary, labelStyle.Render(contextText(snapshot.Context)), inner), width),
		frameLine(renderContextBar(snapshot.Context.UsedPercentage, inner), width),
		frameLine(tokens, width),
		splitSeparator(width),
		splitFrameLine(labelStyle.Render("5-HOUR LIMIT"), labelStyle.Render("7-DAY LIMIT"), width),
		splitFrameLine(progressLine(snapshot.RateLimits.FiveHour.UsedPercentage, leftWidth), progressLine(snapshot.RateLimits.SevenDay.UsedPercentage, rightWidth), width),
		splitFrameLine(labelStyle.Render(resetCountdown(snapshot.RateLimits.FiveHour.ResetsAt, now)), labelStyle.Render(resetCountdown(snapshot.RateLimits.SevenDay.ResetsAt, now)), width),
		separator(width),
		frameLine(infoLine("SESSION", sessionLabel(snapshot)+"  ·  "+durationText(snapshot.Cost.TotalDurationMS), inner), width),
		frameLine(infoLine("ACTIVITY", activityText(snapshot), inner), width),
		frameLine(infoLine("MODEL", modelDetail, inner), width),
		separator(width),
		frameLine(infoLine("PI", m.systemText(), inner), width),
		frameLine(infoLine("UPDATED", styleErrorOrNormal(updateText, m.lastError), inner), width),
		separator(width),
		frameLine(helpStyle.Render("[s] Sessions   [a] Auto   [r] Refresh       [q] Quit"), width),
		bottomBorder(width),
	}
	return finalizeFrame(lines, width, height)
}

func (m Model) compactDashboardFrame(snapshot statusmodel.Snapshot, now time.Time, age time.Duration, status string, width, height int) string {
	inner := width - 2
	header := betweenRendered(titleStyle.Render(providerTitle(snapshot)+" STATUS"), status, inner)
	lines := []string{
		topBorder(width),
		frameLine(header, width),
		separator(width),
		frameLine(infoLine("MODEL", displayModelName(snapshot), inner), width),
		frameLine(smallMeterRow("CTX", snapshot.Context.UsedPercentage, inner), width),
		frameLine(infoLine("TOKENS", compactTokenText(snapshot.Context), inner), width),
		separator(width),
		frameLine(smallMeterRow("5H", snapshot.RateLimits.FiveHour.UsedPercentage, inner), width),
		frameLine(infoLine("RESET", resetCountdown(snapshot.RateLimits.FiveHour.ResetsAt, now), inner), width),
		frameLine(smallMeterRow("7D", snapshot.RateLimits.SevenDay.UsedPercentage, inner), width),
		frameLine(infoLine("RESET", resetCountdown(snapshot.RateLimits.SevenDay.ResetsAt, now), inner), width),
		separator(width),
		frameLine(infoLine("SESSION", sessionLabel(snapshot), inner), width),
		frameLine(infoLine("PI", m.systemText(), inner), width),
		frameLine(infoLine("UPDATED", ageText(age)+" ago", inner), width),
		separator(width),
		frameLine(helpStyle.Render("[s] Sessions  [a] Auto  [r] Refresh  [q] Quit"), width),
		bottomBorder(width),
	}
	return finalizeFrame(lines, width, height)
}

func (m Model) waitingFrame(now time.Time, width, height int) string {
	inner := width - 2
	header := betweenRendered(titleStyle.Render("AI STATUS"), labelStyle.Render(now.Format("15:04:05 MST")), inner)
	errorLine := ""
	if m.lastError != nil {
		errorLine = errorStyle.Render(m.lastError.Error())
	}
	lines := []string{
		topBorder(width),
		frameLine(header, width),
		separator(width),
		frameLine("", width),
		frameLine(centerRendered(titleStyle.Render("WAITING FOR DATA"), inner), width),
		frameLine(centerRendered(labelStyle.Render("No Claude or Codex snapshot is available yet"), inner), width),
		frameLine("", width),
		frameLine(labelStyle.Render("STATUSLINE COMMAND"), width),
		frameLine(titleStyle.Render("~/.local/bin/llm-status ingest"), width),
		frameLine("", width),
		separator(width),
		frameLine(labelStyle.Render("PI HEALTH"), width),
		frameLine(m.systemText(), width),
		frameLine("", width),
		frameLine(centerRendered(labelStyle.Render("Data appears after the next AI response"), inner), width),
		frameLine(errorLine, width),
		frameLine("", width),
		separator(width),
		frameLine(helpStyle.Render("[r] Refresh                              [q] Quit"), width),
		bottomBorder(width),
	}
	return finalizeFrame(lines, width, height)
}

func (m Model) sessionsView() string {
	width, height := m.frameSize()
	inner := width - 2
	visible := max(1, height-6)
	start, end := visibleRange(m.sessionCursor, len(m.snapshots), visible)
	summary := fmt.Sprintf("%d SESSIONS", len(m.snapshots))
	if len(m.snapshots) > 0 {
		summary = fmt.Sprintf("%d–%d OF %d", start+1, end, len(m.snapshots))
	}
	lines := []string{
		topBorder(width),
		frameLine(betweenRendered(titleStyle.Render("SESSIONS"), labelStyle.Render(summary), inner), width),
		separator(width),
	}
	for slot := 0; slot < visible; slot++ {
		i := start + slot
		if i < end {
			snapshot := m.snapshots[i]
			cursor := "  "
			style := lipgloss.NewStyle()
			if i == m.sessionCursor {
				cursor = "› "
				style = style.Bold(true).Foreground(colorBlue)
			}
			left := style.Render(cursor + clip(sessionLabel(snapshot), max(8, inner-24)))
			right := labelStyle.Render(clip(displayModelName(snapshot), 10) + "  " + ageText(max(0, m.now().Sub(snapshot.CapturedAt))) + " ago")
			lines = append(lines, frameLine(betweenRendered(left, right, inner), width))
		} else {
			lines = append(lines, frameLine("", width))
		}
	}
	lines = append(lines,
		separator(width),
		frameLine(helpStyle.Render("[↑/↓] Move  [enter] Pin  [a] Auto  [s/esc] Back  [q] Quit"), width),
		bottomBorder(width),
	)
	return finalizeFrame(lines, width, height)
}

func (m Model) systemText() string {
	parts := []string{
		"CPU " + percentOrDash(m.stats.CPUPercent),
		"RAM " + memoryText(m.stats.MemoryUsedBytes, m.stats.MemoryTotalBytes),
		"Temp " + temperatureText(m.stats.TemperatureC),
	}
	if m.stats.Load1 != nil {
		parts = append(parts, fmt.Sprintf("Load %.2f", *m.stats.Load1))
	}
	if m.stats.Uptime != nil {
		parts = append(parts, "Up "+compactDuration(*m.stats.Uptime))
	}
	return strings.Join(parts, "  ")
}

func (m Model) frameSize() (int, int) {
	width := 66
	if m.width > 0 {
		width = min(66, max(28, m.width))
	}
	height := 20
	if m.height > 0 {
		height = min(20, max(6, m.height))
	}
	return width, height
}

func topBorder(width int) string {
	return colorBorderString("╭" + strings.Repeat("─", max(0, width-2)) + "╮")
}

func bottomBorder(width int) string {
	return colorBorderString("╰" + strings.Repeat("─", max(0, width-2)) + "╯")
}

func separator(width int) string {
	return colorBorderString("├" + strings.Repeat("─", max(0, width-2)) + "┤")
}

func splitSeparator(width int) string {
	left, right := splitWidths(width)
	return colorBorderString("├" + strings.Repeat("─", left) + "┼" + strings.Repeat("─", right) + "┤")
}

func frameLine(content string, width int) string {
	inner := max(0, width-2)
	content = clipStyled(content, inner)
	return colorBorderString("│") + padStyled(content, inner) + colorBorderString("│")
}

func splitFrameLine(left, right string, width int) string {
	leftWidth, rightWidth := splitWidths(width)
	left = padStyled(clipStyled(left, leftWidth), leftWidth)
	right = padStyled(clipStyled(right, rightWidth), rightWidth)
	return colorBorderString("│") + left + colorBorderString("│") + right + colorBorderString("│")
}

func splitWidths(width int) (int, int) {
	inner := max(1, width-2)
	left := max(1, (inner-1)/2)
	return left, max(1, inner-left-1)
}

func finalizeFrame(lines []string, width, height int) string {
	if height <= 0 {
		height = len(lines)
	}
	if len(lines) > height {
		lines = append(lines[:height-1], bottomBorder(width))
	}
	insertAt := max(1, len(lines)-3)
	for len(lines) < height {
		lines = append(lines, "")
		copy(lines[insertAt+1:], lines[insertAt:])
		lines[insertAt] = frameLine("", width)
		insertAt++
	}
	return strings.Join(lines, "\n")
}

func centerRendered(value string, width int) string {
	space := max(0, width-lipgloss.Width(value))
	return strings.Repeat(" ", space/2) + value
}

func renderContextBar(percentage *float64, width int) string {
	if percentage == nil {
		return labelStyle.Render(strings.Repeat("░", width))
	}
	value := min(100, max(0, *percentage))
	filled := int(math.Round(value * float64(width) / 100))
	return lipgloss.NewStyle().Foreground(contextColor(percentage)).Render(strings.Repeat("█", filled)) +
		labelStyle.Render(strings.Repeat("░", width-filled))

}

func contextColor(percentage *float64) lipgloss.Color {
	if percentage == nil {
		return colorMuted
	}
	if *percentage >= 90 {
		return colorRed
	}
	if *percentage >= 75 {
		return colorYellow
	}
	return colorBlue
}

func styleErrorOrNormal(value string, err error) string {
	if err != nil {
		return errorStyle.Render(value)
	}
	return value
}

func progressLine(percentage *float64, width int) string {
	percent := percentageText(percentage)
	barWidth := max(4, width-lipgloss.Width(percent)-1)
	return renderBar(percentage, barWidth) + " " + lipgloss.NewStyle().Bold(true).Foreground(thresholdColorValue(percentage)).Render(percent)
}

func smallMeterRow(label string, percentage *float64, width int) string {
	percent := percentageText(percentage)
	barWidth := max(4, width-len(label)-lipgloss.Width(percent)-2)
	return labelStyle.Render(label) + " " + renderBar(percentage, barWidth) + " " + lipgloss.NewStyle().Bold(true).Foreground(thresholdColorValue(percentage)).Render(percent)
}

func renderBar(percentage *float64, width int) string {
	if percentage == nil {
		return labelStyle.Render(strings.Repeat("░", width))
	}
	value := min(100, max(0, *percentage))
	filled := int(math.Round(value * float64(width) / 100))
	return lipgloss.NewStyle().Foreground(thresholdColor(value)).Render(strings.Repeat("█", filled)) +
		labelStyle.Render(strings.Repeat("░", width-filled))
}

func thresholdColorValue(value *float64) lipgloss.Color {
	if value == nil {
		return colorMuted
	}
	return thresholdColor(*value)
}

func percentageText(value *float64) string {
	if value == nil {
		return "--%"
	}
	return fmt.Sprintf("%.0f%%", min(100, max(0, *value)))
}

func resetCountdown(epoch *int64, now time.Time) string {
	if epoch == nil || *epoch <= 0 {
		return "reset --"
	}
	remaining := time.Unix(*epoch, 0).Sub(now)
	if remaining <= 0 {
		return "reset due"
	}
	return "resets " + compactDuration(remaining)
}

func displayModelName(snapshot statusmodel.Snapshot) string {
	if snapshot.Model.DisplayName != "" {
		return snapshot.Model.DisplayName
	}
	if snapshot.Model.ID != "" {
		return snapshot.Model.ID
	}
	if providerTitle(snapshot) == "CODEX" {
		return "Codex"
	}
	return "Claude"
}

func providerTitle(snapshot statusmodel.Snapshot) string {
	switch statusmodel.CanonicalProvider(snapshot.Provider) {
	case statusmodel.ProviderCodex:
		return "CODEX"
	default:
		return "CLAUDE"
	}
}

func clientVersion(snapshot statusmodel.Snapshot) string {
	if snapshot.ClientVersion != "" {
		return snapshot.ClientVersion
	}
	return snapshot.ClaudeCodeVersion
}

func activityText(snapshot statusmodel.Snapshot) string {
	return strings.Join([]string{
		"Cost " + costText(snapshot.Cost.TotalCostUSD),
		"Code " + codeText(snapshot.Cost.TotalLinesAdded, snapshot.Cost.TotalLinesRemoved),
	}, "  ·  ")
}

func compactTokenText(context statusmodel.Context) string {
	return "↑ " + tokenText(context.InputTokens()) + " input  ↓ " + tokenText(context.OutputTokens()) + " output"
}

func tokenText(value *int64) string {
	if value == nil {
		return "--"
	}
	return humanTokens(*value)
}

func infoLine(label, value string, width int) string {
	prefix := fmt.Sprintf("%-9s", label)
	line := labelStyle.Render(prefix) + value
	return clipStyled(line, width)
}

func colorBorderString(value string) string {
	return lipgloss.NewStyle().Foreground(colorBorder).Render(value)
}

func betweenRendered(left, right string, width int) string {
	space := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	return left + strings.Repeat(" ", space) + right
}

func padStyled(value string, width int) string {
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
}

func clipStyled(value string, width int) string {
	if lipgloss.Width(value) <= width {
		return value
	}
	return ansi.Truncate(value, width, "…")
}

func visibleRange(cursor, total, visible int) (int, int) {
	if total <= visible {
		return 0, total
	}
	start := cursor - visible/2
	start = min(max(0, start), total-visible)
	return start, start + visible
}

func thresholdColor(value float64) lipgloss.Color {
	if value >= 90 {
		return colorRed
	}
	if value >= 70 {
		return colorYellow
	}
	return colorGreen
}

func contextText(context statusmodel.Context) string {
	if context.WindowSize == nil {
		return "tokens --"
	}
	var used int64
	if input := context.InputTokens(); input != nil {
		used += *input
	}
	if output := context.OutputTokens(); output != nil {
		used += *output
	}
	if used == 0 && context.UsedPercentage != nil {
		used = int64(math.Round(float64(*context.WindowSize) * *context.UsedPercentage / 100))
	}
	return humanTokens(used) + " / " + humanTokens(*context.WindowSize)
}

func humanTokens(value int64) string {
	if value >= 1_000_000 {
		return trimDecimal(fmt.Sprintf("%.1f", float64(value)/1_000_000)) + "M"
	}
	if value >= 1_000 {
		return trimDecimal(fmt.Sprintf("%.1f", float64(value)/1_000)) + "k"
	}
	return fmt.Sprintf("%d", value)
}

func trimDecimal(value string) string {
	return strings.TrimSuffix(value, ".0")
}

func durationText(milliseconds *int64) string {
	if milliseconds == nil {
		return "--"
	}
	return formatClock(time.Duration(*milliseconds) * time.Millisecond)
}

func formatClock(value time.Duration) string {
	if value < 0 {
		value = 0
	}
	hours := int(value / time.Hour)
	value -= time.Duration(hours) * time.Hour
	minutes := int(value / time.Minute)
	value -= time.Duration(minutes) * time.Minute
	seconds := int(value / time.Second)
	return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
}

func costText(value *float64) string {
	if value == nil {
		return "--"
	}
	return fmt.Sprintf("$%.2f", *value)
}

func codeText(added, removed *int64) string {
	if added == nil && removed == nil {
		return "--"
	}
	var add, remove int64
	if added != nil {
		add = *added
	}
	if removed != nil {
		remove = *removed
	}
	return fmt.Sprintf("+%d  -%d", add, remove)
}

func percentOrDash(value *float64) string {
	if value == nil {
		return "--"
	}
	return fmt.Sprintf("%.0f%%", *value)
}

func memoryText(used, total *uint64) string {
	if used == nil || total == nil {
		return "--"
	}
	return fmt.Sprintf("%.1f/%.1f GB", float64(*used)/(1<<30), float64(*total)/(1<<30))
}

func temperatureText(value *float64) string {
	if value == nil {
		return "--"
	}
	return fmt.Sprintf("%.0f°C", *value)
}

func modeText(snapshot statusmodel.Snapshot) string {
	parts := make([]string, 0, 2)
	if snapshot.Effort != "" {
		parts = append(parts, "effort "+snapshot.Effort)
	}
	if snapshot.ThinkingEnabled != nil {
		if *snapshot.ThinkingEnabled {
			parts = append(parts, "thinking on")
		} else {
			parts = append(parts, "thinking off")
		}
	}
	return strings.Join(parts, " · ")
}

func sessionLabel(snapshot statusmodel.Snapshot) string {
	if snapshot.Session.Name != "" {
		return snapshot.Session.Name + " (" + shortID(snapshot.Session.ID) + ")"
	}
	return shortID(snapshot.Session.ID)
}

func shortID(id string) string {
	if utf8.RuneCountInString(id) <= 12 {
		return id
	}
	return clip(id, 12)
}

func ageText(age time.Duration) string {
	if age < time.Second {
		return "just now"
	}
	return compactDuration(age)
}

func compactDuration(value time.Duration) string {
	value = value.Round(time.Second)
	if value < time.Minute {
		return fmt.Sprintf("%ds", int(value/time.Second))
	}
	if value < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(value/time.Minute), int(value%time.Minute/time.Second))
	}
	if value < 24*time.Hour {
		return fmt.Sprintf("%dh%02dm", int(value/time.Hour), int(value%time.Hour/time.Minute))
	}
	return fmt.Sprintf("%dd%02dh", int(value/(24*time.Hour)), int(value%(24*time.Hour)/time.Hour))
}

func clip(value string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}
