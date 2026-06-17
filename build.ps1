# build.ps1 - build the Windows daemon exe with no console window.
# Requires Go (https://go.dev/dl/) on PATH, or $env:GOROOT pointing at a Go SDK.

$ErrorActionPreference = "Stop"
$env:CGO_ENABLED = "0"   # fully static, pure-Go single exe (no C toolchain needed)

$here = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $here

$go = (Get-Command go -ErrorAction SilentlyContinue).Source
if (-not $go -and $env:GOROOT) { $go = Join-Path $env:GOROOT "bin\go.exe" }
if (-not $go) {
    throw "Go not found. Install Go from https://go.dev/dl/ (or set `$env:GOROOT)."
}

# -H windowsgui : no console window flashes when REAPER launches the exe.
# -s -w         : strip symbol/debug info -> smaller exe.
& $go build -trimpath -ldflags "-H windowsgui -s -w" `
    -o "reaper-discord-presence.exe" ./cmd/reaper-discord-presence

if ($LASTEXITCODE -ne 0) { throw "go build failed ($LASTEXITCODE)" }

Write-Output "Built: $here\reaper-discord-presence.exe ($((Get-Item "$here\reaper-discord-presence.exe").Length) bytes)"
