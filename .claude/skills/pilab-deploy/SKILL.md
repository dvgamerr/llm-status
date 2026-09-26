---
name: pilab-deploy
description: Pushes local llm-status commits to origin, pulls and rebuilds them on the pilab Raspberry Pi over SSH, then restarts the llm-status-tty1 framebuffer service so the physical display picks up the change. Use when the user asks to "deploy to pilab", "push and build on pi", "sync the dashboard to pi", "reload tty1", "อัปเดต dashboard บน pi", or "push แล้ว build ที่ pi".
---

# Pi Lab Deploy

Deploys this repo's `llm-status` binary to the Raspberry Pi `pilab` and
reloads the display service that runs it. Four steps: verify local state,
push, pull+build on pilab, restart+verify the service.

## Critical

- `pilab` has no keyboard. All control goes through `ssh pilab` and
  `systemctl` — never send signals directly to the `llm-status` process
  and never try to control `/dev/tty1` by typing into some other terminal.
  `Restart=always` means killing the process just respawns it; the service
  is the thing to stop/start/restart. (See the project's `CLAUDE.md`
  "SSH-only control" section for the full rationale.)
- pilab's git checkout lives at `~/.lab` — that is **not** the same path as
  `~/.local/bin`, which is only the install prefix for the built binary.
- Never run `git push --force`, `git reset --hard`, or skip hooks anywhere
  in this flow. If a step fails, stop and report — don't work around it with
  a destructive command.
- If the local working tree has uncommitted changes, stop and ask the user
  whether to commit them first. Don't push a dirty tree silently, and don't
  assume the user wants those changes included.

## Instructions

### Step 1: Verify local state

```
git status --short
git log --oneline -1
```

If `git status --short` prints anything, stop and tell the user what's
uncommitted before proceeding — this skill deploys committed history, not
working-tree state.

### Step 2: Push to origin

```
git push origin <current-branch>
```

Read the output. If it's rejected (non-fast-forward, diverged branches),
stop — see Troubleshooting. Don't retry with `--force`.

### Step 3: Pull and build on pilab

```
ssh pilab "cd ~/.lab && git pull --ff-only && bash scripts/install.sh"
```

`scripts/install.sh` with no arguments builds `./cmd/llm-status` using
the Pi's own Go toolchain (`go build`) and installs the result to
`~/.local/bin/llm-status` — the exact path the systemd service execs.
If `go build` fails, the script exits non-zero before installing anything;
report the compiler error to the user and stop. Do not restart the service
on a failed build (the old binary stays in place, which is correct —
nothing to undo).

### Step 4: Reload the tty1 service and verify

```
ssh pilab "sudo systemctl restart llm-status-tty1.service"
ssh pilab "systemctl --no-pager --full status llm-status-tty1.service"
```

Confirm the status output shows `Active: active (running)` with a start
time matching this restart, and that there's no immediate crash-loop (a
`Main PID` that's already changed again on a second check a few seconds
later means it's crashing). If it isn't healthy, go straight to
Troubleshooting instead of restarting again.

## Examples

### Example: normal deploy after merging a feature

User says: "deploy ไป pilab หน่อย" or "push แล้ว build pi ให้ด้วย"

Actions: Steps 1-4 above, then confirm the deployed commit matches what was
pushed:

```
ssh pilab "cd ~/.lab && git rev-parse --short HEAD"
```

Report that hash alongside the local `git log --oneline -1` hash so the user
can see they match.

### Example: local tree has uncommitted work

User says: "deploy หน่อย" while `git status --short` shows modified files.

Actions: Stop at Step 1. Tell the user which files are uncommitted and ask
whether to commit, stash, or deploy from the last commit as-is (i.e. their
uncommitted changes will NOT reach pilab). Do not guess.

## Troubleshooting

### `git push` is rejected (non-fast-forward)

Cause: `origin/<branch>` has commits the local branch doesn't.

Solution: stop and tell the user to pull/rebase first. Never force-push to
resolve this automatically.

### `git pull --ff-only` fails on pilab

Cause: `~/.lab` has commits or uncommitted edits that aren't on origin (for
example a prior manual fix made directly on the Pi).

Solution: stop, run `ssh pilab "cd ~/.lab && git status"` and show the
user, then ask how to reconcile. Never `git reset --hard` on pilab without
explicit confirmation — that discards whatever is there.

### `scripts/install.sh` / `go build` fails on pilab

Cause: usually a compile error introduced by the pushed change, or a Go
toolchain mismatch.

Solution: paste the compiler error back to the user; don't attempt fixes
via SSH edits on pilab — fix the source locally, commit, and re-run this
skill from Step 2.

### Service won't reach `active (running)` after restart

Cause: build succeeded but the binary fails against the real
`/dev/fb0`/`/dev/tty1` (behavior can differ from `llm-status preview` on
a dev machine).

Solution: `ssh pilab "journalctl -u llm-status-tty1.service -n 50 --no-pager"`
and report what it shows; ask before making further changes.
