# Tests install.ps1 against a fake release served from a local HttpListener.
# Needs Go (for the stub binary) and an OpenSSL 3 (Git for Windows). Signs
# with a throwaway key; install.ps1's key block is rewritten in a copy only.
# Runs the cases under the PowerShell host that runs this script, so the
# workflow runs it once with powershell.exe (5.1) and once with pwsh (7).
# The cases need Windows (drive paths, the user PATH, .exe); -SelfTest also
# runs elsewhere when Go and OpenSSL 3 are installed.
#   release\test-install.ps1            all cases
#   release\test-install.ps1 -SelfTest  fixture builder, server and runner checks only
# Dot-sourcing (. release\test-install.ps1) loads the functions without running.
param([switch]$SelfTest)
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
$Here = Split-Path -Parent $MyInvocation.MyCommand.Path
$RepoRoot = (Resolve-Path (Join-Path $Here '..')).Path
$Work = Join-Path ([IO.Path]::GetTempPath()) ('agentbus-itest-' + [guid]::NewGuid().ToString('N'))
$Fx = Join-Path $Work 'fixture'
$Port = Get-Random -Minimum 20000 -Maximum 40000
$Base = "http://127.0.0.1:$Port"
$Server = $null
$OpenSsl = $null
$HostExe = (Get-Process -Id $PID).Path
$IsWin = [Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT
$Hidden = if ($IsWin) { @{ WindowStyle = 'Hidden' } } else { @{} }
$SavedUserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$script:Pass = 0; $script:Fail = 0

function Find-OpenSsl3 {
    foreach ($c in @('openssl', "$env:ProgramFiles\Git\usr\bin\openssl.exe", "$env:ProgramFiles\Git\mingw64\bin\openssl.exe")) {
        $cmd = Get-Command $c -ErrorAction SilentlyContinue
        if ($cmd -and ((& $cmd.Source version | Select-Object -First 1) -match '^OpenSSL 3')) { return $cmd.Source }
    }
    throw 'test-install.ps1: no OpenSSL 3 found (install Git for Windows)'
}

function Get-OpenSsl3 {
    if (-not $script:OpenSsl) { $script:OpenSsl = Find-OpenSsl3 }
    return $script:OpenSsl
}

# Invoke-Ssl runs OpenSSL and throws on a non-zero exit. No stderr
# redirection: under Windows PowerShell 5.1 with 'Stop', a redirected stderr
# line becomes a terminating NativeCommandError.
function Invoke-Ssl([string[]]$SslArgs) {
    $ssl = Get-OpenSsl3
    & $ssl @SslArgs | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "test-install.ps1: openssl $($SslArgs[0]) failed with exit code $LASTEXITCODE" }
}

# Cleanup runs on every exit path (success, failure, Ctrl-C) and twice is fine.
function Cleanup {
    if ($script:Server) {
        Stop-Process -Id $script:Server.Id -Force -ErrorAction SilentlyContinue
        $null = $script:Server.WaitForExit(3000)
        $script:Server = $null
    }
    # Only stubs started from this run's work directory: never the user's real agentbus sessions (the VM runs them).
    Get-Process -Name agentbus -ErrorAction SilentlyContinue | Where-Object { $_.Path -like "$Work*" } | Stop-Process -Force -ErrorAction SilentlyContinue
    if ([Environment]::GetEnvironmentVariable('Path', 'User') -ne $SavedUserPath) {
        [Environment]::SetEnvironmentVariable('Path', $SavedUserPath, 'User')
    }
    foreach ($i in 1..5) {
        if (-not (Test-Path -LiteralPath $Work)) { break }
        Remove-Item -LiteralPath $Work -Recurse -Force -ErrorAction SilentlyContinue
        if (Test-Path -LiteralPath $Work) { Start-Sleep -Milliseconds 300 }
    }
}

# Set-EnvMap sets process environment variables from the map (a null or empty
# value removes the variable) and returns what was there, for Restore-EnvMap.
function Set-EnvMap([hashtable]$Map) {
    $saved = @{}
    foreach ($k in $Map.Keys) {
        $saved[$k] = [Environment]::GetEnvironmentVariable($k)
        $v = $Map[$k]
        if ($v -is [string] -and $v -eq '') { $v = $null }
        [Environment]::SetEnvironmentVariable($k, $v)
    }
    return $saved
}

function Restore-EnvMap([hashtable]$Saved) {
    foreach ($k in $Saved.Keys) { [Environment]::SetEnvironmentVariable($k, $Saved[$k]) }
}

# Quote-Arg quotes one argument for Start-Process -ArgumentList, which joins
# its list with spaces and does no quoting of its own.
function Quote-Arg([string]$A) { return '"' + $A + '"' }

# Set-KeyBlock rewrites the $PubKeyPem here-string between the markers (the
# PowerShell twin of release/embed-key.sh).
function Set-KeyBlock([string]$Script, [string]$Pem) {
    $text = Get-Content -LiteralPath $Script -Raw
    $pattern = '(?s)(# BEGIN agentbus release public key\r?\n).*?(# END agentbus release public key)'
    if (-not [regex]::IsMatch($text, $pattern)) { throw "test-install.ps1: no key block markers in $Script" }
    $block = "`$PubKeyPem = @'`n" + $Pem.Trim() + "`n'@`n"
    $new = [regex]::Replace($text, $pattern, { param($m) $m.Groups[1].Value + $block + $m.Groups[2].Value })
    [IO.File]::WriteAllText($Script, $new)
}

function Add-Byte([string]$Path) { $s = [IO.File]::Open($Path, 'Append'); try { $s.WriteByte(0) } finally { $s.Close() } }

# Write-Sums writes SHA256SUMS for the two zips and install.ps1 in a release
# directory and signs it with the throwaway key.
function Write-Sums([string]$Dir, [string]$Tag) {
    $sums = foreach ($f in "agentbus-$Tag-windows-amd64.zip", "agentbus-$Tag-windows-arm64.zip", 'install.ps1') {
        (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $Dir $f)).Hash.ToLower() + '  ' + $f
    }
    [IO.File]::WriteAllText((Join-Path $Dir 'SHA256SUMS'), ($sums -join "`n") + "`n")
    Invoke-Ssl @('pkeyutl', '-sign', '-rawin', '-inkey', (Join-Path $Work 'key.pem'), '-in', (Join-Path $Dir 'SHA256SUMS'), '-out', (Join-Path $Dir 'SHA256SUMS.sig'))
}

function New-Release([string]$Variant, [string]$Tag, [string]$Version) {
    $d = Join-Path $Fx (Join-Path $Variant $Tag)
    New-Item -ItemType Directory -Force -Path $d | Out-Null
    $bin = Join-Path $d 'bin'
    New-Item -ItemType Directory -Force -Path $bin | Out-Null
    $savedCgo = Set-EnvMap @{ 'CGO_ENABLED' = '0' }
    Push-Location (Join-Path (Join-Path $Here 'testdata') 'stub')
    try {
        & go build -trimpath -ldflags "-s -w -X main.version=$Version" -o (Join-Path $bin 'agentbus.exe') .
        if ($LASTEXITCODE -ne 0) { throw 'stub build failed' }
    } finally { Pop-Location; Restore-EnvMap $savedCgo }
    foreach ($a in 'amd64', 'arm64') {
        Compress-Archive -LiteralPath (Join-Path $bin 'agentbus.exe') -DestinationPath (Join-Path $d "agentbus-$Tag-windows-$a.zip") -Force
    }
    Remove-Item -Recurse -Force $bin
    Copy-Item (Join-Path $Fx 'install.ps1') (Join-Path $d 'install.ps1')
    Write-Sums $d $Tag
}

# Build-Fixture: stub exe for two versions, zips, SHA256SUMS, signature, and
# the variants valid, tampered-zip, tampered-sums, tampered-sig,
# missing-entry, broken-exe (a zip whose agentbus.exe cannot run) and slow.
# Also install-placeholder.ps1 (a copy still holding the placeholder key) and
# fakes holding stand-in openssl.cmd files.
function Build-Fixture {
    New-Item -ItemType Directory -Force -Path $Fx | Out-Null
    Invoke-Ssl @('genpkey', '-algorithm', 'ed25519', '-out', (Join-Path $Work 'key.pem'))
    Invoke-Ssl @('pkey', '-in', (Join-Path $Work 'key.pem'), '-pubout', '-out', (Join-Path $Work 'key.pub'))
    Copy-Item (Join-Path $RepoRoot 'install.ps1') (Join-Path $Fx 'install.ps1')
    Set-KeyBlock (Join-Path $Fx 'install.ps1') (Get-Content -LiteralPath (Join-Path $Work 'key.pub') -Raw)
    Copy-Item (Join-Path $RepoRoot 'install.ps1') (Join-Path $Fx 'install-placeholder.ps1')
    Set-KeyBlock (Join-Path $Fx 'install-placeholder.ps1') ("-----BEGIN PUBLIC KEY-----`nREPLACED-BY-release/embed-key.sh`n-----END PUBLIC KEY-----")
    # fakes: OpenSSL 1.x; fakes3: claims OpenSSL 3 but every other call fails.
    $fakes = Join-Path $Fx 'fakes'; $fakes3 = Join-Path $Fx 'fakes3'
    New-Item -ItemType Directory -Force -Path $fakes, $fakes3 | Out-Null
    Set-Content -LiteralPath (Join-Path $fakes 'openssl.cmd') -Value '@echo OpenSSL 1.1.1w  11 Sep 2023'
    Set-Content -LiteralPath (Join-Path $fakes3 'openssl.cmd') -Value @('@echo off', 'if "%1"=="version" (echo OpenSSL 3.0.13 30 Jan 2024 & exit /b 0)', 'exit /b 1')
    foreach ($tag in 'v9.0.0', 'v9.0.1') { New-Release 'valid' $tag $tag.Substring(1) }
    Set-Content -LiteralPath (Join-Path (Join-Path $Fx 'valid') 'latest') -Value 'v9.0.1'
    foreach ($v in 'tampered-zip', 'tampered-sums', 'tampered-sig', 'missing-entry', 'broken-exe', 'slow') {
        New-Item -ItemType Directory -Force -Path (Join-Path $Fx $v) | Out-Null
        Copy-Item -Recurse (Join-Path $Fx 'valid/v9.0.1') (Join-Path $Fx "$v/v9.0.1")
        Set-Content -LiteralPath (Join-Path $Fx "$v/latest") -Value 'v9.0.1'
    }
    foreach ($a in 'amd64', 'arm64') { Add-Byte (Join-Path $Fx "tampered-zip/v9.0.1/agentbus-v9.0.1-windows-$a.zip") }
    $sums = Join-Path $Fx 'tampered-sums/v9.0.1/SHA256SUMS'
    $lines = Get-Content -LiteralPath $sums
    $lines[0] = $(if ($lines[0][0] -eq 'f') { 'E' } else { 'f' }) + $lines[0].Substring(1)
    [IO.File]::WriteAllText($sums, ($lines -join "`n") + "`n")
    Add-Byte (Join-Path $Fx 'tampered-sig/v9.0.1/SHA256SUMS.sig')
    $me = Join-Path $Fx 'missing-entry/v9.0.1'
    [IO.File]::WriteAllText((Join-Path $me 'SHA256SUMS'), ((Get-Content -LiteralPath (Join-Path $me 'SHA256SUMS') | Where-Object { $_ -match ' install.ps1$' }) -join "`n") + "`n")
    Invoke-Ssl @('pkeyutl', '-sign', '-rawin', '-inkey', (Join-Path $Work 'key.pem'), '-in', (Join-Path $me 'SHA256SUMS'), '-out', (Join-Path $me 'SHA256SUMS.sig'))
    # broken-exe: correctly hashed and signed, but agentbus.exe is text.
    $be = Join-Path $Fx 'broken-exe/v9.0.1'
    $junk = Join-Path $Work 'junk'
    New-Item -ItemType Directory -Force -Path $junk | Out-Null
    Set-Content -LiteralPath (Join-Path $junk 'agentbus.exe') -Value 'this is not an executable'
    foreach ($a in 'amd64', 'arm64') { Compress-Archive -LiteralPath (Join-Path $junk 'agentbus.exe') -DestinationPath (Join-Path $be "agentbus-v9.0.1-windows-$a.zip") -Force }
    Write-Sums $be 'v9.0.1'
}

function Test-ServerUp {
    try { Invoke-WebRequest -Uri "$script:Base/" -UseBasicParsing -ErrorAction Stop | Out-Null; return $true }
    catch {
        # An HTTP error (the 404 for /) still proves the server answers; a
        # refused connection has no response. PowerShell 7's connection
        # exceptions have no Response property, hence the lookup.
        $p = $_.Exception.PSObject.Properties['Response']
        return [bool]($p -and $p.Value)
    }
}

function Start-Server {
    foreach ($attempt in 1..3) {
        if ($attempt -gt 1) { $script:Port = Get-Random -Minimum 20000 -Maximum 40000; $script:Base = "http://127.0.0.1:$script:Port" }
        $argList = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', (Quote-Arg (Join-Path (Join-Path $Here 'testdata') 'fixture-server.ps1')), '-Root', (Quote-Arg $Fx), '-Port', "$script:Port")
        $script:Server = Start-Process -FilePath $HostExe -ArgumentList $argList -PassThru @Hidden
        foreach ($i in 1..50) {
            if ($script:Server.HasExited) { break }
            if (Test-ServerUp) { return }
            Start-Sleep -Milliseconds 100
        }
        # Port taken, or the server crashed: stop it and try another port.
        Stop-Process -Id $script:Server.Id -Force -ErrorAction SilentlyContinue
        $script:Server = $null
    }
    throw 'fixture server did not start after 3 attempts'
}

function Test-Verify([string]$Dir) {
    $ssl = Get-OpenSsl3
    & $ssl pkeyutl -verify -rawin -pubin -inkey (Join-Path $Work 'key.pub') -in (Join-Path $Dir 'SHA256SUMS') -sigfile (Join-Path $Dir 'SHA256SUMS.sig') 2>&1 | Out-Null
    return ($LASTEXITCODE -eq 0)
}

function Assert([string]$Name, [bool]$Ok) {
    if ($Ok) { $script:Pass++; Write-Host "PASS $Name" } else { $script:Fail++; Write-Host "FAIL $Name" }
}

# Get-Status returns the HTTP status of a request, 0 when there is none.
function Get-Status([string]$Url, [string]$Method) {
    $req = [System.Net.HttpWebRequest]::Create($Url); $req.Method = $Method; $req.AllowAutoRedirect = $false
    if ($Method -eq 'POST') { $req.ContentLength = 0 }
    try { $resp = $req.GetResponse() }
    catch [System.Net.WebException] { $resp = $_.Exception.Response }
    if (-not $resp) { return 0 }
    $code = [int]$resp.StatusCode
    $resp.Close()
    return $code
}

function ConvertTo-EncodedCommand([string]$Text) { return [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($Text)) }

# Get-ChildArgs builds the host arguments for one run mode:
#   file       -File <script> <args>                       (powershell -File)
#   iex        Get-Content -Raw <script> | Invoke-Expression    (irm | iex)
#   iex-dot    the same inside . { ... }
#   iex-catch  the same inside try/catch: prints THROWN: <message> and exits 7,
#              which proves a refusal is a throw and not an exit.
# iex modes use -EncodedCommand so $PSCommandPath is empty, as in a pasted
# or piped run.
function Get-ChildArgs([string]$Mode, [string]$ScriptPath, [string[]]$ScriptArgs) {
    $q = $ScriptPath.Replace("'", "''")
    $load = "Get-Content -Raw -LiteralPath '$q' | Invoke-Expression"
    switch ($Mode) {
        'file'      { return @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $ScriptPath) + @($ScriptArgs) }
        'iex'       { $cmd = $load }
        'iex-dot'   { $cmd = ". { $load }" }
        'iex-catch' { $cmd = "try { $load } catch { Write-Output ('THROWN: ' + `$_.Exception.Message); exit 7 }" }
        default     { throw "test-install.ps1: unknown run mode '$Mode'" }
    }
    return @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-EncodedCommand', (ConvertTo-EncodedCommand $cmd))
}

# Invoke-Child runs a script in a child host and returns its exit code and
# combined output with every whitespace character removed, so a line the host
# wrapped still matches.
function Invoke-Child([string]$Mode, [string]$ScriptPath, [string[]]$ScriptArgs) {
    $childArgs = Get-ChildArgs $Mode $ScriptPath $ScriptArgs
    # Windows PowerShell 5.1 raises a terminating NativeCommandError for
    # captured stderr under $ErrorActionPreference = 'Stop'; relax it around
    # the child run only (every refusal writes its throw to stderr).
    $eap = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
    try {
        $out = (& $HostExe @childArgs 2>&1 | ForEach-Object { "$_" }) -join "`n"
        $exit = $LASTEXITCODE
    } finally { $ErrorActionPreference = $eap }
    $out = $out -replace '\x1b\[[0-9;]*m', '' # pwsh 7 colors its error view
    return @{ Exit = $exit; Out = $out; Squashed = ($out -replace '\s', '') }
}

# Invoke-Case runs a script of the fixture (install.ps1 unless ScriptName says
# otherwise) in a child host with the given environment and compares exit
# code and output. Env maps variable names to values for the child; a null or
# empty value removes the variable. Before and After are scriptblocks run in
# this process around the child; After returns the extra condition.
function Invoke-Case([string]$Name, [int]$WantExit, [string]$WantOut, [hashtable]$Env, [string]$InstallDir, [scriptblock]$Before, [scriptblock]$After, [string]$Mode = 'file', [string]$ScriptName = 'install.ps1', [string[]]$ScriptArgs = @()) {
    $map = @{ 'AGENTBUS_BASE_URL' = "$script:Base/valid"; 'AGENTBUS_VERSION' = ''; 'AGENTBUS_INSTALL_DIR' = $InstallDir; 'AGENTBUS_SKIP_SIGNATURE' = ''; 'AGENTBUS_TEST_OSARCH' = '' }
    if ($Env) { foreach ($k in $Env.Keys) { $map[$k] = $Env[$k] } }
    $saved = Set-EnvMap $map
    try {
        if ($Before) { & $Before }
        $r = Invoke-Child $Mode (Join-Path $Fx $ScriptName) $ScriptArgs
        $extra = if ($After) { [bool](& $After) } else { $true }
        if ($r.Exit -eq $WantExit -and $r.Squashed.Contains(($WantOut -replace '\s', '')) -and $extra) { $script:Pass++; Write-Host "PASS $Name" }
        else { $script:Fail++; Write-Host "FAIL $Name (exit=$($r.Exit) want=$WantExit extra=$extra)"; Write-Host ($r.Out -replace '(?m)^', '    ') }
    } finally {
        Restore-EnvMap $saved
    }
}

function Get-UserPathEntries { return @(([Environment]::GetEnvironmentVariable('Path', 'User') + '') -split ';' | Where-Object { $_ }) }

function Invoke-SelfTest {
    Build-Fixture
    $v = Join-Path $Fx 'valid/v9.0.1'
    Assert 'self: sums list three files' ((Get-Content -LiteralPath (Join-Path $v 'SHA256SUMS')).Count -eq 3)
    Assert 'self: valid signature verifies' (Test-Verify $v)
    Assert 'self: tampered-sig fails' (-not (Test-Verify (Join-Path $Fx 'tampered-sig/v9.0.1')))
    Assert 'self: tampered-sums fails' (-not (Test-Verify (Join-Path $Fx 'tampered-sums/v9.0.1')))
    Assert 'self: tampered-zip keeps sums' ((Get-FileHash -LiteralPath (Join-Path $v 'SHA256SUMS')).Hash -eq (Get-FileHash -LiteralPath (Join-Path $Fx 'tampered-zip/v9.0.1/SHA256SUMS')).Hash)
    Assert 'self: tampered-zip differs from valid' ((Get-FileHash -LiteralPath (Join-Path $v 'agentbus-v9.0.1-windows-amd64.zip')).Hash -ne (Get-FileHash -LiteralPath (Join-Path $Fx 'tampered-zip/v9.0.1/agentbus-v9.0.1-windows-amd64.zip')).Hash)
    Assert 'self: missing-entry verifies and lacks zips' ((Test-Verify (Join-Path $Fx 'missing-entry/v9.0.1')) -and -not ((Get-Content -LiteralPath (Join-Path $Fx 'missing-entry/v9.0.1/SHA256SUMS')) -match 'windows'))
    $be = Join-Path $Fx 'broken-exe/v9.0.1'
    Assert 'self: broken-exe verifies and hashes its own zips' ((Test-Verify $be) -and (@(Get-Content -LiteralPath (Join-Path $be 'SHA256SUMS')) -contains ((Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $be 'agentbus-v9.0.1-windows-amd64.zip')).Hash.ToLower() + '  agentbus-v9.0.1-windows-amd64.zip')))
    Assert 'self: broken-exe differs from valid' ((Get-FileHash -LiteralPath (Join-Path $be 'agentbus-v9.0.1-windows-amd64.zip')).Hash -ne (Get-FileHash -LiteralPath (Join-Path $v 'agentbus-v9.0.1-windows-amd64.zip')).Hash)
    $pem = Get-Content -LiteralPath (Join-Path $Work 'key.pub')
    Assert 'self: install.ps1 copy carries the throwaway key' ((Get-Content -LiteralPath (Join-Path $Fx 'install.ps1') -Raw).Contains($pem[1]))
    $ph = Get-Content -LiteralPath (Join-Path $Fx 'install-placeholder.ps1') -Raw
    Assert 'self: placeholder copy holds the placeholder and the rest of the script' ($ph.Contains("`nREPLACED-BY-release/embed-key.sh`n") -and -not $ph.Contains($pem[1]) -and $ph.Contains('function Main'))
    Assert 'self: install.ps1 copy differs from the repo only in the key block' ((Get-Content -LiteralPath (Join-Path $Fx 'install.ps1')).Count -eq (Get-Content -LiteralPath (Join-Path $RepoRoot 'install.ps1')).Count)
    $threw = $false
    try { Set-KeyBlock (Join-Path $Fx 'fakes/openssl.cmd') 'x' } catch { $threw = $true }
    Assert 'self: Set-KeyBlock refuses a file without markers' $threw
    Expand-Archive -LiteralPath (Join-Path $Fx 'valid/v9.0.0/agentbus-v9.0.0-windows-amd64.zip') -DestinationPath (Join-Path $Work 'x') -Force
    if (-not $IsWin) { & chmod +x (Join-Path $Work 'x/agentbus.exe') } # Expand-Archive drops the mode on Unix
    Assert 'self: stub prints its version' ((& (Join-Path $Work 'x/agentbus.exe') version) -eq '9.0.0')
    if ($IsWin) {
        $fake3 = Join-Path $Fx 'fakes3/openssl.cmd'
        $ver3 = (& $fake3 version | Select-Object -First 1)
        & $fake3 pkeyutl | Out-Null
        Assert 'self: fakes3 openssl claims version 3 and fails everything else' (("$ver3" -like 'OpenSSL 3*') -and ($LASTEXITCODE -eq 1))
        Assert 'self: fakes openssl claims version 1' ((& (Join-Path $Fx 'fakes/openssl.cmd') version) -like 'OpenSSL 1.*')
    }

    # The environment helpers and the child runner.
    $before = [Environment]::GetEnvironmentVariable('AGENTBUS_VERSION')
    [Environment]::SetEnvironmentVariable('AGENTBUS_VERSION', 'keep-me')
    [Environment]::SetEnvironmentVariable('AGENTBUS_SELFTEST_B', 'b')
    try {
        $s = Set-EnvMap @{ 'AGENTBUS_VERSION' = 'changed'; 'AGENTBUS_SELFTEST_A' = 'a'; 'AGENTBUS_SELFTEST_B' = '' }
        $inside = ([Environment]::GetEnvironmentVariable('AGENTBUS_VERSION') -eq 'changed') -and ([Environment]::GetEnvironmentVariable('AGENTBUS_SELFTEST_A') -eq 'a') -and ($null -eq [Environment]::GetEnvironmentVariable('AGENTBUS_SELFTEST_B'))
        Restore-EnvMap $s
        Assert 'self: Set-EnvMap sets, empties and Restore-EnvMap restores' ($inside -and ([Environment]::GetEnvironmentVariable('AGENTBUS_VERSION') -eq 'keep-me') -and ($null -eq [Environment]::GetEnvironmentVariable('AGENTBUS_SELFTEST_A')) -and ([Environment]::GetEnvironmentVariable('AGENTBUS_SELFTEST_B') -eq 'b'))
    } finally {
        [Environment]::SetEnvironmentVariable('AGENTBUS_VERSION', $before)
        [Environment]::SetEnvironmentVariable('AGENTBUS_SELFTEST_A', $null)
        [Environment]::SetEnvironmentVariable('AGENTBUS_SELFTEST_B', $null)
    }
    $probe = Join-Path $Fx 'probe.ps1'
    [IO.File]::WriteAllText($probe, "Write-Output ('args=' + `$args.Count + ' v=' + `$env:AGENTBUS_VERSION)`nif (`$env:AGENTBUS_SELFTEST_FAIL) { throw 'boom message' }`n")
    $s = Set-EnvMap @{ 'AGENTBUS_VERSION' = 'v1.2.3'; 'AGENTBUS_SELFTEST_FAIL' = '' }
    try {
        $r = Invoke-Child 'file' $probe @('a', 'b')
        Assert 'self: Invoke-Child file mode passes arguments and environment' (($r.Exit -eq 0) -and $r.Squashed.Contains('args=2v=v1.2.3'))
        foreach ($m in 'iex', 'iex-dot') {
            $r = Invoke-Child $m $probe @()
            Assert "self: Invoke-Child $m mode runs the script with no arguments" (($r.Exit -eq 0) -and $r.Squashed.Contains('args=0v=v1.2.3'))
        }
        [Environment]::SetEnvironmentVariable('AGENTBUS_SELFTEST_FAIL', '1')
        $r = Invoke-Child 'file' $probe @()
        Assert 'self: Invoke-Child file mode reports a throw as exit 1 with the message' (($r.Exit -eq 1) -and $r.Squashed.Contains('boommessage'))
        $r = Invoke-Child 'iex-catch' $probe @()
        Assert 'self: Invoke-Child iex-catch mode proves the throw (exit 7)' (($r.Exit -eq 7) -and $r.Squashed.Contains('THROWN:boommessage'))
    } finally { Restore-EnvMap $s }
    $threw = $false
    try { Get-ChildArgs 'bogus' $probe @() | Out-Null } catch { $threw = $true }
    Assert 'self: Get-ChildArgs refuses an unknown mode' $threw

    # Invoke-Case itself: a match counts as a pass, any mismatch as a failure
    # (exit code, output, extra condition); the counters are put back after.
    $p0 = $script:Pass; $f0 = $script:Fail
    Invoke-Case 'self-probe-match' 1 'boom   message' @{ 'AGENTBUS_SELFTEST_FAIL' = '1' } '' $null { $true } 'file' 'probe.ps1'
    $gotPass = $script:Pass - $p0
    Invoke-Case 'self-probe-wrong-exit (expected FAIL, not counted)' 0 'boommessage' @{ 'AGENTBUS_SELFTEST_FAIL' = '1' } '' $null $null 'file' 'probe.ps1'
    Invoke-Case 'self-probe-wrong-output (expected FAIL, not counted)' 1 'not in the output' @{ 'AGENTBUS_SELFTEST_FAIL' = '1' } '' $null $null 'file' 'probe.ps1'
    Invoke-Case 'self-probe-wrong-extra (expected FAIL, not counted)' 1 'boommessage' @{ 'AGENTBUS_SELFTEST_FAIL' = '1' } '' $null { $false } 'file' 'probe.ps1'
    $gotFail = $script:Fail - $f0
    $script:Pass = $p0; $script:Fail = $f0
    Assert 'self: Invoke-Case passes one match and fails three mismatches' (($gotPass -eq 1) -and ($gotFail -eq 3))
    Assert 'self: Invoke-Case left no AGENTBUS_ variable behind' (-not (@('AGENTBUS_BASE_URL', 'AGENTBUS_INSTALL_DIR', 'AGENTBUS_SELFTEST_FAIL', 'AGENTBUS_SKIP_SIGNATURE', 'AGENTBUS_TEST_OSARCH') | Where-Object { [Environment]::GetEnvironmentVariable($_) }))

    # install.ps1's own entry point under every run form. The refusals tested
    # fire first in Main, so these run on any OS.
    Invoke-Case 'self: -File runs Main' 1 'AGENTBUS_SKIP_SIGNATURE must be 1 or unset' @{ 'AGENTBUS_SKIP_SIGNATURE' = 'yes' } '' $null $null
    Invoke-Case 'self: iex runs Main' 1 'AGENTBUS_SKIP_SIGNATURE must be 1 or unset' @{ 'AGENTBUS_SKIP_SIGNATURE' = 'yes' } '' $null $null 'iex'
    Invoke-Case 'self: . { iex } runs Main' 1 'AGENTBUS_SKIP_SIGNATURE must be 1 or unset' @{ 'AGENTBUS_SKIP_SIGNATURE' = 'yes' } '' $null $null 'iex-dot'
    Invoke-Case 'self: iex refusal is a throw' 7 'THROWN: agentbus install: AGENTBUS_SKIP_SIGNATURE must be 1 or unset' @{ 'AGENTBUS_SKIP_SIGNATURE' = 'yes' } '' $null $null 'iex-catch'

    Start-Server
    $srv = $script:Server
    $req = [System.Net.HttpWebRequest]::Create("$Base/valid/releases/latest"); $req.Method = 'HEAD'; $req.AllowAutoRedirect = $false
    $resp = $req.GetResponse(); $loc = $resp.Headers['Location']; $resp.Close()
    Assert 'self: server redirects latest' ($loc -like '*/valid/releases/tag/v9.0.1')
    $got = Join-Path $Work 'served-sums'
    Invoke-WebRequest -Uri "$Base/valid/releases/download/v9.0.1/SHA256SUMS" -UseBasicParsing -OutFile $got
    Assert 'self: server serves SHA256SUMS byte for byte' ((Get-FileHash -LiteralPath $got).Hash -eq (Get-FileHash -LiteralPath (Join-Path $v 'SHA256SUMS')).Hash)
    Assert 'self: server answers HEAD of a download with 200' ((Get-Status "$Base/valid/releases/download/v9.0.1/SHA256SUMS" 'HEAD') -eq 200)
    Assert 'self: server answers an unknown asset with 404' ((Get-Status "$Base/valid/releases/download/v9.0.1/nope.zip" 'GET') -eq 404)
    Assert 'self: server answers an unknown variant with 404' ((Get-Status "$Base/nope/releases/latest" 'GET') -eq 404)
    Assert 'self: server answers an unknown path with 404' ((Get-Status "$Base/valid/other" 'GET') -eq 404)
    Assert 'self: server refuses POST with 405' ((Get-Status "$Base/valid/releases/latest" 'POST') -eq 405)
    Cleanup
    Assert 'self: cleanup stopped the server' ($srv.HasExited)
    Assert 'self: cleanup removed the work directory' (-not (Test-Path -LiteralPath $Work))
    Assert 'self: user PATH restored' ([Environment]::GetEnvironmentVariable('Path', 'User') -eq $SavedUserPath)
}

function Invoke-Cases {
    $dir = Join-Path $Work 'bin'
    $noGit = @{ 'Path' = "$env:SystemRoot\System32;$env:SystemRoot"; 'ProgramFiles' = (Join-Path $Work 'no-programs') }
    $fakePath = @{ 'Path' = (Join-Path $Fx 'fakes') + ";$env:SystemRoot\System32;$env:SystemRoot"; 'ProgramFiles' = (Join-Path $Work 'no-programs') }
    $fake3Path = @{ 'Path' = (Join-Path $Fx 'fakes3') + ";$env:SystemRoot\System32;$env:SystemRoot"; 'ProgramFiles' = (Join-Path $Work 'no-programs') }
    Invoke-Case 'valid' 0 'installed agentbus 9.0.1' @{} $dir $null { (& (Join-Path $dir 'agentbus.exe') version) -eq '9.0.1' }
    Invoke-Case 'path-added' 0 'added' @{} (Join-Path $Work 'bin2') $null { (Get-UserPathEntries) -contains (Join-Path $Work 'bin2') }
    Invoke-Case 'pinned-version' 0 'installed agentbus 9.0.0' @{ 'AGENTBUS_VERSION' = 'v9.0.0' } $dir $null $null
    Invoke-Case 'version-no-v' 1 'version must look like vX.Y.Z' @{ 'AGENTBUS_VERSION' = '9.0.0' } $dir $null $null
    Invoke-Case 'latest-unparsable' 1 'could not determine the latest release' @{ 'AGENTBUS_BASE_URL' = "$Base/nope" } $dir $null $null
    Invoke-Case 'tampered-zip' 1 'checksum mismatch' @{ 'AGENTBUS_BASE_URL' = "$Base/tampered-zip" } $dir $null $null
    Invoke-Case 'tampered-sums' 1 'signature check of SHA256SUMS failed' @{ 'AGENTBUS_BASE_URL' = "$Base/tampered-sums" } $dir $null $null
    Invoke-Case 'tampered-sig' 1 'signature check of SHA256SUMS failed' @{ 'AGENTBUS_BASE_URL' = "$Base/tampered-sig" } $dir $null $null
    Invoke-Case 'sums-missing-entry' 1 'SHA256SUMS has no entry for' @{ 'AGENTBUS_BASE_URL' = "$Base/missing-entry" } $dir $null $null
    Invoke-Case 'no-openssl' 1 'winget install --id Git.Git -e' $noGit $dir $null $null
    Invoke-Case 'openssl-too-old' 1 'OpenSSL 1.1.1w' $fakePath $dir $null $null
    Invoke-Case 'skip-signature-valid' 0 'skipping the signature check' ($noGit + @{ 'AGENTBUS_SKIP_SIGNATURE' = '1' }) $dir $null $null
    Invoke-Case 'skip-signature-tampered' 1 'checksum mismatch' @{ 'AGENTBUS_SKIP_SIGNATURE' = '1'; 'AGENTBUS_BASE_URL' = "$Base/tampered-zip" } $dir $null $null
    Invoke-Case 'skip-signature-bad-value' 1 'AGENTBUS_SKIP_SIGNATURE must be 1 or unset' @{ 'AGENTBUS_SKIP_SIGNATURE' = 'yes' } $dir $null $null
    Invoke-Case 'unknown-arch' 1 'unsupported architecture: Riscv64' @{ 'AGENTBUS_TEST_OSARCH' = 'Riscv64' } $dir $null $null
    Invoke-Case 'relative-dir' 1 'must be an absolute path' @{} 'bin' $null $null
    $script:Running = $null
    Invoke-Case 'upgrade-while-running' 0 'upgraded: restart' @{} $dir { $script:Running = Start-Process -FilePath (Join-Path $dir 'agentbus.exe') -ArgumentList 'sleep' -PassThru @Hidden } {
        ((& (Join-Path $dir 'agentbus.exe') version) -eq '9.0.1') -and ((Get-ChildItem -LiteralPath $dir -Filter 'agentbus.exe.old-*').Count -eq 1)
    }
    if ($script:Running) { Stop-Process -Id $script:Running.Id -Force; Start-Sleep -Milliseconds 500 }
    Invoke-Case 'leftover-old-cleaned' 0 'installed agentbus' @{} $dir $null { (Get-ChildItem -LiteralPath $dir -Filter 'agentbus.exe.old-*').Count -eq 0 }
    # Two installers racing into a fresh directory. The environment is set
    # around both launches and put back at once: without it they would
    # install from github.com into the real install directory.
    $dir3 = Join-Path $Work 'bin3'
    $saved = Set-EnvMap @{ 'AGENTBUS_BASE_URL' = "$Base/valid"; 'AGENTBUS_INSTALL_DIR' = $dir3; 'AGENTBUS_VERSION' = ''; 'AGENTBUS_SKIP_SIGNATURE' = ''; 'AGENTBUS_TEST_OSARCH' = '' }
    try {
        $childArgs = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', (Quote-Arg (Join-Path $Fx 'install.ps1')))
        $c1 = Start-Process -FilePath $HostExe -ArgumentList $childArgs -PassThru @Hidden
        $c2 = Start-Process -FilePath $HostExe -ArgumentList $childArgs -PassThru @Hidden
    } finally { Restore-EnvMap $saved }
    $c1.WaitForExit(); $c2.WaitForExit()
    $served = $null
    if (Test-Path -LiteralPath (Join-Path $dir3 'agentbus.exe')) { $served = "$(& (Join-Path $dir3 'agentbus.exe') version)" }
    Assert 'concurrent' (($c1.ExitCode -eq 0) -and ($c2.ExitCode -eq 0) -and ($served -eq '9.0.1'))
    Invoke-Case 'failed-run-leaves-no-temp' 1 'checksum mismatch' @{ 'AGENTBUS_BASE_URL' = "$Base/tampered-zip" } $dir $null { (Get-ChildItem ([IO.Path]::GetTempPath()) -Filter 'agentbus-install-*' -Directory).Count -eq 0 }

    # Refusals and paths install.ps1 has beyond the plan.
    Invoke-Case 'version-suffix' 1 'version must look like vX.Y.Z' @{ 'AGENTBUS_VERSION' = 'v9.0.0-rc1' } $dir $null $null
    Invoke-Case 'version-newline' 1 'a value containing a newline' @{ 'AGENTBUS_VERSION' = "v9.0.0`n" } $dir $null $null
    Invoke-Case 'positional-argument' 1 'this script takes no arguments' @{} $dir $null $null 'file' 'install.ps1' @('v9.0.0')
    Invoke-Case 'placeholder-key' 1 'no embedded release key' @{} $dir $null $null 'file' 'install-placeholder.ps1'
    Invoke-Case 'install-path-is-directory' 1 'agentbus.exe is a directory' @{} (Join-Path $Work 'dirbin') { New-Item -ItemType Directory -Force -Path (Join-Path (Join-Path $Work 'dirbin') 'agentbus.exe') | Out-Null } $null
    Invoke-Case 'trailing-backslash-installs' 0 'installed agentbus 9.0.1 to' @{} "$Work\bin4\\\" $null {
        (Test-Path -LiteralPath (Join-Path (Join-Path $Work 'bin4') 'agentbus.exe')) -and ((Get-UserPathEntries) -contains (Join-Path $Work 'bin4'))
    }
    Invoke-Case 'trailing-slash-installs' 0 'installed agentbus 9.0.1 to' @{} "$Work\bin5/" $null {
        (Test-Path -LiteralPath (Join-Path (Join-Path $Work 'bin5') 'agentbus.exe')) -and ((Get-UserPathEntries) -contains (Join-Path $Work 'bin5'))
    }
    Invoke-Case 'empty-localappdata' 1 'LocalAppData is not set' @{ 'LocalAppData' = '' } '' $null $null
    Invoke-Case 'unwritable-install-dir' 1 'cannot write to' @{} (Join-Path (Join-Path $Work 'afile') 'sub') { [IO.File]::WriteAllText((Join-Path $Work 'afile'), 'x') } $null
    Invoke-Case 'iex-plain' 0 'installed agentbus 9.0.1' @{} (Join-Path $Work 'bin6') $null { (& (Join-Path (Join-Path $Work 'bin6') 'agentbus.exe') version) -eq '9.0.1' } 'iex'
    Invoke-Case 'iex-dot-block' 0 'installed agentbus 9.0.1' @{} (Join-Path $Work 'bin7') $null { (& (Join-Path (Join-Path $Work 'bin7') 'agentbus.exe') version) -eq '9.0.1' } 'iex-dot'
    Invoke-Case 'iex-refusal-throws' 7 'THROWN: agentbus install: version must look like vX.Y.Z' @{ 'AGENTBUS_VERSION' = '9.0.0' } $dir $null $null 'iex-catch'
    Invoke-Case 'version-check-reports-binary' 0 ('installed agentbus 9.0.1 to ' + (Join-Path (Join-Path $Work 'bin8') 'agentbus.exe')) @{} (Join-Path $Work 'bin8') $null $null
    Invoke-Case 'version-check-binary-does-not-run' 1 'agentbus.exe does not run' @{ 'AGENTBUS_BASE_URL' = "$Base/broken-exe" } (Join-Path $Work 'bin9') $null $null
    Invoke-Case 'openssl-fails-to-run' 1 'signature check of SHA256SUMS failed' $fake3Path $dir $null $null
    Invoke-Case 'base-url-trailing-slash' 0 'installed agentbus 9.0.1' @{ 'AGENTBUS_BASE_URL' = "$Base/valid/" } $dir $null $null

    $pub = Join-Path $RepoRoot 'release/agentbus-release-ed25519.pub'
    if (Test-Path -LiteralPath $pub) {
        $text = Get-Content -LiteralPath (Join-Path $RepoRoot 'install.ps1') -Raw
        $m = [regex]::Match($text, "(?s)\`$PubKeyPem = @'\r?\n(.*?)\r?\n'@")
        Assert 'key-matches-pub' ($m.Success -and (($m.Groups[1].Value -replace "`r", '').Trim() -eq ((Get-Content -LiteralPath $pub -Raw) -replace "`r", '').Trim()))
    } else { Assert "key-matches-pub ($pub missing: generate it per the #34 plan, Task 1, then run release/embed-key.sh)" $false }
}

function Invoke-Main {
    try {
        if ($SelfTest) { Invoke-SelfTest } else {
            Build-Fixture
            Start-Server
            Invoke-Cases
        }
    } catch {
        $script:Fail++
        Write-Host "FAIL harness error: $($_.Exception.Message)"
        Write-Host $_.ScriptStackTrace
    } finally { Cleanup }
    Write-Host "test-install.ps1 ($($PSVersionTable.PSVersion)): $script:Pass passed, $script:Fail failed"
    if ($script:Fail -ne 0) { exit 1 }
}

if ($MyInvocation.InvocationName -ne '.') { Invoke-Main }
