param(
    [string]$TranscriptPath = ""
)

$ErrorActionPreference = "Stop"
$PSNativeCommandUseErrorActionPreference = $true
$OutputEncoding = [Console]::OutputEncoding = [System.Text.UTF8Encoding]::new()
$RepoDir = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path

if ([string]::IsNullOrWhiteSpace($TranscriptPath)) {
    $TranscriptPath = Join-Path $RepoDir "artifacts\verify-transcript.txt"
}

$TranscriptDir = Split-Path -Parent $TranscriptPath
New-Item -ItemType Directory -Force -Path $TranscriptDir | Out-Null
Start-Transcript -Path $TranscriptPath -Force

try {
    Set-Location $RepoDir

    Write-Host "[1/17] gofmt (read-only check)"
    $GoFiles = @(rg --files -g "*.go")
    if ($GoFiles.Count -gt 0) {
        $Unformatted = @(gofmt -l $GoFiles)
        if ($Unformatted.Count -gt 0) {
            throw "gofmt required for: $($Unformatted -join ', ')"
        }
    }

    Write-Host "[2/17] PowerShell syntax"
    $PowerShellScripts = @(rg --files -g "*.ps1")
    foreach ($PowerShellScript in $PowerShellScripts) {
        $Tokens = $null
        $ParseErrors = $null
        [System.Management.Automation.Language.Parser]::ParseFile(
            (Join-Path $RepoDir $PowerShellScript),
            [ref]$Tokens,
            [ref]$ParseErrors
        ) | Out-Null
        if ($ParseErrors.Count -gt 0) {
            throw "PowerShell syntax error in $PowerShellScript`: $($ParseErrors[0].Message)"
        }
    }

    Write-Host "[3/17] POSIX shell syntax"
    $GitCommand = Get-Command git
    $GitRoot = (Resolve-Path (Join-Path (Split-Path -Parent $GitCommand.Source) "..")).Path
    $GitBash = Join-Path $GitRoot "bin\bash.exe"
    if (-not (Test-Path -LiteralPath $GitBash)) {
        throw "Git Bash not found at $GitBash"
    }
    $ShellScripts = @(rg --files -g "*.sh")
    & $GitBash -n @ShellScripts

    Write-Host "[4/17] JSON and SVG syntax"
    foreach ($JsonFile in @(rg --files -g "*.json")) {
        Get-Content -Raw -LiteralPath $JsonFile | ConvertFrom-Json | Out-Null
    }
    foreach ($SvgFile in @(rg --files -g "*.svg")) {
        $Document = [xml]::new()
        $Document.PreserveWhitespace = $true
        $Document.Load((Join-Path $RepoDir $SvgFile))
        if ($Document.DocumentElement.LocalName -ne "svg") {
            throw "$SvgFile does not have an svg root element"
        }
    }

    Write-Host "[5/17] Go module consistency"
    go mod verify
    go mod tidy -diff

    Write-Host "[6/17] staticcheck"
    go tool staticcheck ./...

    Write-Host "[7/17] vulnerability scan"
    go tool govulncheck ./...

    Write-Host "[8/17] repeated shuffled tests"
    go test -shuffle=on -count=3 ./...

    Write-Host "[9/17] coverage floor"
    $CoveragePath = Join-Path $RepoDir "artifacts\coverage.out"
    go test "-coverprofile=$CoveragePath" ./...
    $CoverageSummary = go tool cover "-func=$CoveragePath"
    $CoverageSummary | Write-Host
    $TotalCoverage = $CoverageSummary | Select-String '^total:' | Select-Object -Last 1
    if (-not $TotalCoverage -or $TotalCoverage.Line -notmatch '([0-9]+(?:\.[0-9]+)?)%') {
        throw "unable to parse total Go test coverage"
    }
    if ([double]$Matches[1] -lt 80) {
        throw "total Go test coverage $($Matches[1])% is below the 80% floor"
    }

    Write-Host "[10/17] go vet"
    go vet ./...

    Write-Host "[11/17] Windows build"
    $NativeBinary = Join-Path $RepoDir "artifacts\llm-status-verify.exe"
    go build -trimpath -o $NativeBinary ./cmd/llm-status

    Write-Host "[12/17] Raspberry Pi 4 linux/arm64 build"
    $ArmBinary = Join-Path $RepoDir "artifacts\llm-status-linux-arm64"
    $PreviousCGO = $env:CGO_ENABLED
    $PreviousGOOS = $env:GOOS
    $PreviousGOARCH = $env:GOARCH
    try {
        $env:CGO_ENABLED = "0"
        $env:GOOS = "linux"
        $env:GOARCH = "arm64"
        go build -trimpath -o $ArmBinary ./cmd/llm-status
    }
    finally {
        $env:CGO_ENABLED = $PreviousCGO
        $env:GOOS = $PreviousGOOS
        $env:GOARCH = $PreviousGOARCH
    }

    Write-Host "[13/17] ARM64 build metadata"
    $ArmMetadata = go version -m $ArmBinary
    if (($ArmMetadata -join "`n") -notmatch "GOOS=linux" -or ($ArmMetadata -join "`n") -notmatch "GOARCH=arm64") {
        throw "cross-built binary is not linux/arm64"
    }

    Write-Host "[14/17] Binary ingest smoke test"
    $SmokeDir = Join-Path $RepoDir ("artifacts\smoke-" + [guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Force -Path $SmokeDir | Out-Null
    try {
        $StatusLine = Get-Content -Raw (Join-Path $RepoDir "examples\statusline-input.json") |
            & $NativeBinary ingest --state-dir $SmokeDir
        Write-Host $StatusLine
        if ($StatusLine -notmatch "\[Opus\] 5h 51% .* 7d 34% .* ctx 72%") {
            throw "unexpected status line: $StatusLine"
        }
        $SnapshotPath = Join-Path $SmokeDir "latest.json"
        if (-not (Test-Path -LiteralPath $SnapshotPath)) {
            throw "ingest did not create latest.json"
        }
        $RawSnapshot = Get-Content -Raw -LiteralPath $SnapshotPath
        $Snapshot = $RawSnapshot | ConvertFrom-Json
        if ($Snapshot.session.id -ne "demo-session-abc123") {
            throw "unexpected persisted session ID"
        }
        if ($RawSnapshot -match "transcript_path|this/field/is/intentionally/not/persisted") {
            throw "sanitized snapshot contains transcript data"
        }
    }
    finally {
        if (Test-Path -LiteralPath $SmokeDir) {
            Remove-Item -LiteralPath $SmokeDir -Recurse -Force
        }
    }

    Write-Host "[15/17] Linux package smoke test"
    $PackageDir = Join-Path $RepoDir ("artifacts\packages-" + [guid]::NewGuid().ToString("N"))
    $env:LLM_STATUS_DIST_DIR = $PackageDir
    try {
        & $GitBash scripts/package.sh v0.0.0-verify
        $Packages = @(Get-ChildItem -LiteralPath $PackageDir -Filter "llm-status_v0.0.0-verify_linux_*.tar.gz")
        if ($Packages.Count -ne 2) {
            throw "expected 2 Linux packages, found $($Packages.Count)"
        }
        $Checksums = @(Get-Content -LiteralPath (Join-Path $PackageDir "SHA256SUMS"))
        if (($Checksums | Where-Object { $_ -match "llm-status_v0.0.0-verify_linux_" }).Count -ne 2) {
            throw "SHA256SUMS does not contain both verification packages"
        }

        Write-Host "[16/17] SHA256 verification"
        foreach ($Checksum in $Checksums) {
            if ($Checksum -notmatch '^([0-9a-fA-F]{64})\s+\*?\.?/?(.+)$') {
                throw "invalid checksum line: $Checksum"
            }
            $ExpectedHash = $Matches[1]
            $PackageName = $Matches[2]
            $ActualHash = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $PackageDir $PackageName)).Hash
            if ($ActualHash -ne $ExpectedHash) {
                throw "SHA256 mismatch for $PackageName"
            }
            Write-Host "SHA256_OK $PackageName"
        }
    }
    finally {
        Remove-Item Env:LLM_STATUS_DIST_DIR -ErrorAction SilentlyContinue
        if (Test-Path -LiteralPath $PackageDir) {
            Remove-Item -LiteralPath $PackageDir -Recurse -Force
        }
    }

    Write-Host "[17/17] patch whitespace"
    git diff --check

    Write-Host "VERIFY_OK"
}
finally {
    Stop-Transcript
}
