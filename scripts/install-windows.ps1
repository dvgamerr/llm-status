param(
    [string]$BinaryPath = "",
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA "Programs\llm-status"),
    [string]$MirrorHost = "pilab",
    [string]$RemoteBinary = "/home/pi/.local/bin/llm-status"
)

$ErrorActionPreference = "Stop"
$PSNativeCommandUseErrorActionPreference = $true
$RepoDir = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path

# The relay itself now runs as a real Windows Service (`llm-status
# service install`, internal/service/manager_windows.go) instead of a
# Scheduled Task — the same command also works unchanged on Linux
# (systemd --user) and macOS (launchd). `service install` stops and
# restarts an already-running instance on its own when re-run, so there's
# no separate "stop before replacing the binary" step needed here anymore.

if ([string]::IsNullOrWhiteSpace($BinaryPath)) {
    $BinaryPath = Join-Path $RepoDir "bin\llm-status.exe"
}
$BinaryPath = (Resolve-Path -LiteralPath $BinaryPath).Path
$Principal = [Security.Principal.WindowsPrincipal]::new([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $Principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw "install-windows.ps1 must run from an Administrator shell"
}
if ($MirrorHost -notmatch '^[A-Za-z0-9._@:-]+$' -or $MirrorHost.StartsWith('-')) {
    throw "invalid SSH mirror host: $MirrorHost"
}
if ($RemoteBinary -notmatch '^[A-Za-z0-9_./~-]+$') {
    throw "invalid remote binary path: $RemoteBinary"
}

$InstalledBinary = Join-Path $InstallDir "llm-status.exe"
$ClaudeSettingsPath = Join-Path $env:USERPROFILE ".claude\settings.json"
if (Test-Path -LiteralPath $ClaudeSettingsPath) {
    $ClaudeSettings = Get-Content -Raw -LiteralPath $ClaudeSettingsPath | ConvertFrom-Json -AsHashtable
}
else {
    $ClaudeSettings = [ordered]@{}
}
if ($ClaudeSettings -isnot [Collections.IDictionary]) {
    throw "Claude settings root must be a JSON object"
}
if ($ClaudeSettings.Contains("hooks") -and $ClaudeSettings["hooks"] -isnot [Collections.IDictionary]) {
    throw "Claude settings hooks value must be a JSON object"
}

# Parse both user configs before copying the binary or creating backups. In
# particular, a multiline or otherwise unsupported TOML notify value must fail
# closed instead of silently adding a second notify assignment.
$CodexConfigPath = Join-Path $env:USERPROFILE ".codex\config.toml"
if (-not (Test-Path -LiteralPath $CodexConfigPath)) {
    throw "Codex config was not found at $CodexConfigPath"
}
$CodexConfig = Get-Content -Raw -LiteralPath $CodexConfigPath
$NotifyMatches = [regex]::Matches($CodexConfig, '(?m)^\s*notify\s*=\s*(?<array>\[[^\r\n]*\])\s*(?:#.*)?$')
$AnyNotifyMatch = [regex]::Match($CodexConfig, '(?m)^\s*notify\s*=')
if ($NotifyMatches.Count -gt 1) {
    throw "Codex config contains multiple notify assignments"
}
if ($AnyNotifyMatch.Success -and $NotifyMatches.Count -eq 0) {
    throw "Codex notify must be a single-line TOML array before this installer can preserve it"
}
$NotifyMatch = $null
$ExistingNotify = @()
if ($NotifyMatches.Count -eq 1) {
    $NotifyMatch = $NotifyMatches[0]
    try {
        $ExistingNotify = @($NotifyMatch.Groups["array"].Value | ConvertFrom-Json)
    }
    catch {
        throw "Codex notify is not a JSON-compatible TOML string array: $($_.Exception.Message)"
    }
    if (@($ExistingNotify | Where-Object { $_ -isnot [string] }).Count -gt 0) {
        throw "Codex notify may contain only string arguments"
    }
}

$ForwardProgram = ""
$ForwardArguments = @()
if ($ExistingNotify.Count -ge 2 -and $ExistingNotify[0] -eq $InstalledBinary -and $ExistingNotify[1] -eq "codex-notify") {
    for ($Index = 2; $Index -lt $ExistingNotify.Count; $Index++) {
        if ($ExistingNotify[$Index] -eq "--forward" -and $Index + 1 -lt $ExistingNotify.Count) {
            $ForwardProgram = $ExistingNotify[++$Index]
        }
        elseif ($ExistingNotify[$Index] -eq "--forward-arg" -and $Index + 1 -lt $ExistingNotify.Count) {
            $ForwardArguments += $ExistingNotify[++$Index]
        }
        else {
            throw "existing llm-status Codex notify wrapper contains an unsupported or incomplete argument: $($ExistingNotify[$Index])"
        }
    }
    if ($ForwardArguments.Count -gt 0 -and [string]::IsNullOrWhiteSpace($ForwardProgram)) {
        throw "existing llm-status Codex notify wrapper has forward arguments but no forward program"
    }
}
elseif ($ExistingNotify.Count -gt 0) {
    $ForwardProgram = $ExistingNotify[0]
    if ($ExistingNotify.Count -gt 1) {
        $ForwardArguments = @($ExistingNotify[1..($ExistingNotify.Count - 1)])
    }
}

# Installs from before the Windows Service era registered the relay as a
# Scheduled Task named "claude-status-relay" (the tool's name at the time,
# before this tool was renamed to llm-status), and nothing has ever removed
# it on upgrade. That task runs with LogonType Interactive, so unlike the
# service it lives in the desktop session and every ssh delivery it makes
# opens a console window on screen; it also races the service for the same
# state directory and log file. Clear it out before the binary is replaced,
# since its running process would otherwise hold a lock on the file. The
# task and process names here are deliberately the pre-rename literals, not
# $RelayServiceName/$InstalledBinary — a real leftover from that era is
# named "claude-status-relay"/"claude-status.exe" regardless of what this
# tool is called today.
$LegacyTask = Get-ScheduledTask -TaskName "claude-status-relay" -ErrorAction SilentlyContinue
if ($LegacyTask) {
    Get-CimInstance Win32_Process -Filter "Name='claude-status.exe'" |
        Where-Object { $_.SessionId -ne 0 -and $_.CommandLine -like "* relay *" } |
        ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
    Unregister-ScheduledTask -TaskName "claude-status-relay" -Confirm:$false
    Write-Host "removed legacy Scheduled Task: claude-status-relay"
}

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
$StagedBinary = Join-Path $InstallDir ("llm-status." + [guid]::NewGuid().ToString("N") + ".exe")
Copy-Item -LiteralPath $BinaryPath -Destination $StagedBinary
try {
    $BinaryInstalled = $false
    for ($Attempt = 0; $Attempt -lt 100; $Attempt++) {
        try {
            Move-Item -LiteralPath $StagedBinary -Destination $InstalledBinary -Force
            $BinaryInstalled = $true
            break
        }
        catch {
            if ($Attempt -eq 99) {
                throw
            }
            Start-Sleep -Milliseconds 100
        }
    }
    if (-not $BinaryInstalled) {
        throw "failed to install $InstalledBinary"
    }
}
finally {
    if (Test-Path -LiteralPath $StagedBinary) {
        Remove-Item -LiteralPath $StagedBinary -Force
    }
}

$Timestamp = Get-Date -Format "yyyyMMdd-HHmmss"

function Backup-ConfigFile {
    param([string]$Path)
    if (Test-Path -LiteralPath $Path) {
        $BackupPath = "$Path.llm-status-backup-$Timestamp"
        Copy-Item -LiteralPath $Path -Destination $BackupPath
        return $BackupPath
    }
    return $null
}

function Write-Utf8Atomic {
    param(
        [string]$Path,
        [string]$Content
    )
    $Directory = Split-Path -Parent $Path
    New-Item -ItemType Directory -Force -Path $Directory | Out-Null
    $Temporary = Join-Path $Directory ("." + [IO.Path]::GetFileName($Path) + "." + [guid]::NewGuid().ToString("N") + ".tmp")
    try {
        [IO.File]::WriteAllText($Temporary, $Content, [Text.UTF8Encoding]::new($false))
        Move-Item -LiteralPath $Temporary -Destination $Path -Force
    }
    finally {
        if (Test-Path -LiteralPath $Temporary) {
            Remove-Item -LiteralPath $Temporary -Force
        }
    }
}

$ClaudeBackup = Backup-ConfigFile -Path $ClaudeSettingsPath
$ClaudeCommand = '"' + $InstalledBinary + '" ingest'
$ClaudeSettings["statusLine"] = [ordered]@{
    type = "command"
    command = $ClaudeCommand
    padding = 1
    refreshInterval = 5
}

# The activity command turns hook events into the dashboard's working / idle /
# needs-approval mascot state. It is registered on six hooks and is additive:
# any other hook already configured on the same event is left in place.
$ActivityCommand = '"' + $InstalledBinary + '" activity'

function Set-ClaudeStatusHook {
    param($Settings, [string]$EventName, [string]$Command, [string]$Matcher)

    if (-not $Settings.Contains("hooks")) {
        $Settings["hooks"] = [ordered]@{}
    }
    $Hooks = $Settings["hooks"]
    $ExistingGroups = @()
    if ($Hooks.Contains($EventName)) {
        $ExistingGroups = @($Hooks[$EventName])
    }

    # Drop any group this script previously installed for this event, then
    # re-add it fresh; leave every other tool's hook group untouched.
    $KeptGroups = @($ExistingGroups | Where-Object {
        $GroupHooks = @($_.hooks)
        -not ($GroupHooks | Where-Object { $_.command -like '*llm-status*" activity*' })
    })

    $NewGroup = [ordered]@{
        hooks = @([ordered]@{ type = "command"; command = $Command })
    }
    if (-not [string]::IsNullOrWhiteSpace($Matcher)) {
        $NewGroup = [ordered]@{ matcher = $Matcher; hooks = $NewGroup.hooks }
    }

    $Hooks[$EventName] = @($KeptGroups) + @($NewGroup)
    $Settings["hooks"] = $Hooks
}

Set-ClaudeStatusHook -Settings $ClaudeSettings -EventName "UserPromptSubmit" -Command $ActivityCommand
Set-ClaudeStatusHook -Settings $ClaudeSettings -EventName "PreToolUse" -Command $ActivityCommand -Matcher "*"
Set-ClaudeStatusHook -Settings $ClaudeSettings -EventName "Stop" -Command $ActivityCommand
Set-ClaudeStatusHook -Settings $ClaudeSettings -EventName "Notification" -Command $ActivityCommand
# SubagentStart/SubagentStop track how many Task-tool subagents are running
# concurrently, so the mascot can show "1 subagent" / "2+ subagents" instead
# of whatever the parent session's own last tool event happened to be.
Set-ClaudeStatusHook -Settings $ClaudeSettings -EventName "SubagentStart" -Command $ActivityCommand
Set-ClaudeStatusHook -Settings $ClaudeSettings -EventName "SubagentStop" -Command $ActivityCommand

$ClaudeJson = $ClaudeSettings | ConvertTo-Json -Depth 100
Write-Utf8Atomic -Path $ClaudeSettingsPath -Content ($ClaudeJson + [Environment]::NewLine)

$CodexBackup = Backup-ConfigFile -Path $CodexConfigPath

$NewNotify = @(
    $InstalledBinary,
    "codex-notify"
)
if (-not [string]::IsNullOrWhiteSpace($ForwardProgram)) {
    $NewNotify += @("--forward", $ForwardProgram)
    foreach ($ForwardArgument in $ForwardArguments) {
        $NewNotify += @("--forward-arg", [string]$ForwardArgument)
    }
}
$NotifyLine = "notify = " + ($NewNotify | ConvertTo-Json -Compress -AsArray)
if ($null -ne $NotifyMatch) {
    $CodexConfig = $CodexConfig.Remove($NotifyMatch.Index, $NotifyMatch.Length).Insert($NotifyMatch.Index, $NotifyLine)
}
else {
    $CodexConfig = $NotifyLine + [Environment]::NewLine + $CodexConfig
}
Write-Utf8Atomic -Path $CodexConfigPath -Content $CodexConfig

# Registering a Windows Service requires an elevated (Administrator) shell,
# unlike the old Scheduled Task registration this replaces.
& $InstalledBinary service install --mirror-ssh $MirrorHost --remote-bin $RemoteBinary --refresh 1s

Write-Host "installed binary: $InstalledBinary"
Write-Host "configured Claude statusLine: $ClaudeSettingsPath"
Write-Host "configured Codex notify: $CodexConfigPath"
if ($ClaudeBackup) { Write-Host "Claude backup: $ClaudeBackup" }
if ($CodexBackup) { Write-Host "Codex backup: $CodexBackup" }
