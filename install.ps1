# keyto CLI installer (Windows / PowerShell).
#
#   irm https://raw.githubusercontent.com/hemfrid/keyto-hub-cli/main/install.ps1 | iex
#
# Downloads the windows/amd64 binary from the latest GitHub release,
# verifies it against the published sha256 checksums, installs it to
# %LOCALAPPDATA%\Programs\keyto, and adds that dir to your user PATH.
#
# Env overrides:
#   KEYTO_VERSION       release tag to install (default: latest), e.g. v0.1.0
$ErrorActionPreference = 'Stop'

$repo    = 'hemfrid/keyto-hub-cli'
$version = if ($env:KEYTO_VERSION) { $env:KEYTO_VERSION } else { 'latest' }
$asset   = 'keyto_windows_amd64.exe'
$base    = if ($version -eq 'latest') {
  "https://github.com/$repo/releases/latest/download"
} else {
  "https://github.com/$repo/releases/download/$version"
}

$tmp = Join-Path $env:TEMP ("keyto-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  Write-Host "Downloading $asset ($version)..."
  Invoke-WebRequest -Uri "$base/$asset"      -OutFile (Join-Path $tmp 'keyto.exe')
  Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt')

  # Verify sha256 (checksums.txt lines are: "<sha>  <asset>")
  $line = Select-String -Path (Join-Path $tmp 'checksums.txt') -SimpleMatch $asset | Select-Object -First 1
  if (-not $line) { throw "no checksum for $asset in checksums.txt" }
  $expected = ($line.Line -split '\s+')[0].ToLower()
  $actual   = (Get-FileHash (Join-Path $tmp 'keyto.exe') -Algorithm SHA256).Hash.ToLower()
  if ($actual -ne $expected) { throw "checksum mismatch for $asset (expected $expected, got $actual)" }

  $dir = Join-Path $env:LOCALAPPDATA 'Programs\keyto'
  New-Item -ItemType Directory -Path $dir -Force | Out-Null
  Copy-Item (Join-Path $tmp 'keyto.exe') (Join-Path $dir 'keyto.exe') -Force

  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  if (($userPath -split ';') -notcontains $dir) {
    [Environment]::SetEnvironmentVariable('Path', "$userPath;$dir", 'User')
    Write-Host "Added $dir to your user PATH (restart the shell to pick it up)."
  }

  Write-Host "Installed keyto -> $dir\keyto.exe"
  Write-Host "Next: run  keyto auth"
}
finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
