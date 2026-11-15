# Install Signet on Windows (PowerShell):  irm https://raw.githubusercontent.com/kahlkritzinger/signet/main/install.ps1 | iex
# Puts signet.exe in %LOCALAPPDATA%\Signet and adds it to your PATH. Nothing else is installed.
$ErrorActionPreference = "Stop"
$repo = "kahlkritzinger/signet"
$dir = Join-Path $env:LOCALAPPDATA "Signet"
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$exe = Join-Path $dir "signet.exe"
$url = "https://github.com/$repo/releases/latest/download/signet-windows-amd64.exe"
Write-Host "Downloading $url"
Invoke-WebRequest -Uri $url -OutFile "$exe.tmp"
$sums = (Invoke-WebRequest -Uri "https://github.com/$repo/releases/latest/download/checksums.txt").Content
$want = ($sums -split "`n" | Where-Object { $_ -match "signet-windows-amd64.exe" }) -replace "\s.*", ""
$have = (Get-FileHash "$exe.tmp" -Algorithm SHA256).Hash.ToLower()
if ($want.Trim() -ne $have) { Remove-Item "$exe.tmp"; throw "checksum mismatch, not installed" }
Move-Item -Force "$exe.tmp" $exe
$path = [Environment]::GetEnvironmentVariable("Path", "User")
if (($path -split ";") -notcontains $dir) { [Environment]::SetEnvironmentVariable("Path", "$path;$dir", "User") }
Write-Host "Installed $exe. Open a new terminal, then: signet set-default   (PDFs open in Signet)"
