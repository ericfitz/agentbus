# install.ps1: install agentbus on Windows into %LocalAppData%\Programs\agentbus.
#   irm https://github.com/ericfitz/agentbus/releases/latest/download/install.ps1 | iex
# Windows PowerShell 5.1 and PowerShell 7. Environment:
#   AGENTBUS_VERSION        vX.Y.Z to install (default: the latest release)
#   AGENTBUS_INSTALL_DIR    absolute directory (default: %LocalAppData%\Programs\agentbus)
#   AGENTBUS_BASE_URL       mirror or test server (default: https://github.com/ericfitz/agentbus)
#   AGENTBUS_SKIP_SIGNATURE 1 skips the OpenSSL signature check (the checksum is still verified)
#   AGENTBUS_TEST_OSARCH    test hook: pretend the OS architecture is this value
# Downloads the zip, SHA256SUMS and SHA256SUMS.sig, verifies the Ed25519
# signature with OpenSSL 3 (Git for Windows ships one) and the public key
# below, verifies the zip's hash, runs the new agentbus.exe once in the
# temporary directory, then swaps it into place (whatever is there, running or
# not, is renamed aside, never overwritten) and adds the directory to the user
# PATH. Windows builds start at v1.14.1; an older AGENTBUS_VERSION is refused.
# Never runs agentbus init. A refusal throws, so a piped `iex` leaves the
# session open; `powershell -File install.ps1` exits 1.
#Requires -Version 5.1
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

# BEGIN agentbus release public key
$PubKeyPem = @'
-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAx7/YtImf3eM+x0+mN3CEmMiGsSZ404MWe0G7UModNsQ=
-----END PUBLIC KEY-----
'@
# END agentbus release public key

# Upgrade: agentbus.exe was already there. OldVersion: what it reported, or
# $null when it did not run (or vanished under a concurrent installer).
$script:Upgrade = $false
$script:OldVersion = $null

# Get-Arch reads the OS's native architecture from the registry, which neither
# WOW64 nor x64 emulation on ARM64 changes. Not RuntimeInformation: PSReadLine
# 3.0 defines its own System.Runtime.InteropServices.RuntimeInformation, with
# no OSArchitecture, and it shadows the real type in interactive Windows
# PowerShell 5.1 sessions.
function Get-Arch {
    $a = $env:AGENTBUS_TEST_OSARCH
    if (-not $a) {
        try { $a = (Get-ItemProperty -LiteralPath 'HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager\Environment' -Name PROCESSOR_ARCHITECTURE).PROCESSOR_ARCHITECTURE } catch { $a = $null }
        if (-not $a) { $a = 'unknown' }
    }
    switch ("$a") {
        'AMD64' { return 'amd64' }
        'ARM64' { return 'arm64' }
        default { throw "agentbus install: unsupported architecture: $a (releases cover windows-amd64 and windows-arm64)" }
    }
}

# Get-OpenSslVersion returns the "OpenSSL X.Y.Z ..." line the executable
# prints for `version`, or $null. Windows PowerShell 5.1 turns captured stderr
# into a terminating error under 'Stop' (an OpenSSL config warning is enough),
# so the call runs with the preference relaxed; the output is collected whole.
function Get-OpenSslVersion([string]$Exe) {
    $eap = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
    $global:LASTEXITCODE = 1 # a launch failure must not read a stale 0
    try { $out = @(& $Exe version 2>&1 | ForEach-Object { "$_" }) } catch { return $null } finally { $ErrorActionPreference = $eap }
    return ($out | Where-Object { $_ -match '^OpenSSL \d+\.' } | Select-Object -First 1)
}

function Get-OpenSslMajor([string]$Exe) {
    $v = Get-OpenSslVersion $Exe
    if ("$v" -match '^OpenSSL (\d+)\.') { return [int]$Matches[1] }
    return $null
}

# Find-OpenSsl returns the first OpenSSL 3+ among: openssl on PATH, then the
# usr\bin\openssl.exe, mingw64\bin\openssl.exe and (ARM64 builds)
# clangarm64\bin\openssl.exe of each Git for Windows root: the one git on PATH
# reports, ProgramFiles\Git, then ProgramW6432\Git (a 32-bit PowerShell sees
# ProgramFiles as "Program Files (x86)"; the 64-bit Git is under ProgramW6432).
function Find-OpenSsl {
    $candidates = @()
    $onPath = Get-Command openssl -ErrorAction SilentlyContinue
    if ($onPath) { $candidates += $onPath.Source }
    $roots = @()
    if (Get-Command git -ErrorAction SilentlyContinue) {
        try {
            $exec = (& git --exec-path 2>$null | Select-Object -First 1)
            if ($exec) { $roots += (Resolve-Path (Join-Path $exec '..\..\..')).Path }
        } catch { $roots = @() }
    }
    foreach ($programs in @($env:ProgramFiles, $env:ProgramW6432)) {
        if (-not $programs) { continue }
        $root = Join-Path $programs 'Git'
        if ($roots -notcontains $root) { $roots += $root }
    }
    foreach ($root in $roots) {
        foreach ($rel in @('usr\bin\openssl.exe', 'mingw64\bin\openssl.exe', 'clangarm64\bin\openssl.exe')) {
            $p = Join-Path $root $rel
            if ((Test-Path -LiteralPath $p) -and ($candidates -notcontains $p)) { $candidates += $p }
        }
    }
    $old = $null
    foreach ($cand in $candidates) {
        $major = Get-OpenSslMajor $cand
        if ($major -ge 3) { return $cand }
        if ($major -and -not $old) { $old = "$cand ($(Get-OpenSslVersion $cand))" }
    }
    if ($old) {
        throw "agentbus install: found $old, but verifying the release signature needs OpenSSL 3. Install Git for Windows (winget install --id Git.Git -e), which ships OpenSSL 3, or rerun with AGENTBUS_SKIP_SIGNATURE=1 to verify the hash only"
    }
    throw "agentbus install: OpenSSL 3 is required to verify the release signature and none was found. Install Git for Windows: winget install --id Git.Git -e (or rerun with AGENTBUS_SKIP_SIGNATURE=1 to verify the hash only)"
}

# Resolve-Tag: AGENTBUS_VERSION, else the tag releases/latest redirects to
# (no API call, so no anonymous rate limit).
function Resolve-Tag([string]$Base) {
    $tag = $env:AGENTBUS_VERSION
    if (-not $tag) {
        $loc = $null
        try {
            $req = [System.Net.HttpWebRequest]::Create("$Base/releases/latest")
            $req.Method = 'HEAD'
            $req.AllowAutoRedirect = $false
            $resp = $req.GetResponse()
            $loc = $resp.Headers['Location']
            $resp.Close()
        } catch { $loc = $null }
        if ($loc) { $tag = ($loc.TrimEnd('/') -split '/')[-1] }
        if (-not $tag) { throw "agentbus install: could not determine the latest release from $Base/releases/latest; set AGENTBUS_VERSION=vX.Y.Z" }
    }
    if ($tag.Contains("`n")) { throw 'agentbus install: version must look like vX.Y.Z, got a value containing a newline' }
    if ($tag -cnotmatch '^v[0-9]+\.[0-9]+\.[0-9]+\z') { throw "agentbus install: version must look like vX.Y.Z, got '$tag'" }
    return $tag
}

# Get-Asset downloads $Url to $Out. An HTTP 404 throws $NotFound when one is
# given (the zip of a release without a Windows build); every other failure
# is a download error. The status is read through PSObject.Properties: Windows
# PowerShell 5.1 throws System.Net.WebException and PowerShell 7
# Microsoft.PowerShell.Commands.HttpResponseException, both with a Response
# carrying StatusCode, but a connection failure has no Response at all and
# Set-StrictMode makes a missing property a terminating error.
function Get-Asset([string]$Url, [string]$Out, [string]$NotFound) {
    $pp = $ProgressPreference; $ProgressPreference = 'SilentlyContinue' # Windows PowerShell 5.1 crawls with the progress bar
    try { Invoke-WebRequest -Uri $Url -OutFile $Out -UseBasicParsing }
    catch {
        $status = 0
        $resp = $_.Exception.PSObject.Properties['Response']
        if ($resp -and $resp.Value) {
            $sc = $resp.Value.PSObject.Properties['StatusCode']
            if ($sc -and $sc.Value) { try { $status = [int]$sc.Value } catch { $status = 0 } }
        }
        if ($status -eq 404 -and $NotFound) { throw $NotFound }
        throw "agentbus install: download failed: $Url"
    } finally { $ProgressPreference = $pp }
}

function Test-Signature([string]$OpenSsl, [string]$Dir, [string]$Base) {
    $key = Join-Path $Dir 'release.pub'
    # The placeholder is assembled here so no other line of this file equals
    # the one release.sh and embed-key.sh look for.
    $placeholder = @('-----BEGIN PUBLIC KEY-----', ('REPLACED-BY-' + 'release/embed-key.sh'), '-----END PUBLIC KEY-----') -join "`n"
    if ((($PubKeyPem -replace "`r", '').Trim()) -ceq $placeholder) {
        throw "agentbus install: this copy of install.ps1 has no embedded release key; fetch it from $Base/releases/latest/download/install.ps1"
    }
    [IO.File]::WriteAllText($key, ($PubKeyPem -replace "`r", '') + "`n")
    # Windows PowerShell 5.1 turns captured stderr into a terminating error
    # under 'Stop'; openssl writes to stderr on a bad signature, so relax it
    # for this one call and report through the exit code.
    $eap = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
    $global:LASTEXITCODE = 1 # a launch failure must not read a stale 0
    try {
        & $OpenSsl pkeyutl -verify -rawin -pubin -inkey $key -in (Join-Path $Dir 'SHA256SUMS') -sigfile (Join-Path $Dir 'SHA256SUMS.sig') 2>&1 | Out-Null
        $code = $LASTEXITCODE
    } catch { $code = 1 } finally { $ErrorActionPreference = $eap }
    if ($code -ne 0) { throw "agentbus install: signature check of SHA256SUMS failed: the download is damaged, tampered with, or signed with a key this script does not know" }
}

function Test-Hash([string]$Dir, [string]$Asset) {
    $pattern = '^([0-9a-fA-F]{64})\s+\*?' + [regex]::Escape($Asset) + '$'
    $line = Get-Content -LiteralPath (Join-Path $Dir 'SHA256SUMS') | Where-Object { $_ -match $pattern } | Select-Object -First 1
    if (-not $line) { throw "agentbus install: SHA256SUMS has no entry for $Asset" }
    $want = ($line -split '\s+')[0].ToLower()
    $got = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $Dir $Asset)).Hash.ToLower()
    if ($got -ne $want) { throw "agentbus install: checksum mismatch for $Asset" }
}

# Install-Binary extracts agentbus.exe, runs it once where it was extracted
# (a download that does not run never replaces a working install), records
# the version the current binary reports, swaps the new one into place and
# returns the new version.
#
# The swap: Windows allows renaming a running executable but not overwriting
# or deleting it, so whatever sits at the target is renamed aside and the new
# file is then moved in without -Force. Two installers racing into the same
# directory each see the other's steps as a failed rename (the target
# vanished) or a failed move (a file appeared), and the loop starts over;
# neither ever deletes a binary the other may be running. Running the target
# is not part of the loop: the version check already happened on the
# extracted copy, so a target renamed aside by the other installer at the
# wrong moment cannot fail this one.
function Install-Binary([string]$Dir, [string]$Asset, [string]$InstallDir) {
    $extract = Join-Path $Dir 'extract'
    Expand-Archive -LiteralPath (Join-Path $Dir $Asset) -DestinationPath $extract -Force
    $new = Join-Path $extract 'agentbus.exe'
    if (-not (Test-Path -LiteralPath $new)) { throw "agentbus install: $Asset does not contain agentbus.exe" }
    $version = Get-InstalledVersion $new
    try {
        New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
        $probe = Join-Path $InstallDir ('.agentbus-write-test-' + [guid]::NewGuid().ToString('N').Substring(0, 8))
        try { [IO.File]::WriteAllText($probe, 'x') }
        finally { Remove-Item -LiteralPath $probe -Force -ErrorAction SilentlyContinue }
    } catch { throw "agentbus install: cannot write to $InstallDir. Set AGENTBUS_INSTALL_DIR to a writable directory" }
    $target = Join-Path $InstallDir 'agentbus.exe'
    if (Test-Path -LiteralPath $target -PathType Container) { throw "agentbus install: $target is a directory; remove it or choose another AGENTBUS_INSTALL_DIR" }
    $script:Upgrade = Test-Path -LiteralPath $target
    $script:OldVersion = $null
    if ($script:Upgrade) {
        # A binary that does not run, or that another installer renamed aside
        # between the test and the launch, is an upgrade from an unknown version.
        try { $script:OldVersion = Get-InstalledVersion $target } catch { $script:OldVersion = $null }
    }
    $placed = $false
    foreach ($attempt in 1..10) {
        try {
            if (Test-Path -LiteralPath $target) { Move-Item -LiteralPath $target -Destination ("$target.old-" + [guid]::NewGuid().ToString('N').Substring(0, 8)) }
            Move-Item -LiteralPath $new -Destination $target
            $placed = $true
            break
        } catch { Start-Sleep -Milliseconds 300 }
    }
    if (-not $placed) { throw "agentbus install: could not replace $target (another installer or a scanner kept it busy); rerun the installer" }
    Get-ChildItem -LiteralPath $InstallDir -Filter 'agentbus.exe.old-*' | ForEach-Object {
        try { Remove-Item -LiteralPath $_.FullName -Force -ErrorAction Stop } catch { } # still running: a later run removes it
    }
    return $version
}

# Add-UserPath appends $Dir to the user Path in the registry. The value is read
# and written as stored: GetEnvironmentVariable expands %VAR% entries and
# SetEnvironmentVariable writes REG_SZ, which would bake every expansion into
# the user's Path and change its type.
function Add-UserPath([string]$Dir) {
    $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment')
    try {
        $user = [string]$key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        $parts = @($user -split ';' | Where-Object { $_ })
        foreach ($part in $parts) {
            if ($part -eq $Dir -or [Environment]::ExpandEnvironmentVariables($part) -eq $Dir) { return }
        }
        $key.SetValue('Path', (($parts + $Dir) -join ';'), [Microsoft.Win32.RegistryValueKind]::ExpandString)
    } finally { $key.Close() }
    # SetEnvironmentVariable broadcasts WM_SETTINGCHANGE, which the registry
    # write above does not; deleting a variable that does not exist is the
    # cheapest way to send it, so running programs see the new Path.
    try { [Environment]::SetEnvironmentVariable('AGENTBUS_PATH_REFRESH', $null, 'User') } catch { }
    Write-Host "added $Dir to your user PATH; new terminals pick it up"
}

# Get-InstalledVersion runs an agentbus.exe (the extracted one before the swap,
# the installed one for its old version) and returns the version it prints,
# or throws. The output is collected whole (not piped to Select-Object -First
# 1, which stops the pipeline before the native exit code is recorded), stderr
# is left alone (a captured stderr line is a terminating error in Windows
# PowerShell 5.1) and the exit code is seeded non-zero so a launch failure
# cannot read a stale 0.
function Get-InstalledVersion([string]$Exe) {
    $global:LASTEXITCODE = 1
    $out = @()
    try { $out = @(& $Exe version) } catch { $out = @() }
    if ($LASTEXITCODE -ne 0 -or $out.Count -eq 0) { throw "agentbus install: $Exe does not run" }
    return "$($out[0])"
}

function Main {
    if ($args.Count -gt 0) { throw 'agentbus install: this script takes no arguments; configure it with AGENTBUS_VERSION, AGENTBUS_INSTALL_DIR, AGENTBUS_BASE_URL, AGENTBUS_SKIP_SIGNATURE' }
    $base = if ($env:AGENTBUS_BASE_URL) { $env:AGENTBUS_BASE_URL.TrimEnd('/', '\') } else { 'https://github.com/ericfitz/agentbus' }
    $skip = $false
    switch ("$env:AGENTBUS_SKIP_SIGNATURE") {
        ''      { $skip = $false }
        '1'     { $skip = $true; Write-Warning 'AGENTBUS_SKIP_SIGNATURE=1: skipping the signature check; the checksum is still verified' }
        default { throw "agentbus install: AGENTBUS_SKIP_SIGNATURE must be 1 or unset, got '$env:AGENTBUS_SKIP_SIGNATURE'" }
    }
    if (-not $env:AGENTBUS_INSTALL_DIR -and -not $env:LocalAppData) { throw 'agentbus install: LocalAppData is not set; set AGENTBUS_INSTALL_DIR to an absolute directory' }
    $installDir = if ($env:AGENTBUS_INSTALL_DIR) { $env:AGENTBUS_INSTALL_DIR } else { Join-Path $env:LocalAppData 'Programs\agentbus' }
    $installDir = $installDir.TrimEnd('\', '/')
    if ($installDir -match '^[A-Za-z]:$') { $installDir += '\' }
    if (-not [IO.Path]::IsPathRooted($installDir) -or $installDir -notmatch '^[A-Za-z]:\\|^\\\\') { throw "agentbus install: AGENTBUS_INSTALL_DIR must be an absolute path, got '$installDir'" }
    $arch = Get-Arch
    $openssl = if ($skip) { $null } else { Find-OpenSsl }
    $tag = Resolve-Tag $base
    $asset = "agentbus-$tag-windows-$arch.zip"
    $tmp = Join-Path ([IO.Path]::GetTempPath()) ('agentbus-install-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        $dl = "$base/releases/download/$tag"
        Get-Asset "$dl/$asset" (Join-Path $tmp $asset) "agentbus install: release $tag has no Windows build: $asset was not found at $dl/$asset. Windows builds start at v1.14.1; rerun with AGENTBUS_VERSION=v1.14.1 or later"
        Get-Asset "$dl/SHA256SUMS" (Join-Path $tmp 'SHA256SUMS')
        if (-not $skip) {
            Get-Asset "$dl/SHA256SUMS.sig" (Join-Path $tmp 'SHA256SUMS.sig')
            Test-Signature $openssl $tmp $base
        }
        Test-Hash $tmp $asset
        $v = Install-Binary $tmp $asset $installDir
        Add-UserPath $installDir
        Write-Host "installed agentbus $v to $installDir\agentbus.exe"
        Write-Host 'next: agentbus init --global (once per machine), then agentbus init inside each repository'
        if ($script:Upgrade) {
            if ($script:OldVersion -and $script:OldVersion -eq $v) { Write-Host "reinstalled: agentbus $v was already installed" }
            elseif ($script:OldVersion) { Write-Host "upgraded from $($script:OldVersion): restart every harness session and the TUI to pick up the new binary" }
            else { Write-Host 'upgraded: restart every harness session and the TUI to pick up the new binary' }
        }
    } finally {
        Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }
}

# Dot-sourcing (. .\install.ps1) loads the functions without installing.
if ($MyInvocation.InvocationName -ne '.' -or -not $PSCommandPath) { Main @args }
