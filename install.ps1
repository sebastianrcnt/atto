# Install or update atto from its GitHub releases.
#
#   irm https://raw.githubusercontent.com/sebastianrcnt/atto/main/install.ps1 | iex
#
# $env:ATTO_VARIANT = "slim" or "full" picks the binary variant (default: full).
# $env:ATTO_CHANNEL = "stable" or "edge" picks the channel (default: stable, the
# latest release; edge is the unstable build of main);
# $env:ATTO_VERSION = "v0.1.0" pins an exact release and wins over ATTO_CHANNEL;
# $env:ATTO_INSTALL_DIR picks the directory (default: %LOCALAPPDATA%\Programs\atto).
# Running it again installs the newest build of the channel over the old one.
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

$repo = "sebastianrcnt/atto"
$dir = if ($env:ATTO_INSTALL_DIR) { $env:ATTO_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "Programs\atto" }
$channel = if ($env:ATTO_CHANNEL) { $env:ATTO_CHANNEL } else { "stable" }
$version = $env:ATTO_VERSION
$variant = if ($env:ATTO_VARIANT) { $env:ATTO_VARIANT } else { "full" }

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    "AMD64" { "amd64" }
    "ARM64" { "arm64" }
    default { throw "atto install: unsupported CPU $env:PROCESSOR_ARCHITECTURE" }
}
$prefix = switch ($variant) {
    "full" { "atto" }
    "slim" { "atto-slim" }
    default { throw "atto install: unknown ATTO_VARIANT '$variant'; use slim or full" }
}
$asset = "${prefix}_windows_$arch.exe"

# ATTO_VERSION=edge and ATTO_VERSION=latest are older spellings of
# ATTO_CHANNEL=edge and ATTO_CHANNEL=stable; they are still accepted.
if ($version -eq "edge") { $channel = "edge"; $version = "" }
elseif ($version -eq "latest") { $version = "" }
if ($channel -ne "stable" -and $channel -ne "edge") { throw "atto install: unknown ATTO_CHANNEL '$channel'; use stable or edge" }

$base = if ($env:ATTO_DOWNLOAD_BASE) { $env:ATTO_DOWNLOAD_BASE }
    elseif ($version) { "https://github.com/$repo/releases/download/$version" }
    elseif ($channel -eq "edge") { "https://github.com/$repo/releases/download/edge" }
    else { "https://github.com/$repo/releases/latest/download" }

$tmp = Join-Path ([IO.Path]::GetTempPath()) ("atto-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    Write-Host "Downloading $asset ($(if ($version) { $version } else { $channel }))..."
    Invoke-WebRequest -UseBasicParsing "$base/$asset" -OutFile "$tmp\atto.exe"
    Invoke-WebRequest -UseBasicParsing "$base/checksums.txt" -OutFile "$tmp\checksums.txt"

    $want = Get-Content "$tmp\checksums.txt" | ForEach-Object {
        $f = $_ -split '\s+'
        if ($f.Count -ge 2 -and $f[1].TrimStart('*') -eq $asset) { $f[0].ToLower() }
    } | Select-Object -First 1
    if (-not $want) { throw "atto install: checksums.txt has no $asset" }
    $got = (Get-FileHash "$tmp\atto.exe" -Algorithm SHA256).Hash.ToLower()
    if ($got -ne $want) { throw "atto install: checksum mismatch for $asset; not installing" }

    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    $exe = Join-Path $dir "atto.exe"
    # A running atto.exe can't be overwritten but can be renamed.
    if (Test-Path $exe) { Move-Item -Force $exe "$exe.old" }
    Move-Item -Force "$tmp\atto.exe" $exe
    Remove-Item -Force "$exe.old" -ErrorAction SilentlyContinue
    Write-Host "Installed $(& $exe -version) to $exe"

    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if (($userPath -split ';') -notcontains $dir) {
        [Environment]::SetEnvironmentVariable("Path", "$userPath;$dir", "User")
        Write-Host "Added $dir to your user PATH; open a new terminal to use atto."
    }
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
