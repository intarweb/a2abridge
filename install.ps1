# install.ps1 — one-line installer for Windows (PowerShell 5.1+ or 7).
#
# Typical usage:
#   iwr -useb https://raw.githubusercontent.com/<owner>/a2abridge/main/install.ps1 | iex
#
# With flags:
#   $env:A2A_VERSION = "v0.2.0"; iwr -useb ... | iex
#
# Env overrides:
#   A2A_REPO         GitHub repo in owner/name (default: vbcherepanov/a2abridge)
#   A2A_VERSION      tag (default: latest release)
#   A2A_PREFIX       install prefix (default: $HOME\.a2abridge)
#   A2A_NO_SERVICE   "1" to skip Windows Service install
#   A2A_NO_IDE       "1" to skip writing IDE configs

[CmdletBinding()]
param(
    [string] $Version = $env:A2A_VERSION,
    [string] $Repo    = $(if ($env:A2A_REPO) { $env:A2A_REPO } else { "vbcherepanov/a2abridge" }),
    [string] $Prefix  = $(if ($env:A2A_PREFIX) { $env:A2A_PREFIX } else { Join-Path $HOME ".a2abridge" }),
    [switch] $DryRun
)

$ErrorActionPreference = "Stop"

# --- detect platform --------------------------------------------------
# Releases publish windows/amd64 and windows/arm64 only — fail fast on
# anything else instead of building a download URL that 404s.
$rawArch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
$arch = switch ($rawArch) {
    "AMD64" { "amd64" }
    "ARM64" { "arm64" }
    default { throw "unsupported architecture: $rawArch (published Windows builds: amd64, arm64)" }
}

# --- resolve version --------------------------------------------------
if (-not $Version) {
    Write-Host "→ resolving latest release for $Repo"
    $Version = (Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest").tag_name
}
if (-not $Version) {
    throw "could not resolve latest version"
}
Write-Host "→ installing a2abridge $Version (windows/$arch) into $Prefix"

# --- download + unzip -------------------------------------------------
$VersionStripped = $Version -replace "^v",""
$Asset = "a2abridge_${VersionStripped}_windows_${arch}.zip"
$Url   = "https://github.com/$Repo/releases/download/$Version/$Asset"
$Tmp   = New-Item -ItemType Directory -Path (Join-Path ([IO.Path]::GetTempPath()) ("a2abridge-install-" + [guid]::NewGuid().ToString("N").Substring(0,8)))
try {
    Write-Host "→ downloading $Url"
    $Zip = Join-Path $Tmp "a2abridge.zip"
    Invoke-WebRequest -Uri $Url -OutFile $Zip

    # --- verify checksum (mandatory) ----------------------------------
    Write-Host "→ verifying sha256 against checksums.txt"
    $SumsUrl  = "https://github.com/$Repo/releases/download/$Version/checksums.txt"
    $SumsFile = Join-Path $Tmp "checksums.txt"
    try {
        Invoke-WebRequest -Uri $SumsUrl -OutFile $SumsFile
    } catch {
        throw ("checksums.txt is missing from release $Version — this installer refuses unverified binaries. " +
               "Older tags were published without checksums; pin a release that ships checksums.txt, e.g.: " +
               '$env:A2A_VERSION = "vX.Y.Z"')
    }
    $Expected = $null
    foreach ($line in Get-Content $SumsFile) {
        $parts = $line.Trim() -split "\s+"
        if ($parts.Count -ge 2 -and $parts[1] -eq $Asset) { $Expected = $parts[0]; break }
    }
    if (-not $Expected) {
        throw "checksums.txt in release $Version has no entry for $Asset"
    }
    $Actual = (Get-FileHash -Algorithm SHA256 -Path $Zip).Hash
    if ($Actual -ne $Expected) { # -ne is case-insensitive in PowerShell
        throw "CHECKSUM MISMATCH for ${Asset}: expected $Expected, got $Actual. The download is corrupted or tampered with."
    }
    Write-Host "  sha256 OK: $Actual"

    Expand-Archive -Path $Zip -DestinationPath $Tmp -Force

    $BinDir = Join-Path $Prefix "bin"
    New-Item -ItemType Directory -Path $BinDir -Force | Out-Null
    Move-Item -Force (Join-Path $Tmp "a2abridge.exe") (Join-Path $BinDir "a2abridge.exe")
} finally {
    Remove-Item -Recurse -Force $Tmp
}

$Bin = Join-Path $Prefix "bin\a2abridge.exe"

# --- register IDEs + skill + hook -------------------------------------
if ($env:A2A_NO_IDE -ne "1") {
    Write-Host "→ registering MCP server in detected IDEs"
    $applyArgs = @("install")
    if (-not $DryRun) { $applyArgs += "--apply" }
    & $Bin @applyArgs
}

# --- service supervisor ----------------------------------------------
if (-not $DryRun -and $env:A2A_NO_SERVICE -ne "1") {
    Write-Host "→ installing directory daemon"
    try { & $Bin service install }
    catch { Write-Warning "service install failed — retry manually: $Bin service install" }
}

# --- summary ---------------------------------------------------------
Write-Host ""
Write-Host "a2abridge $Version installed."
Write-Host ""
Write-Host "  binary:  $Bin"
Write-Host "  doctor:  $Bin doctor"
Write-Host "  service: $Bin service status"
Write-Host ""
Write-Host "Add to PATH so a2abridge is reachable:"
Write-Host "  setx PATH `"`$env:PATH;$Prefix\bin`""
Write-Host ""
Write-Host "Restart your IDEs (Claude Code, Codex, Cursor, ...) to pick up the new MCP server."
