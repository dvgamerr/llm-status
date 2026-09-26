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
`go run ./cmd/llm-status preview` and look at the resulting
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
dashboard has no buttons to press. `llm-status gfx --touch-device` can
point at a different evdev node or be set to `""` to disable it; a failure
to open the device is logged as a warning and the dashboard keeps running
without touch feedback, the same "secondary feature must never break the
primary display" pattern as `pixelui.resolveActivity`'s fallbacks. The
`llm-status-tty1.service` unit's `SupplementaryGroups=` must include
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
  `%LOCALAPPDATA%\Programs\llm-status\llm-status.exe`.
- Claude Code calls `ingest` through `%USERPROFILE%\.claude\settings.json`.
- Codex calls `codex-notify` through the external `notify` array in
  `%USERPROFILE%\.codex\config.toml`. Preserve and forward any notifier that
  was configured before llm-status is installed.
- Source adapters only persist sanitized local state; they never open the
  network. The long-lived `llm-status relay` process is the sole owner of
  SSH transport. On Windows, `scripts/install-windows.ps1` runs one relay via
  the `llm-status-relay` Windows Service. It retries changed snapshots and
  pipes only the allowlisted `model.Snapshot` to
  `/home/pi/.local/bin/llm-status import`.
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
  `llm-status activity` (`scripts/install-windows.ps1`'s
  `Set-ClaudeStatusHook`). That merge is additive: it only ever replaces the
  `llm-status ... activity` hook group it previously installed on each
  event and leaves every other tool's hook group on that event untouched.
- `llm-status activity` reads one hook payload from stdin and merges the
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
  `%LOCALAPPDATA%\llm-status\sessions\*.json` with genuine
  `rate_limits` (account-wide, so any fresh one is valid regardless of
  which session produced it). The independent relay notices that atomic
  local write and delivers it to `pilab`; the short-lived source process
  never waits for or launches SSH.
- Two sources can provide real numbers for a VS Code extension session:
  (a) `llm-status usage --five-hour PCT --seven-day PCT`
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
  `llm-status-tty1.service`. It runs
  `/home/pi/.local/bin/llm-status gfx --refresh 66ms --framebuffer /dev/fb0 --tty /dev/tty1`
  (~15fps; the flag floor is 20ms, so this is intentionally not maxed out)
  with `Restart=always`.
- Inspect it with
  `ssh pilab "systemctl --no-pager --full status llm-status-tty1.service"`.
  Start, stop, or restart it through SSH with `sudo systemctl`; do not try to
  control the service by sending keys to the physical console.
- `/home/pi/.local/bin` is not guaranteed to be in Fish's non-interactive
  `PATH`, so use the absolute path when invoking `llm-status` outside the
  service.

## Lessons learned from the failed control attempt

- Running `clear` in an ordinary SSH session cleared only that SSH terminal,
  not `/dev/tty1`.
- Running bare `llm-status` only prints command help; the primary dashboard
  command is `llm-status gfx`.
- Opening a visible PowerShell/SSH TUI on Windows did not update the Raspberry
  Pi's physical display and left an interactive session the user could not
  control from the Pi.
- Sending `TERM` or `KILL` directly to the dashboard process was the wrong
  control method: systemd immediately respawned it because the service uses
  `Restart=always`. Control `llm-status-tty1.service` instead.
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

### 2026-09-25 — `claude-status-relay` Windows Service replaced by `llm-proxy`

- The `claude-status-relay` Windows Service on this Windows host was
  deliberately uninstalled (`claude-status service remove`) and replaced in
  that same service slot by a new `llm-proxy` Windows Service. This was an
  explicit, confirmed decision to stop mirroring sanitized snapshots to
  `pilab` from this machine — **the `pilab` framebuffer dashboard no longer
  receives live updates from this host** until/unless `claude-status service
  install --mirror-ssh pilab` is run again. The `claude-status relay`,
  `service install/remove/start/stop/status` code paths are untouched and
  still work; only the OS-level auto-start registration was swapped.
- New `internal/llmproxy` (package) + `cmd/llm-proxy` (binary, built to
  `D:\home\go\bin\llm-proxy.exe` alongside `claude-status.exe`) is a small
  local reverse proxy: `NewHandler` in `internal/llmproxy/proxy.go` maps
  `/anthropic` → `https://api.anthropic.com` and `/openai` →
  `https://api.openai.com` via `httputil.ReverseProxy` per prefix, forwards
  `x-api-key`/`Authorization` headers unmodified, rewrites the outgoing
  `Host` header to the upstream's (the default director leaves the
  client's original `127.0.0.1:8787` Host, which upstreams would
  reject/misroute), and sets `FlushInterval: -1` so SSE responses stream
  token-by-token instead of buffering. `internal/llmproxy/app.go` mirrors
  `internal/app`'s CLI/service pattern (`llm-proxy serve`, `llm-proxy
  service install/remove/start/stop/status`), reusing the same
  cross-platform `internal/service` package claude-status's relay used.
- `ANTHROPIC_BASE_URL` was set (`setx`) to `http://127.0.0.1:8787/anthropic`
  so Claude Code CLI/Desktop/VS Code all route through the proxy once
  restarted — verified live against the real `api.anthropic.com` (a
  deliberately-invalid test key correctly got back a real 401 from
  Anthropic's edge, not a proxy error).
- Codex was deliberately **not** repointed at the proxy: Codex on this
  machine logs in via ChatGPT OAuth (`auth.json`, `model = "gpt-6-astra"`),
  and a custom `[model_providers.*]` with `base_url` only supports
  `env_key` (API-key) auth — switching `model_provider` would break the
  existing OAuth login immediately since no `OPENAI_API_KEY` is configured.
  Revisit only once a real OpenAI API key is available to set as `env_key`.

### 2026-09-25 (follow-up) — env-driven logging, clearer proxy error logs, per-route example payloads

- `internal/logging.New` now reads two env vars instead of always emitting
  plain text with no level filtering: `LOG_FORMAT=text` switches to the
  existing `zerolog.ConsoleWriter`, anything else (including unset) writes
  raw JSON; `LOG_LEVEL` parses any zerolog level name and defaults to
  `info`. Caught in testing: `zerolog.ParseLevel("")` returns `(NoLevel,
  nil)` — not an error — and `NoLevel` sorts above every real level, so
  naively calling `zerolog.SetGlobalLevel(ParseLevel(os.Getenv(...)))`
  would have silently suppressed all logging whenever `LOG_LEVEL` was
  unset. `levelFromEnv` special-cases the empty string to `InfoLevel`
  before ever calling `ParseLevel`. `.env`'s `LOG_LEVEL=debug` /
  `LOG_FORMAT=text` local overrides are unaffected; only the code's
  default-when-unset behavior changed. `New`'s signature is unchanged, so
  every existing caller across claude-status picked up the new behavior
  automatically.
- Diagnosed a real `llm-proxy` log the user pasted:
  `INF proxy request ... path=/anthropic/api/hello route=/anthropic`
  immediately followed by `WRN upstream request failed
  error="context canceled" ... path=/api/hello`. This is not a routing
  bug — `NewHandler` already forwards every sub-path under a configured
  prefix, and the request *did* match `/anthropic`. The confusing part was
  the two log lines showing different `path` values for what looked like
  the same request: `ReverseProxy.ErrorHandler` in Go's stdlib receives the
  already-Director-rewritten outbound request, so its `path` is the
  *upstream* path (`/api/hello`), not the client's original path
  (`/anthropic/api/hello`) the "proxy request" line logged. `newReverseProxy`
  now labels that field `upstream_path` and also logs `route` and
  `upstream_host` on every error, so the two log lines are easy to
  correlate instead of looking like two different requests. Separately,
  `context canceled` here means the *client* disconnected or hit its own
  short timeout before the round-trip to the real upstream finished — not
  a proxy failure — so `ErrorHandler` now checks
  `r.Context().Err() != nil && errors.Is(err, context.Canceled)` and logs
  that case at `Debug` instead of `Warn`, keeping `Warn` for upstream
  failures that are actually the proxy's problem.
- Added per-route example payload capture, gated by a new `--examples-dir`
  flag on both `llm-proxy serve` and `llm-proxy service install` (default
  `%UserConfigDir%/llm-proxy/examples`, matching the existing `--log-file`
  default pattern; empty string disables it). `newReverseProxy`'s
  `Director` and a new `ModifyResponse` sample up to 8 KiB of the
  request/response body via `peekBody` (reads ahead, then replaces the
  body with a reader that replays what was read followed by the rest of
  the original stream, so the real proxied call is unaffected) and write
  `<route>-request.json` / `<route>-response.json` under the configured
  directory. `Authorization` and `x-api-key` header values are replaced
  with the literal string `REDACTED` before writing — mirroring
  `examples/statusline-input.json`'s existing placeholder-value pattern —
  never persisted in plaintext, consistent with the "never mirror
  credentials" rule above.

### 2026-09-25 (follow-up 2) — `claude-status` renamed to `llm-status`

- Full-depth rename of the main dashboard/ingest tool from `claude-status`
  to `llm-status`, for consistent branding with `llm-proxy`: the Go module
  path (`github.com/dvgamerr/claude-status` → `.../llm-status`, cascading
  into every `internal/...` import including `llm-proxy`'s own, since both
  share this module), `cmd/claude-status` → `cmd/llm-status`, the state
  directory name and its env var override (`CLAUDE_STATUS_STATE_DIR` →
  `LLM_STATUS_STATE_DIR`), the relay service name (`claude-status-relay` →
  `llm-status-relay`), the pilab systemd unit
  (`claude-status-tty1.service` → `llm-status-tty1.service`, including the
  reference copy at `configs/`), the default remote binary path
  (`/home/pi/.local/bin/claude-status` → `.../llm-status`), and every CLI
  usage/version string. The GitHub repo itself was renamed too
  (`dvgamerr/claude-status` → `dvgamerr/llm-status`, via `gh repo rename`,
  with the local `origin` remote URL updated to match) — required because
  `README.md` documents `go install github.com/dvgamerr/llm-status/...`,
  which only resolves if the module path and the actual repo agree.
- **Clean rename, no backward-compat shim**, per explicit decision: existing
  local state under the old `claude-status` state directory (session/usage
  cache, not precious data) is not migrated, and there's no fallback that
  still reads `CLAUDE_STATUS_STATE_DIR`.
- **This pass deliberately did not touch any live deployment.** The
  `pilab` Raspberry Pi is still running the old `claude-status-tty1.service`
  unit against the old `/home/pi/.local/bin/claude-status` binary, and this
  Windows host's installed Windows Service and the hooks already written
  into `%USERPROFILE%\.claude\settings.json` still point at the old
  `claude-status.exe`. Redeploying both (rebuilding, reinstalling the
  service/hooks under the new name, and retiring the old service/unit) is a
  separate follow-up step — e.g. via the `pilab-deploy` skill for the Pi
  side and re-running `scripts/install-windows.ps1` for this host — not
  done as part of this rename.
- The rename was applied as one repo-wide literal-string substitution
  (`claude-status`→`llm-status`, `CLAUDE_STATUS`→`LLM_STATUS`) across every
  Go source file, `go.mod`, `Makefile`, the `scripts/*.sh`/`scripts/*.ps1`
  installers, `.github/workflows/ci.yml`, `configs/claude-settings.json`,
  and `.claude/skills/pilab-deploy/SKILL.md`, since the string never means
  anything else in this codebase. This file (`CLAUDE.md`) was the one
  deliberate exception: only the living-doc sections above this
  "Maintenance log" were rewritten — every dated entry above, including
  the two immediately preceding this one, was left exactly as originally
  written, since each describes what a service/binary was actually called
  on that date, and rewriting past entries to say `llm-status` would
  misrepresent that history.

### 2026-09-25 (follow-up 3) — Claude Code's `/api/hello` connectivity probe answered locally

- User reported the earlier `context canceled` fix wasn't enough — it was
  still happening. Root cause, found by actually reading Claude Code's
  behavior (not just the Go stdlib side): every Claude Code invocation
  fires a credential-free, fire-and-forget `HEAD /api/hello` at
  `${ANTHROPIC_BASE_URL}` on startup to warm up the TCP connection
  (`fetch(...).catch(() => {})` client-side — it never waits for or checks
  the response). Routing that through the full reverse-proxy round-trip to
  the real `api.anthropic.com` guaranteed the client would sometimes cancel
  before the round-trip finished, on every single session start — this
  wasn't a bug in the proxy's error handling, it was doing unnecessary work
  that was structurally exposed to being interrupted. Several other
  Claude-Code-compatible proxy projects independently converged on the same
  fix, which is now what `internal/llmproxy/proxy.go` does:
  `isAnthropicHelloProbe` matches `HEAD`/`GET
  /anthropic/api/hello` before it ever reaches `ReverseProxy`, and
  `respondAnthropicHello` answers it instantly and locally with Anthropic's
  own `{"message":"hello"}` shape — no upstream round-trip, no
  cancellation window, and it's logged at `Debug` (not `Info`) so it
  doesn't add a "proxy request" line to every session start either. The
  same path under `/openai` has no such convention and is still forwarded
  normally.

### 2026-09-25 (follow-up 4) — `llm-proxy` scoped to CLI/Desktop/VS Code clients only, no raw API-key traffic

- Explicit product decision (`/goal`): `llm-proxy` no longer forwards *any*
  request that doesn't look like it came from an actual Claude or Codex
  client — Claude Code CLI, the Claude Code VS Code extension, Claude
  Desktop, Codex CLI, or the Codex VS Code extension. A bare `curl` call or
  a direct Anthropic/OpenAI SDK integration hitting `/anthropic` or
  `/openai` now gets `403 Forbidden` instead of being forwarded, even
  though its credentials would otherwise have worked fine — this proxy is
  not meant to double as a general-purpose Anthropic/OpenAI API gateway.
- `internal/llmproxy/proxy.go`'s `isKnownCLIClient` is the gate, checked
  per-route right after the `/api/hello` probe short-circuit and before
  anything is forwarded:
  - `/anthropic`: `User-Agent` must start with `claude-cli/` (Claude Code
    CLI) or `claude-code/` (the VS Code extension shares the same
    underlying CLI, per Anthropic's own client-identification convention),
    or contain both `Claude` and `Electron` (Claude Desktop is an Electron
    app; this specific pattern isn't published by Anthropic but is
    documented by third-party enterprise network-filtering guides that
    need to tell Claude's CLI/Desktop/Web clients apart, e.g. Netskope's
    Claude client policy guide).
  - `/openai`: the `Originator` header must be `codex_cli_rs` (Codex CLI)
    or `codex_vscode` (the Codex VS Code extension) — OpenAI's own
    client-identification convention for Responses API requests. No
    distinct "Codex Desktop" client identifier was found in research;
    OpenAI doesn't appear to ship one separate from the CLI/VS Code
    extension, so only those two are recognized for `/openai` today.
- **This is explicitly a policy gate, not a security control** — every
  header it checks is ordinary and client-supplied, so anything (curl
  included) can set the same headers to claim to be one of these tools.
  It adds no protection beyond whatever `x-api-key`/`Authorization`
  already provide; its only job is keeping this proxy's traffic scoped to
  "came from a real Claude/Codex CLI, Desktop, or VS Code extension" per
  the stated goal, not gatekeeping who holds a valid key.
- Covered by `internal/llmproxy/proxy_test.go`:
  `TestNewHandlerAllowsKnownClaudeAndCodexClients` (table-driven, one case
  per recognized client) and `TestNewHandlerRejectsClientsWithoutAKnownCLISignature`
  (no headers, a raw SDK User-Agent, a raw `Authorization`-only OpenAI
  client, and an unrecognized `Originator` value — all expect 403 and
  confirm the upstream is never actually hit). Every pre-existing test that
  exercises real proxying was updated to set a recognized header, since
  they'd otherwise now get 403 themselves.
