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
# below, verifies the zip's hash, then swaps agentbus.exe into place (a running
# copy is renamed, not overwritten) and adds the directory to the user PATH.
# Never runs agentbus init. A refusal throws, so a piped `iex` leaves the
# session open; `powershell -File install.ps1` exits 1.
#Requires -Version 5.1
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

# BEGIN agentbus release public key
$PubKeyPem = @'
-----BEGIN PUBLIC KEY-----
REPLACED-BY-release/embed-key.sh
-----END PUBLIC KEY-----
'@
# END agentbus release public key

$script:Upgrade = $false

function Get-Arch {
    $a = if ($env:AGENTBUS_TEST_OSARCH) { $env:AGENTBUS_TEST_OSARCH } else { "$([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture)" }
    switch ($a) {
        'X64'   { return 'amd64' }
        'Arm64' { return 'arm64' }
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

# Find-OpenSsl returns the first OpenSSL 3+ among: openssl on PATH, then Git
# for Windows' usr\bin\openssl.exe, mingw64\bin\openssl.exe and (ARM64 builds)
# clangarm64\bin\openssl.exe.
function Find-OpenSsl {
    $candidates = @()
    $onPath = Get-Command openssl -ErrorAction SilentlyContinue
    if ($onPath) { $candidates += $onPath.Source }
    $gitRoot = $null
    if (Get-Command git -ErrorAction SilentlyContinue) {
        try {
            $exec = (& git --exec-path 2>$null | Select-Object -First 1)
            if ($exec) { $gitRoot = (Resolve-Path (Join-Path $exec '..\..\..')).Path }
        } catch { $gitRoot = $null }
    }
    if (-not $gitRoot -and $env:ProgramFiles) { $gitRoot = Join-Path $env:ProgramFiles 'Git' }
    if ($gitRoot) {
        foreach ($rel in @('usr\bin\openssl.exe', 'mingw64\bin\openssl.exe', 'clangarm64\bin\openssl.exe')) {
            $p = Join-Path $gitRoot $rel
            if (Test-Path -LiteralPath $p) { $candidates += $p }
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

function Get-Asset([string]$Url, [string]$Out) {
    $pp = $ProgressPreference; $ProgressPreference = 'SilentlyContinue' # Windows PowerShell 5.1 crawls with the progress bar
    try { Invoke-WebRequest -Uri $Url -OutFile $Out -UseBasicParsing } catch { throw "agentbus install: download failed: $Url" } finally { $ProgressPreference = $pp }
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

# Move-WithRetry: a rename or move racing another installer is retried once.
function Move-WithRetry([string]$From, [string]$To) {
    try { Move-Item -LiteralPath $From -Destination $To -Force }
    catch { Start-Sleep -Milliseconds 500; Move-Item -LiteralPath $From -Destination $To -Force }
}

function Install-Binary([string]$Dir, [string]$Asset, [string]$InstallDir) {
    $extract = Join-Path $Dir 'extract'
    Expand-Archive -LiteralPath (Join-Path $Dir $Asset) -DestinationPath $extract -Force
    $new = Join-Path $extract 'agentbus.exe'
    if (-not (Test-Path -LiteralPath $new)) { throw "agentbus install: $Asset does not contain agentbus.exe" }
    try {
        New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
        $probe = Join-Path $InstallDir ('.agentbus-write-test-' + [guid]::NewGuid().ToString('N').Substring(0, 8))
        try { [IO.File]::WriteAllText($probe, 'x') }
        finally { Remove-Item -LiteralPath $probe -Force -ErrorAction SilentlyContinue }
    } catch { throw "agentbus install: cannot write to $InstallDir. Set AGENTBUS_INSTALL_DIR to a writable directory" }
    $target = Join-Path $InstallDir 'agentbus.exe'
    if (Test-Path -LiteralPath $target -PathType Container) { throw "agentbus install: $target is a directory; remove it or choose another AGENTBUS_INSTALL_DIR" }
    $script:Upgrade = Test-Path -LiteralPath $target
    if ($script:Upgrade) {
        # Windows allows renaming a running executable but not overwriting it.
        Move-WithRetry $target ("$target.old-" + [guid]::NewGuid().ToString('N').Substring(0, 8))
    }
    Move-WithRetry $new $target
    Get-ChildItem -LiteralPath $InstallDir -Filter 'agentbus.exe.old-*' | ForEach-Object {
        try { Remove-Item -LiteralPath $_.FullName -Force -ErrorAction Stop } catch { } # still running: a later run removes it
    }
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

# Get-InstalledVersion runs the installed binary. The output is collected
# whole (not piped to Select-Object -First 1, which stops the pipeline before
# the native exit code is recorded) and the exit code is seeded non-zero so a
# launch failure cannot read a stale 0.
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
        Get-Asset "$dl/$asset" (Join-Path $tmp $asset)
        Get-Asset "$dl/SHA256SUMS" (Join-Path $tmp 'SHA256SUMS')
        if (-not $skip) {
            Get-Asset "$dl/SHA256SUMS.sig" (Join-Path $tmp 'SHA256SUMS.sig')
            Test-Signature $openssl $tmp $base
        }
        Test-Hash $tmp $asset
        Install-Binary $tmp $asset $installDir
        Add-UserPath $installDir
        $v = Get-InstalledVersion (Join-Path $installDir 'agentbus.exe')
        Write-Host "installed agentbus $v to $installDir\agentbus.exe"
        Write-Host 'next: agentbus init --global (once per machine), then agentbus init inside each repository'
        if ($script:Upgrade) { Write-Host 'upgraded: restart every harness session and the TUI to pick up the new binary' }
    } finally {
        Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }
}

# Dot-sourcing (. .\install.ps1) loads the functions without installing.
if ($MyInvocation.InvocationName -ne '.' -or -not $PSCommandPath) { Main @args }
