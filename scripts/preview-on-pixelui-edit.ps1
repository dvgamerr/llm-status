# PostToolUse hook: after Claude Code edits a file under internal/pixelui,
# regenerate pixel-dashboard-preview.png automatically, so the CLAUDE.md rule
# ("look at the preview before calling a pixelui change done") is enforced
# by the harness instead of relying on memory. Must always exit 0 — this is
# a convenience side effect, never allowed to block or fail the edit.
$ErrorActionPreference = "SilentlyContinue"

try {
    $payload = [Console]::In.ReadToEnd() | ConvertFrom-Json
}
catch {
    exit 0
}

$filePath = $payload.tool_input.file_path
if ([string]::IsNullOrWhiteSpace($filePath)) {
    exit 0
}
if ($filePath -notmatch "internal[\\/]pixelui") {
    exit 0
}

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Push-Location $repoRoot
try {
    go run ./cmd/llm-status preview *> $null
}
catch {
}
finally {
    Pop-Location
}
exit 0
