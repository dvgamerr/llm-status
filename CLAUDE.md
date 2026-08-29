# Raspberry Pi display target

- Target host: `pilab`
- Board: Raspberry Pi 4 Model B Rev 1.4
- Display: original Raspberry Pi Touch Display 7-inch, 800x480
- Native display path: `/dev/fb0`, `vc4drmfb`, 800x480, RGB565 (16 bpp),
  stride 1600 bytes
- Linux console font remains TerminusBold 12x24 only for text-mode fallback
- Text-mode fallback grid: 66 columns x 20 rows
- Installed/supported TerminusBold sizes: 10x20, 12x24, 14x28, and 16x32
- Previous console configuration backup:
  `/etc/default/console-setup.codex-backup-20260731`
- Touch controller: `10-0038 generic ft5x06` at `/dev/input/event0`
  (`ID_INPUT_TOUCHSCREEN=1`, `INPUT_PROP_DIRECT`). Confirmed by raw capture
  that `ABS_MT_POSITION_X/Y` and the legacy `ABS_X/Y` mirror already report
  in panel pixels (0-799, 0-479) — no scaling needed. `input_event` on this
  64-bit kernel is 24 bytes (8+8+2+2+4), also confirmed by capture rather
  than assumed from headers.

The primary UI is the native `gfx` dashboard, rendered at exactly 800x480
pixels into `/dev/fb0`; the old 66x20 TUI is fallback only. After every edit
to `internal/pixelui` (layout, colors, animation), run
`go run ./cmd/claude-status preview` and look at the resulting
`pixel-dashboard-preview.png` before calling the change done — don't just
read the diff and assume the pixels landed where the numbers say they
should. Final visual QA before shipping to `pilab` should still be against
real RGB565 output, not only this RGB PNG preview, because subtle gradients
band on the physical 16-bit framebuffer in ways the preview won't show.
Keep the warm Claude-first theme with a
flat dark background, high-contrast type, equal Claude 5-hour/7-day limit
rows (each with its reset countdown), and a Codex card that deliberately
never shows a session id/name — only its model, reasoning effort, and
total tokens (input+output) with an estimated USD cost from
`codexPricingTable` (`internal/pixelui/codex_pricing.go`), since Codex is
"the other tool" here, not a session to track, and per-model pricing
varies enough that a raw context-window percentage was less useful than
knowing what it's actually costing.
Provider selection is independent: a newer Codex event must never displace
Claude from the primary UI. The display follows the newest snapshot for each
provider without local input, except for the touch ripple below — touch
never changes what data is shown, only a purely visual acknowledgment that
the screen was tapped.

The panel is a touchscreen, so `internal/touch` (Linux-only; a `!linux` stub
elsewhere) reads `/dev/input/event0` directly and `internal/pixelui/render.go`
draws a fading ring at each tap (`renderTouchRipples`/`blendRing`,
`touchRippleLifetime`). This is feedback only, not a control surface — the
dashboard has no buttons to press. `claude-status gfx --touch-device` can
point at a different evdev node or be set to `""` to disable it; a failure
to open the device is logged as a warning and the dashboard keeps running
without touch feedback, the same "secondary feature must never break the
primary display" pattern as `pixelui.resolveActivity`'s fallbacks. The
`claude-status-tty1.service` unit's `SupplementaryGroups=` must include
`input` (added alongside `video`) or the device open fails with a
permission error.

The header uses a bare white Anthropic logo with no background or frame, and
deliberately omits Claude model and session identity. The left-hand status rail
holds context-specific Clawd SVG artwork and is the dashboard's focal point
(`internal/pixelui/render.go`'s `renderRail`). `waiting_approval` is the only
state left on a single static pose plus procedural motion
(`mascotPoseForActivity`): Clawd Exclamation Mark, tight urgent shake, with a
hand-edited "-2" alternate frame that `render.go` alternates on the state's
own beat. Everything else (`idle`, the `working` legacy alias, `typing`,
`thinking`, `building`, `subagent_one`, `subagent_many`) instead plays back a
real frame-by-frame sequence hand-traced from a reference GIF for that pose
(standing and blinking, thought bubble rising, hands typing, hammer
swinging, head bobbing to music, balls arcing) — `gifFramesForActivity`
picks the state's `[]*image.RGBA` sequence and `gifFrameIndex` advances
through it at `gifFrameDuration` (300ms) per frame, looping; see
`internal/pixelui/assets/README.md` for the per-state frame counts and
naming (`clawd-<state>-NN.svg`, zero-padded so lexical sort is also playback
order). The rail card stays completely still in every state; only the
mascot artwork moves — there is deliberately no halo/background circle
behind it and no colored status pill under it, just the mascot art itself
plus one line of caption text. The embedded SVGs are rasterized once at
renderer startup, not on every frame. Activity state is independent of the
statusLine refresh cycle — see below. The rail caption below the mascot is
just the activity duration ("typing for 12s", "idle for 4m") — no session
name or ID, matching
the header's own omission of session identity.

## Provider ingestion and Windows source

- The dashboard accepts both Claude Code and Codex snapshots. A snapshot has a
  `provider` field, and the UI must label it `CLAUDE STATUS` or `CODEX STATUS`.
- Windows installs the source binary at
  `%LOCALAPPDATA%\Programs\claude-status\claude-status.exe`.
- Claude Code calls `ingest` through `%USERPROFILE%\.claude\settings.json`.
- Codex calls `codex-notify` through the external `notify` array in
  `%USERPROFILE%\.codex\config.toml`. Preserve and forward any notifier that
  was configured before claude-status is installed.
- Source adapters only persist sanitized local state; they never open the
  network. The long-lived `claude-status relay` process is the sole owner of
  SSH transport. On Windows, `scripts/install-windows.ps1` runs one relay via
  the `claude-status-relay` Windows Service. It retries changed snapshots and
  pipes only the allowlisted `model.Snapshot` to
  `/home/pi/.local/bin/claude-status import`.
- Never mirror raw Claude statusLine JSON, a raw Codex notify payload, rollout
  lines, prompts, responses, transcripts, credentials, OAuth tokens, or
  session auth. The Pi's `import` command must reject unknown JSON fields.
- Codex context usage uses the latest `last_token_usage.total_tokens` divided
  by `model_context_window`. The 5-hour and 7-day bars use the 300-minute and
  10,080-minute rate-limit windows when the account exposes them; unavailable
  enterprise limits show `UNMETERED` only when Codex explicitly reports
  `credits.unlimited=true`; otherwise unavailable values remain `--`.

## Activity state (mascot animation, approval badge)

- The statusLine refresh alone cannot tell whether Claude is working, idle, or
  blocked on a permission prompt, so `%USERPROFILE%\.claude\settings.json`
  also registers six hooks — `UserPromptSubmit`, `PreToolUse` (matcher `*`),
  `Stop`, `Notification`, `SubagentStart`, and `SubagentStop` — that all call
  `claude-status activity` (`scripts/install-windows.ps1`'s
  `Set-ClaudeStatusHook`). That merge is additive: it only ever replaces the
  `claude-status ... activity` hook group it previously installed on each
  event and leaves every other tool's hook group on that event untouched.
- `claude-status activity` reads one hook payload from stdin and merges the
  result of two independent classifications into that session's
  already-stored snapshot — it never rebuilds the snapshot from scratch, so a
  hook firing before or after the next statusLine event can't clobber the
  other's fields:
  - `claude.ActivityForHook` maps `UserPromptSubmit` → `thinking`, `Stop` →
    `idle`, and `PreToolUse` → `typing` or `building` depending on
    `tool_name` (`Bash` is `building`; everything else, including an empty
    `tool_name`, is `typing`). For `Notification` it inspects the hook's
    `message` text locally to detect a permission prompt ("needs your
    permission to use ...") vs. an idle-nudge; either way the message text
    itself is discarded, never persisted or mirrored, matching the "never
    mirror raw payloads" rule above.
  - `claude.SubagentDeltaForHook` maps `SubagentStart`/`SubagentStop` to a
    `+1`/`-1` adjustment of `Activity.Subagents`, a running count of
    concurrent Task-tool subagents for that session, floored at 0. This is
    independent of `Activity.State`: a `SubagentStart`/`Stop` event never
    touches `State`, and a `PreToolUse`/`Stop`/etc. event never touches
    `Subagents`.
  - `pixelui.resolveActivity` then decides what to actually render: a fresh
    `waiting_approval` always wins; otherwise a positive `Subagents` count
    overrides `State` with `subagent_one` (1) or `subagent_many` (2+); only
    when `Subagents` is 0 does the stored `State` (`thinking`/`typing`/
    `building`/`idle`) show through. `ActivityWorking` is kept only as a
    legacy value some older/pre-tool_name snapshots may still carry, and
    renders identically to `typing`.
- This command must always exit 0. `PreToolUse` hooks can block the tool call
  if their hook exits non-zero, and this activity side channel must never be
  able to do that.
- `pixelui.resolveActivity` degrades gracefully when hooks aren't installed
  (falls back to a statusLine-freshness proxy) and when a state gets stuck
  (falls back to idle after `activityStaleAfter`, 10 minutes, in case a `Stop`
  hook was missed).

## statusLine does not work from the VS Code extension (confirmed, not a bug)

- Windows gotcha noticed while debugging this: `~/.claude.json`'s per-project
  `hasTrustDialogAccepted` is keyed by the project path as a literal string,
  case-sensitive drive letter included. `E:/.dvgamerr/aide-lab` and
  `e:/.dvgamerr/aide-lab` are two separate entries; trusting one does not
  trust the other, and Claude Code silently skips `statusLine`/hooks for an
  untrusted entry with no visible error. If hooks ever stop firing after
  reopening a project, check both letter-case variants before assuming a
  config regression.
- `statusLine` is CLI-terminal-only. Confirmed via Claude Code's own docs
  (the VS Code extension feature-comparison table omits `statusLine`
  entirely) and GitHub issue #55643 ("Support custom statusLine in VS Code
  extension"), closed as **not planned**. It renders as a bar at the bottom
  of a real terminal; the VS Code extension's chat panel has no such
  surface, so `ingest` (the command `statusLine` invokes) never runs for
  that session — not a settings.json mistake, not a trust problem. Hooks
  are unaffected by this: they fire from any interface.
- Rate limits only ever appear in the statusLine JSON for Pro/Max accounts,
  and only after the session's first API response — a second, independent
  reason the numbers can be briefly absent even when statusLine does fire.
- Hooks never carry usage/rate-limit numbers, by design, in any interface —
  `claude.HookInput` only has `session_id`, `hook_event_name`, and
  `message` (Notification only) because that's all Claude Code sends. Don't
  go looking for a hook-based way to get real percentages; there isn't one.
- Non-obvious discovery: statusLine *does* fire from the short-lived CLI
  processes Claude Code spawns internally for subagents (the Task/Agent
  tool) and some slash commands (`/usage`) — those are real CLI
  invocations with a terminal-like context, unlike the main chat session.
  Their `ingest` writes a real snapshot under
  `%LOCALAPPDATA%\claude-status\sessions\*.json` with genuine
  `rate_limits` (account-wide, so any fresh one is valid regardless of
  which session produced it). The independent relay notices that atomic
  local write and delivers it to `pilab`; the short-lived source process
  never waits for or launches SSH.
- Two sources can provide real numbers for a VS Code extension session:
  (a) `claude-status usage --five-hour PCT --seven-day PCT`
  (merges just the two percentages into the latest local session; the relay
  delivers the change —
  see "Activity state" above for the shape it preserves), fed from numbers
  read off `/usage`'s own output or the Account & Usage panel; or (b) a
  short-lived `/usage` or subagent CLI process writes a fresh real snapshot
  under `sessions\*.json`. In both cases the relay automatically selects and
  delivers the freshest Claude snapshot to `pilab`.
- Since a hook-only VS Code snapshot has no numbers of its own but is the
  newest one for the provider, both `relay.latestProviders` and
  `pixelui.LatestProviders` now route through `model.LatestWithUsage`: the
  newest snapshot still wins and keeps its own session identity, activity,
  and `captured_at`, and every usage field it lacks (model, context, rate
  limits, cost, effort, thinking) is backfilled from the newest snapshot of
  the same provider that actually has them. Rate limits are account-wide, so
  borrowing them across sessions reports the same quota either way. The donor
  is deliberately not bounded by age: a rate window carries its own
  `resets_at` and `limitLine` already draws a passed reset as expired,
  so a stale donor degrades visibly instead of quietly showing an old
  percentage as current.

References:

- https://www.raspberrypi.com/documentation/accessories/display.html
- https://manpages.debian.org/trixie/console-setup/console-setup.5.en.html

## SSH-only control

- `pilab` has no keyboard attached. Perform all input, control, recovery, and
  process management remotely through `ssh pilab`; never ask the user to press
  a key such as `q` on the Raspberry Pi.
- The physical pixels are `/dev/fb0`; `/dev/tty1` is switched to `KD_GRAPHICS`
  while the service runs. A normal SSH PTY and a PowerShell window
  on Windows are different terminals, so clearing or drawing in either one does
  not change the Raspberry Pi display.
- The dashboard on `/dev/tty1` is owned by the enabled system service
  `claude-status-tty1.service`. It runs
  `/home/pi/.local/bin/claude-status gfx --refresh 66ms --framebuffer /dev/fb0 --tty /dev/tty1`
  (~15fps; the flag floor is 20ms, so this is intentionally not maxed out)
  with `Restart=always`.
- Inspect it with
  `ssh pilab "systemctl --no-pager --full status claude-status-tty1.service"`.
  Start, stop, or restart it through SSH with `sudo systemctl`; do not try to
  control the service by sending keys to the physical console.
- `/home/pi/.local/bin` is not guaranteed to be in Fish's non-interactive
  `PATH`, so use the absolute path when invoking `claude-status` outside the
  service.

## Lessons learned from the failed control attempt

- Running `clear` in an ordinary SSH session cleared only that SSH terminal,
  not `/dev/tty1`.
- Running bare `claude-status` only prints command help; the primary dashboard
  command is `claude-status gfx`.
- Opening a visible PowerShell/SSH TUI on Windows did not update the Raspberry
  Pi's physical display and left an interactive session the user could not
  control from the Pi.
- Sending `TERM` or `KILL` directly to the dashboard process was the wrong
  control method: systemd immediately respawned it because the service uses
  `Restart=always`. Control `claude-status-tty1.service` instead.
- This file is the canonical project instruction file. Do not write these notes
  to `C:\Users\dvgamerr\Desktop\CLAUDE.md`.

## Maintenance log

### 2026-08-02 — CLI deduplication and framebuffer render allocation cleanup

- Replaced the repeated `flag.NewFlagSet`/output/usage/parse-error blocks in
  the app commands with `newCommandFlagSet` and `parseCommandFlags`. The shared
  path preserves the existing help text and exit-code contract while removing
  11 copies of the same control flow across `app.go`, `service_cmd.go`, and
  `pi_cmd.go`. Service-install help and invalid-flag cases now explicitly cover
  the shared behavior alongside the existing command-table tests.
- `pixelui.fillRounded` now creates one `image.Uniform` per shape and reuses it
  for every scanline. Previously it created the same uniform inside the row
  loop; this removes redundant allocations from the ~15 FPS framebuffer hot
  path without changing geometry, compositing mode, colors, or rendered pixels.
- Successful verification for this maintenance pass: shuffled tests repeated
  three times, package coverage collection, `go vet ./...`, native
  `go build ./...`, a Windows binary ingest/privacy smoke test, Linux ARM64
  cross-build plus metadata inspection, PowerShell and shell syntax checks,
  Linux AMD64/ARM64 release packaging plus checksum verification, and
  `git diff --check`.
- At the time of this pass, coverage was 71.0% and the local Windows host had
  no GCC for `go test -race`; both observations are historical. The later
  full-project debt pass raised coverage above the 80% gate, while race tests
  remain enforced by Linux CI.
- Pre-existing worktree changes in Claude/state/system-info tests and reader
  code, plus untracked mascot GIFs in top-level `assets/`, were deliberately
  left intact and are not part of this maintenance pass.

### 2026-08-03 — theme unification, daily token totals, mascot clip fix, Codex cost

- `internal/dashboard` (the terminal fallback) used an unrelated
  Dracula-style palette (`#FFB86B` amber, `#7DD3FC` blue, `#34415D`
  border, etc.) instead of the warm Claude-brand palette `internal/pixelui`
  already implements. Its `colorOrange`/`colorBlue`/`colorGreen`/
  `colorYellow`/`colorRed`/`colorMuted`/`colorBorder` constants now reuse the
  exact hexes of pixelui's `claudeOrange`/`claudePeach`/`green`/`yellow`/
  `red`/`textSecondary`/`trackColor`, so both UIs read as one theme.
  Separately, every mascot SVG under `internal/pixelui/assets` filled the
  Clawd body with `#D77757`, a one-digit drift from the actual brand hex
  `#D97757` used everywhere else — corrected across all twelve rigs.
- Added `model.TodayTokenTotals(snapshots, now)`: sums input+output tokens
  across every stored snapshot — both Claude and Codex — captured on `now`'s
  local calendar day. The pixel dashboard's INPUT/OUTPUT chips
  (`renderClaudePanel`) and the terminal fallback's INPUT/OUTPUT line
  (`fullDashboardFrame`) both switched from one session's own counts to this
  combined daily total; `runPreview` (`claude-status preview`) needed the
  same wiring since it builds its `pixelui.View` directly rather than going
  through `pixelui.Run`. Caveat documented on the function itself: a Codex
  snapshot only ever carries its latest turn's usage (see
  `internal/codex`), so a long-running Codex session under-counts relative
  to an equivalent Claude session until Codex ingestion tracks a running
  total of its own.
- Fixed three mascot rigs whose animated `<animateTransform>` keyframes
  pushed content outside the shared `viewBox="0 0 100 100"`, which
  `rasterizeSVG` rasterizes 1:1 with no margin — the overflowing portion was
  hard-clipped at the canvas edge, visible as a flat line slicing through
  the shape mid-loop: `clawd-thinking.svg`'s thought-bubble group (peak
  translate pushed the biggest circle 6 units above y=0), `clawd-juggling
  .svg`'s `ball2` (base position 10 units higher than `ball1`/`ball3`, so
  its throw peak went well above y=0), and `clawd-building.svg`'s
  `armHammer` (rotating to 20° pushed the hammer head's corner past
  x=100). All three were retuned to stay inside the viewBox while keeping
  the same motion character (thought bubble still rises, ball2 still
  throws to the same height as the other two balls, the hammer still swings
  a full arc, just capped at 10° instead of 20°).
- The Codex card no longer shows a context-window percentage — Codex's
  card is about "the other tool"'s usage/spend, and the context bar was
  redundant with the Claude panel's own. `codexCard` now calls
  `codexUsageBlock`, which reports total tokens (input+output) and an
  estimated USD cost from the new `internal/pixelui/codex_pricing.go`
  (`codexPricingTable`, matched by exact model ID then longest prefix, with
  a flagship-tier fallback rate for unrecognized models) — different
  Codex-served model families are priced differently, so the estimate is
  keyed by `snapshot.Model.ID`, not a single flat rate. `contextBlock` was
  deleted as dead code once this was its only remaining caller.

### 2026-08-29 — dependency refresh, Windows console suppression, VS Code usage backfill

- Updated every module dependency to its latest release (`go get -u ./...`
  plus `go get -u tool`) and let staticcheck v0.8.1's own requirement pull the
  `go` directive from 1.25.8 to 1.26.0. `pilab` runs go1.26.5; the Windows
  host was upgraded to go1.27.0 partway through this pass. The directive
  stays at 1.26.0 rather than tracking the newest toolchain so the Pi keeps
  building natively instead of downloading a go1.27 toolchain of its own on
  every rebuild.
  Notable bumps: `golang.org/x/image` 0.44→0.45, `x/net` 0.57→0.58,
  `x/text` 0.40→0.41, `charmbracelet/x/ansi` 0.10.1→0.11.8,
  `BurntSushi/toml` and `xo/terminfo` off pseudo-versions onto real tags,
  `x/vuln` 1.6→1.7, `honnef.co/go/tools` 0.7→0.8.1.
- New `internal/winconsole` keeps Windows console windows off the screen
  without giving up the console subsystem (which is what makes the binary
  usable from a terminal and lets `install-windows.ps1` wait on
  `service install` and read its exit code). `main()` calls
  `HideServiceConsole` once SCM has confirmed a service process — a service
  has no interactive user — and `HideDetachedConsole` otherwise, which hides
  the console only when `GetConsoleProcessList` reports this process as its
  sole owner. That is exactly the "Windows allocated a fresh window for this
  launch" case (a hook fired by the VS Code extension host, an Explorer
  double-click); a console inherited from PowerShell has at least two
  attached processes and is left visible. `SuppressChildConsole` adds
  `CREATE_NO_WINDOW` to the relay's ssh children and the forwarded Codex
  notifier. `service install` also now states
  `ServiceType: SERVICE_WIN32_OWN_PROCESS` explicitly rather than relying on
  `mgr.CreateService`'s default, to document that this is never
  `SERVICE_INTERACTIVE_PROCESS`.
- New `internal/model/merge.go` (`HasUsage`, `WithUsageFrom`,
  `LatestWithUsage`, plus `HasValues` on Context/RateLimits/Cost) fixes the
  VS Code session showing `--` for every number — see the statusLine section
  above for the selection rule and why the donor is not age-bounded.
  `relay.latestProviders` and `pixelui.LatestProviders` both call it, so the
  Pi's imported snapshot and any locally rendered dashboard agree.
- Verification for this pass: `gofmt -l`, `go mod verify`, `go mod tidy
  -diff`, `go vet ./...`, `go tool staticcheck ./...`, `go tool govulncheck
  ./...` (no vulnerabilities), `go test -shuffle=on ./...`, coverage 94.6%
  against the 80% floor, `GOOS=windows go vet ./...`, the linux/arm64
  cross-build, and `claude-status preview` (pixels unchanged — the pixelui
  edit is provider selection, not rendering). `go test -race` still runs only
  in Linux CI; this host has no GCC.

### 2026-08-29 (follow-up) — the console window was a leftover Scheduled Task

- The console suppression above did not stop the flashing window on this
  machine, and the reason was machine state, not code: a Scheduled Task named
  `claude-status-relay` from before the Windows Service era was still
  registered and still running. `install-windows.ps1` switched the relay to a
  service but never removed the task it replaced, so both were live at once —
  the service (session 0, invisible) and the task (`LogonType: Interactive`,
  session 1, i.e. the actual desktop). The task's every `ssh` delivery opened
  a console window on screen, and both processes wrote the same `relay.log`
  and polled the same state directory.
- Worse, the task launched the *installed* binary at
  `%LOCALAPPDATA%\Programs\claude-status\claude-status.exe`, which was months
  old. That path is also what `statusLine`, all six hooks, and the Codex
  `notify` entry invoke — so rebuilding only `%USERPROFILE%\go\bin` (which is
  where `go install` puts it, and what `service install` had registered) left
  every hook running stale code. When checking whether a fix is live on
  Windows, check which of those two binaries the failing caller actually
  runs.
- Fixed on this host by unregistering the task, building once and copying the
  same binary to both paths, and re-running `service install` from the
  `%LOCALAPPDATA%\Programs` copy so the service, hooks, statusLine, and Codex
  notify all point at one file. `install-windows.ps1` now unregisters the
  legacy task (and stops a desktop-session relay holding the binary open)
  before it replaces the binary, so an upgrade cleans this up on its own.
