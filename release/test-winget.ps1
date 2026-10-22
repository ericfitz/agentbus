# Tests a set of winget manifests for ericfitz.agentbus the way the
# winget-pkgs validation pipeline does, before they are pushed: checks the
# manifests against the release's SHA256SUMS, runs `winget validate`, then for
# each architecture installs from the manifests, runs the installed
# executable with no arguments (the validator treats a non-zero exit as
# Validation-Executable-Error), checks its version and PE machine type, scans
# it with Microsoft Defender after a signature update, uninstalls, and checks
# nothing is left behind. Needs Windows, winget with local manifests enabled
# (once, from an elevated terminal: winget settings --enable LocalManifestFiles)
# and Microsoft Defender. Run from the repository root in a terminal that is
# NOT elevated: winget refuses to uninstall a user-scope package from an
# elevated one. MpCmdRun needs elevation, so the signature update and the
# scans run at the end in one elevated child process (one UAC prompt), on
# copies of each installed agentbus.exe taken before uninstalling:
#   powershell -ExecutionPolicy Bypass -File release\test-winget.ps1 -ManifestDir release\winget\1.16.0
#   ... -Architecture x64          one architecture (default: x64 and arm64)
#   ... -SumsFile <path>           use a local SHA256SUMS instead of the release's
#   ... -Cleanup                   uninstall ericfitz.agentbus left by a failed run
#   release\test-winget.ps1 -SelfTest  checks the harness's own functions (needs Go;
#                                      also runs outside Windows)
# Refuses to start when ericfitz.agentbus is already installed, so it never
# removes an install it did not make. Two runs at once are refused (a named
# mutex). Every install is uninstalled in a finally block; a run killed before
# that leaves the package installed, and the next run refuses until -Cleanup.
# winget does not find a package installed from local manifests by id
# (`winget uninstall --id` says no installed package matches), so uninstall
# goes through the same manifests.
# Dot-sourcing (. release\test-winget.ps1) loads the functions without running.
param(
    [switch]$SelfTest,
    [switch]$Cleanup,
    [string]$ManifestDir,
    [string[]]$Architecture = @('x64', 'arm64'),
    [string]$SumsFile
)
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
$Here = Split-Path -Parent $MyInvocation.MyCommand.Path
$RepoRoot = (Resolve-Path (Join-Path $Here '..')).Path
$IsWin = [Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT
$PackageId = 'ericfitz.agentbus'
$ManifestFiles = @("$PackageId.yaml", "$PackageId.installer.yaml", "$PackageId.locale.en-US.yaml")
$script:Pass = 0; $script:Fail = 0; $script:Threat = $false; $script:Leftover = $false
$NoPackageFound = -1978335212 # 0x8A150014 APPINSTALLER_CLI_ERROR_NO_APPLICATIONS_FOUND
# powershell -File passes -Architecture x64,arm64 as one string.
$Architecture = @($Architecture | ForEach-Object { $_ -split ',' } | Where-Object { $_ -ne '' })

function Assert([string]$Name, [bool]$Ok) {
    if ($Ok) { $script:Pass++; Write-Host "PASS $Name" } else { $script:Fail++; Write-Host "FAIL $Name" }
}

# Get-GoArch maps a winget architecture to the release asset's name for it.
function Get-GoArch([string]$Arch) {
    switch ($Arch) {
        'x64' { return 'amd64' }
        'arm64' { return 'arm64' }
        default { throw "test-winget: unsupported architecture '$Arch' (releases cover x64 and arm64)" }
    }
}

# Read-Yaml reads the subset of YAML the winget manifests use: top-level
# `Key: value` scalars, and the Installers list as one hashtable per item.
# Block scalars (ReleaseNotes: |-) and other lists are skipped.
function Read-Yaml([string]$Path) {
    $top = @{}; $items = @(); $cur = $null; $inInstallers = $false
    foreach ($line in [IO.File]::ReadAllLines($Path)) {
        if ($line -match '^([A-Za-z0-9]+):\s*(.*)$') {
            $inInstallers = ($Matches[1] -eq 'Installers')
            $top[$Matches[1]] = $Matches[2].Trim()
            if ($null -ne $cur) { $items += $cur; $cur = $null }
        } elseif ($inInstallers -and $line -match '^- ([A-Za-z0-9]+):\s*(.*)$') {
            if ($null -ne $cur) { $items += $cur }
            $cur = @{ $Matches[1] = $Matches[2].Trim() }
        } elseif ($inInstallers -and $null -ne $cur -and $line -match '^  ([A-Za-z0-9]+):\s*(.*)$') {
            $cur[$Matches[1]] = $Matches[2].Trim()
        }
    }
    if ($null -ne $cur) { $items += $cur }
    return @{ Top = $top; Installers = $items }
}

# Test-ManifestSet checks a manifest directory against SHA256SUMS lines and
# returns the problems found (none: an empty array) and the package version.
function Test-ManifestSet([string]$Dir, [string[]]$SumsLines) {
    $problems = @()
    $present = @(Get-ChildItem -LiteralPath $Dir -File | ForEach-Object { $_.Name })
    foreach ($f in $ManifestFiles) { if ($present -notcontains $f) { $problems += "missing manifest $f" } }
    foreach ($f in $present) { if ($ManifestFiles -notcontains $f) { $problems += "unexpected file $f (winget-pkgs takes exactly $($ManifestFiles -join ', '))" } }
    if ($problems.Count -gt 0) { return @{ Problems = $problems; Version = $null } }
    $docs = @{}
    foreach ($f in $ManifestFiles) { $docs[$f] = Read-Yaml (Join-Path $Dir $f) }
    $ver = $docs["$PackageId.yaml"].Top['PackageVersion']
    if (-not ($ver -match '^\d+\.\d+\.\d+$')) { $problems += "$PackageId.yaml: PackageVersion '$ver' is not a release version" }
    foreach ($f in $ManifestFiles) {
        $t = $docs[$f].Top
        $id = if ($t.ContainsKey('PackageIdentifier')) { $t['PackageIdentifier'] } else { '' }
        $v = if ($t.ContainsKey('PackageVersion')) { $t['PackageVersion'] } else { '' }
        if ($id -ne $PackageId) { $problems += "${f}: PackageIdentifier '$id', want '$PackageId'" }
        if ($v -ne $ver) { $problems += "${f}: PackageVersion '$v' differs from $PackageId.yaml's '$ver'" }
    }
    $sums = @{}
    foreach ($l in $SumsLines) { if ($l -match '^([0-9a-fA-F]{64})\s+\*?(\S+)$') { $sums[$Matches[2]] = $Matches[1].ToLower() } }
    $inst = @($docs["$PackageId.installer.yaml"].Installers)
    $arches = @($inst | ForEach-Object { if ($_.ContainsKey('Architecture')) { $_['Architecture'] } else { '' } })
    foreach ($want in @('x64', 'arm64')) { if ($arches -notcontains $want) { $problems += "installer manifest has no $want installer" } }
    foreach ($i in $inst) {
        $a = if ($i.ContainsKey('Architecture')) { $i['Architecture'] } else { '' }
        try { $goarch = Get-GoArch $a } catch { $problems += "installer manifest: unsupported Architecture '$a'"; continue }
        if (@($arches | Where-Object { $_ -eq $a }).Count -gt 1) { $problems += "installer manifest lists $a more than once" }
        $asset = "agentbus-v$ver-windows-$goarch.zip"
        $url = "https://github.com/ericfitz/agentbus/releases/download/v$ver/$asset"
        $gotUrl = if ($i.ContainsKey('InstallerUrl')) { $i['InstallerUrl'] } else { '' }
        $gotSha = if ($i.ContainsKey('InstallerSha256')) { $i['InstallerSha256'].ToLower() } else { '' }
        if ($gotUrl -ne $url) { $problems += "$a InstallerUrl '$gotUrl', want '$url'" }
        if (-not $sums.ContainsKey($asset)) { $problems += "SHA256SUMS has no line for $asset" }
        elseif ($gotSha -ne $sums[$asset]) { $problems += "$a InstallerSha256 $gotSha does not match SHA256SUMS $($sums[$asset]) for $asset" }
    }
    $notesUrl = $docs["$PackageId.locale.en-US.yaml"].Top['ReleaseNotesUrl']
    if ($notesUrl -ne "https://github.com/ericfitz/agentbus/releases/tag/v$ver") { $problems += "locale manifest: ReleaseNotesUrl '$notesUrl' is not v$ver's release" }
    return @{ Problems = $problems; Version = $ver }
}

# Get-LocalManifestSetting reads adminSettings.LocalManifestFiles from
# `winget settings export` output: $true, $false, or $null when the output
# has no such setting.
function Get-LocalManifestSetting([string]$Text) {
    $s = $Text.IndexOf('{'); $e = $Text.LastIndexOf('}')
    if ($s -lt 0 -or $e -le $s) { return $null }
    try { $j = $Text.Substring($s, $e - $s + 1) | ConvertFrom-Json } catch { return $null }
    if ($null -eq $j -or $null -eq $j.PSObject.Properties['adminSettings']) { return $null }
    if ($null -eq $j.adminSettings.PSObject.Properties['LocalManifestFiles']) { return $null }
    return [bool]$j.adminSettings.LocalManifestFiles
}

# Get-DefenderVerdict reads MpCmdRun -Scan's result. Exit 0 is no threats;
# exit 2 is threats found or a scan error, told apart by the output, which
# under -DisableRemediation lists detections ("found N threats"); anything
# else is an error.
function Get-DefenderVerdict([int]$Code, [string]$Out) {
    if ($Code -eq 0) { return 'clean' }
    if ($Code -eq 2 -and $Out -match 'found [1-9][0-9]* threats') { return 'threat' }
    return 'error'
}

# Test-ArchiveScanFailure reports whether winget install output says its
# malware scan of the downloaded archive failed or found something.
function Test-ArchiveScanFailure([string]$Out) { return ($Out -match '(?i)malware|virus|threat') }

# Test-Elevated reports whether this process runs as an elevated
# administrator.
function Test-Elevated {
    if (-not $IsWin) { return $false }
    $p = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
    return $p.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

# DefenderChild is the script the elevated child runs. Its one argument is a
# JSON file naming MpCmdRun (Mp), the files to scan (Files) and where to
# write the results (Out): one record per command with Kind (update or
# scan), File, Code and Out.
$DefenderChild = @'
$ErrorActionPreference = 'Continue'
$in = [IO.File]::ReadAllText($args[0]) | ConvertFrom-Json
$res = @()
function Run([string]$Kind, [string]$File, [string[]]$A) {
    $global:LASTEXITCODE = 0
    $o = @(& $in.Mp @A 2>&1 | ForEach-Object { "$_" })
    $script:res += [pscustomobject]@{ Kind = $Kind; File = $File; Code = $LASTEXITCODE; Out = ($o -join "`n") }
}
Run 'update' '' @('-SignatureUpdate')
foreach ($f in @($in.Files)) { Run 'scan' $f @('-Scan', '-ScanType', '3', '-File', $f, '-DisableRemediation') }
[IO.File]::WriteAllText($in.Out, (ConvertTo-Json -InputObject @($res) -Depth 3))
'@

# New-DefenderJob writes the child script and its input under $Dir and
# returns the paths: Script, Input, Out.
function New-DefenderJob([string]$Dir, [string]$Mp, [string[]]$Files) {
    $job = @{ Script = (Join-Path $Dir 'defender-child.ps1'); Input = (Join-Path $Dir 'defender-in.json'); Out = (Join-Path $Dir 'defender-out.json') }
    [IO.File]::WriteAllText($job.Script, $DefenderChild)
    [IO.File]::WriteAllText($job.Input, (ConvertTo-Json -InputObject @{ Mp = $Mp; Files = @($Files); Out = $job.Out }))
    return $job
}

# Read-DefenderResults reads the child's results, or $null when it wrote
# none (UAC declined, child failed).
function Read-DefenderResults([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) { return $null }
    # Windows PowerShell 5.1's ConvertFrom-Json emits a JSON array as one
    # object; foreach unrolls it on both hosts.
    $j = [IO.File]::ReadAllText($Path) | ConvertFrom-Json
    return , @(foreach ($i in $j) { $i })
}

# Get-DefenderFindings turns the child's records into checks (Name, Ok,
# Verdict, Record); Names maps each scanned file to its architecture label.
# A file in Names with no scan record is a failed check.
function Get-DefenderFindings($Results, [hashtable]$Names) {
    $out = @()
    foreach ($r in $Results) {
        if ($r.Kind -eq 'update') {
            $out += [pscustomobject]@{ Name = "Defender -SignatureUpdate (exit $(Format-Code $r.Code))"; Ok = ($r.Code -eq 0); Verdict = ''; Record = $r }
            continue
        }
        $label = if ($Names.ContainsKey($r.File)) { $Names[$r.File] } else { $r.File }
        $verdict = Get-DefenderVerdict $r.Code $r.Out
        $out += [pscustomobject]@{ Name = "$label Defender scan: $verdict (exit $(Format-Code $r.Code))"; Ok = ($verdict -eq 'clean'); Verdict = $verdict; Record = $r }
    }
    $scanned = @($Results | Where-Object { $_.Kind -eq 'scan' } | ForEach-Object { $_.File })
    foreach ($f in $Names.Keys) { if ($scanned -notcontains $f) { $out += [pscustomobject]@{ Name = "$($Names[$f]) Defender scan ran"; Ok = $false; Verdict = 'missing'; Record = $null } } }
    return , $out
}

function Assert-DefenderResults($Results, [hashtable]$Names) {
    foreach ($f in (Get-DefenderFindings $Results $Names)) {
        Assert $f.Name $f.Ok
        if ($null -ne $f.Record) { Show-Failure $f.Record }
        if ($f.Verdict -eq 'threat') { $script:Threat = $true }
    }
}

# Invoke-DefenderElevated runs the child elevated (one UAC prompt) and
# asserts on its results.
function Invoke-DefenderElevated([string]$Dir, [string]$Mp, [hashtable]$Names) {
    $job = New-DefenderJob $Dir $Mp @($Names.Keys)
    $hostExe = (Get-Process -Id $PID).Path
    try {
        $p = Start-Process -FilePath $hostExe -Verb RunAs -Wait -PassThru -WindowStyle Hidden -ArgumentList @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', "`"$($job.Script)`"", "`"$($job.Input)`"")
        $null = $p
    } catch { Assert "Defender steps run elevated ($($_.Exception.Message); accept the UAC prompt)" $false; return }
    $res = Read-DefenderResults $job.Out
    if ($null -eq $res) { Assert 'Defender child wrote its results' $false; return }
    Assert-DefenderResults $res $Names
}

# Find-MpCmdRun returns the newest Defender platform's MpCmdRun.exe, else
# the one under Program Files, else $null.
function Find-MpCmdRun {
    $plat = Join-Path $env:ProgramData 'Microsoft\Windows Defender\Platform'
    if (Test-Path -LiteralPath $plat) {
        $c = @(Get-ChildItem -LiteralPath $plat -Directory | Sort-Object { try { [version]($_.Name -replace '-.*$', '') } catch { [version]'0.0' } } -Descending |
            ForEach-Object { Join-Path $_.FullName 'MpCmdRun.exe' } | Where-Object { Test-Path -LiteralPath $_ })
        if ($c.Count -gt 0) { return $c[0] }
    }
    $pf = Join-Path $env:ProgramFiles 'Windows Defender\MpCmdRun.exe'
    if (Test-Path -LiteralPath $pf) { return $pf }
    return $null
}

# Get-PackageDirs lists winget's portable install folders for the package
# under a LocalAppData root.
function Get-PackageDirs([string]$LocalAppData) {
    $p = Join-Path $LocalAppData 'Microsoft\WinGet\Packages'
    if (-not (Test-Path -LiteralPath $p)) { return @() }
    return @(Get-ChildItem -LiteralPath $p -Directory | Where-Object { $_.Name -like "${PackageId}_*" } | ForEach-Object { $_.FullName })
}

function Get-LinksExe([string]$LocalAppData) { return (Join-Path $LocalAppData 'Microsoft\WinGet\Links\agentbus.exe') }

# Get-Leftovers lists what a portable install of the package leaves when it
# is still (partly) installed: the Links alias, package folders, and user
# PATH entries naming a package folder. The Links folder itself on PATH is
# winget's and shared by every portable package.
function Get-Leftovers([string]$LocalAppData, [string]$UserPath) {
    $left = @()
    $links = Get-LinksExe $LocalAppData
    if (Test-Path -LiteralPath $links) { $left += $links }
    $left += @(Get-PackageDirs $LocalAppData)
    foreach ($e in ("$UserPath" -split ';')) { if (($e -replace '/', '\') -like "*\WinGet\Packages\${PackageId}_*") { $left += "user PATH entry $e" } }
    return , $left
}

# Get-StalePathEntries returns the entries of a PATH value that name one of
# the package's winget folders that no longer exists ($Exists tests a path).
function Get-StalePathEntries([string]$UserPath, [scriptblock]$Exists) {
    return , @("$UserPath" -split ';' | Where-Object { (($_ -replace '/', '\') -like "*\WinGet\Packages\${PackageId}_*") -and -not (& $Exists $_) })
}

# Remove-UserPathEntries drops entries from the user Path registry value,
# keeping its value kind and the other entries as stored.
function Remove-UserPathEntries([string[]]$Entries) {
    $k = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
    try {
        $v = [string]$k.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        $kind = $k.GetValueKind('Path')
        $k.SetValue('Path', ((@($v -split ';') | Where-Object { $Entries -notcontains $_ }) -join ';'), $kind)
    } finally { $k.Close() }
    # Let running programs see the change, as setting a user variable does.
    try { [Environment]::SetEnvironmentVariable('AGENTBUS_PATH_REFRESH', $null, 'User') } catch { }
}

function Get-UserPath {
    if (-not $IsWin) { return '' }
    $k = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment')
    if ($null -eq $k) { return '' }
    try { return [string]$k.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames) } finally { $k.Close() }
}

# Get-PeMachine returns the architecture in a PE file's header: x64, arm64,
# x86, or the machine value in hex.
function Get-PeMachine([string]$Path) {
    $b = [IO.File]::ReadAllBytes($Path)
    if ($b.Length -lt 64 -or $b[0] -ne 0x4D -or $b[1] -ne 0x5A) { return 'not-pe' }
    $off = [BitConverter]::ToInt32($b, 0x3C)
    if ($off -lt 0 -or $off + 6 -gt $b.Length -or [BitConverter]::ToUInt32($b, $off) -ne 0x00004550) { return 'not-pe' }
    $m = [BitConverter]::ToUInt16($b, $off + 4)
    switch ($m) { 0x8664 { return 'x64' } 0xAA64 { return 'arm64' } 0x014C { return 'x86' } default { return ('0x{0:X4}' -f $m) } }
}

# Invoke-Exe runs a program with stderr merged into stdout and returns its
# exit code and output. Windows PowerShell 5.1 turns captured stderr into a
# terminating error under 'Stop', so this runs under 'Continue'.
function Invoke-Exe([string]$Path, [string[]]$Arguments = @()) {
    $eap = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
    try {
        $global:LASTEXITCODE = 0
        $out = @(& $Path @Arguments 2>&1 | ForEach-Object { "$_" })
        return [pscustomobject]@{ Code = $LASTEXITCODE; Out = ($out -join "`n") }
    } finally { $ErrorActionPreference = $eap }
}

function Format-Code([int]$Code) { if ($Code -lt 0) { return ('{0} (0x{1:X8})' -f $Code, $Code) } return "$Code" }

# Show-Failure prints a failed command's output, indented, so the summary
# stays readable.
function Show-Failure($R) { if ($R.Out) { ($R.Out -split "`n") | ForEach-Object { Write-Host "    $_" } } }

function Invoke-Winget([string[]]$Arguments) { return Invoke-Exe 'winget' $Arguments }

function Get-InstalledState {
    $left = Get-Leftovers $env:LOCALAPPDATA (Get-UserPath)
    $list = Invoke-Winget @('list', '--id', $PackageId, '-e', '--disable-interactivity', '--accept-source-agreements')
    if ($list.Code -eq 0 -and $list.Out -match [regex]::Escape($PackageId)) { $left += "winget list shows $PackageId" }
    return , $left
}

function Invoke-Uninstall([string]$Dir) {
    $r = Invoke-Winget @('uninstall', '--manifest', $Dir, '--purge', '--disable-interactivity', '--accept-source-agreements')
    Assert "winget uninstall --manifest --purge (exit $(Format-Code $r.Code))" ($r.Code -eq 0)
    if ($r.Code -ne 0) { Show-Failure $r }
}

function Test-Preconditions {
    if (-not $IsWin) { throw 'test-winget: the install cases need Windows; run -SelfTest elsewhere' }
    if (Test-Elevated) { throw 'test-winget: run from a terminal that is not elevated: winget cannot uninstall a user-scope package from an elevated one. The Defender steps ask for elevation themselves (one UAC prompt).' }
    if (-not (Get-Command winget -ErrorAction SilentlyContinue)) { throw 'test-winget: winget is not on PATH; install App Installer from the Microsoft Store and open a new terminal' }
    if ([string]::IsNullOrEmpty($ManifestDir)) { throw 'test-winget: -ManifestDir is required (for example -ManifestDir release\winget\1.16.0)' }
    if (-not (Test-Path -LiteralPath $ManifestDir -PathType Container)) { throw "test-winget: -ManifestDir $ManifestDir is not a directory" }
    foreach ($a in $Architecture) { $null = Get-GoArch $a }
    $mp = Find-MpCmdRun
    if ($null -eq $mp) { throw 'test-winget: MpCmdRun.exe not found under ProgramData\Microsoft\Windows Defender\Platform or Program Files\Windows Defender; is Microsoft Defender installed and on?' }
    $setting = Get-LocalManifestSetting (Invoke-Winget @('settings', 'export')).Out
    if ($setting -eq $false) { throw 'test-winget: winget local manifests are disabled; run once from an elevated terminal: winget settings --enable LocalManifestFiles' }
    if ($null -eq $setting) { Write-Host 'warning: could not read LocalManifestFiles from `winget settings export`; if install fails, run from an elevated terminal: winget settings --enable LocalManifestFiles' }
    $state = Get-InstalledState
    if ($state.Count -gt 0) { throw "test-winget: $PackageId is already installed ($($state -join '; ')). This harness only removes installs it made: uninstall it with 'winget uninstall --id $PackageId -e', or if a failed run left it, rerun with -Cleanup -ManifestDir $ManifestDir; then rerun." }
    return $mp
}

function Get-SumsLines([string]$Version) {
    if ($SumsFile) { return [IO.File]::ReadAllLines((Resolve-Path -LiteralPath $SumsFile).Path) }
    $url = "https://github.com/ericfitz/agentbus/releases/download/v$Version/SHA256SUMS"
    $pp = $ProgressPreference; $ProgressPreference = 'SilentlyContinue'
    try { $text = (Invoke-WebRequest -UseBasicParsing -Uri $url).Content } finally { $ProgressPreference = $pp }
    if ($text -is [byte[]]) { $text = [Text.Encoding]::UTF8.GetString($text) }
    return @("$text" -split "`r?`n")
}

function Invoke-ArchCase([string]$Arch, [string]$Dir, [string]$Version, [string]$ScanDir, [hashtable]$ScanNames) {
    Write-Host "==> $Arch"
    $before = @(Get-PackageDirs $env:LOCALAPPDATA)
    $installed = $false
    try {
        # --scope user: the checks below look under LocalAppData.
        $r = Invoke-Winget @('install', '--manifest', $Dir, '--architecture', $Arch, '--scope', 'user', '--accept-package-agreements', '--accept-source-agreements', '--disable-interactivity')
        Assert "$Arch winget install --manifest (exit $(Format-Code $r.Code))" ($r.Code -eq 0)
        if ($r.Code -ne 0) {
            Show-Failure $r
            # winget scans a local manifest's archive with Defender before
            # extracting it, so a detection can surface here.
            if (Test-ArchiveScanFailure $r.Out) { $script:Threat = $true }
            return
        }
        $installed = $true
        $new = @(Get-PackageDirs $env:LOCALAPPDATA | Where-Object { $before -notcontains $_ })
        Assert "$Arch one new package folder ($($new -join ', '))" ($new.Count -eq 1)
        if ($new.Count -ne 1) { return }
        $exe = Join-Path $new[0] 'agentbus.exe'
        Assert "$Arch agentbus.exe in the package folder" (Test-Path -LiteralPath $exe)
        if (-not (Test-Path -LiteralPath $exe)) { return }
        $m = Get-PeMachine $exe
        Assert "$Arch PE machine is $Arch (got $m)" ($m -eq $Arch)
        $bare = Invoke-Exe $exe
        Assert "$Arch bare agentbus.exe exits 0 (exit $(Format-Code $bare.Code))" ($bare.Code -eq 0)
        if ($bare.Code -ne 0) { Show-Failure $bare }
        $links = Get-LinksExe $env:LOCALAPPDATA
        if (Test-Path -LiteralPath $links) {
            $la = Invoke-Exe $links
            Assert "$Arch bare Links\agentbus.exe alias exits 0 (exit $(Format-Code $la.Code))" ($la.Code -eq 0)
        } else {
            Assert "$Arch no Links alias, so winget put the package folder on the user PATH" ((Get-UserPath) -split ';' -contains $new[0])
        }
        $v = Invoke-Exe $exe @('version')
        Assert "$Arch agentbus version prints $Version (got '$($v.Out.Trim())')" (($v.Code -eq 0) -and ($v.Out.Trim() -eq $Version))
        $copy = Join-Path $ScanDir "agentbus-$Arch.exe"
        Copy-Item -LiteralPath $exe -Destination $copy
        $ScanNames[$copy] = $Arch
    } finally {
        if ($installed) { Invoke-Uninstall $Dir }
        $left = Get-Leftovers $env:LOCALAPPDATA (Get-UserPath)
        Assert "$Arch nothing left after uninstall ($($left -join '; '))" ($left.Count -eq 0)
        $script:Leftover = ($left.Count -gt 0)
    }
}

function Invoke-Run {
    $mp = Test-Preconditions
    $dir = (Resolve-Path -LiteralPath $ManifestDir).Path
    $ver = (Read-Yaml (Join-Path $dir "$PackageId.yaml")).Top['PackageVersion']
    $check = Test-ManifestSet $dir (Get-SumsLines $ver)
    foreach ($p in $check.Problems) { Assert "manifest: $p" $false }
    Assert "manifests match v$ver's SHA256SUMS" ($check.Problems.Count -eq 0)
    if ($check.Problems.Count -gt 0) { return }
    $r = Invoke-Winget @('validate', '--manifest', $dir, '--disable-interactivity')
    Assert "winget validate --manifest (exit $(Format-Code $r.Code))" ($r.Code -eq 0)
    if ($r.Code -ne 0) { Show-Failure $r; return }
    $scanDir = Join-Path ([IO.Path]::GetTempPath()) ('agentbus-wscan-' + [guid]::NewGuid().ToString('N'))
    $null = New-Item -ItemType Directory -Path $scanDir
    $names = @{}
    try {
        foreach ($a in $Architecture) {
            Invoke-ArchCase $a $dir $check.Version $scanDir $names
            # A later install would land on the leftover one and test nothing.
            if ($script:Leftover) { Write-Host "stopping: $a left $PackageId installed; fix that, then rerun with -Cleanup -ManifestDir $ManifestDir"; break }
        }
        if ($names.Count -gt 0) { Write-Host '==> Defender (elevated; accept the UAC prompt)'; Invoke-DefenderElevated $scanDir $mp $names }
    } finally { Remove-Item -LiteralPath $scanDir -Recurse -Force -ErrorAction SilentlyContinue }
}

function Invoke-CleanupRun {
    if (-not $IsWin) { throw 'test-winget: -Cleanup needs Windows' }
    if (Test-Elevated) { throw 'test-winget: run -Cleanup from a terminal that is not elevated: winget cannot uninstall a user-scope package from an elevated one' }
    if ([string]::IsNullOrEmpty($ManifestDir)) { throw 'test-winget: -Cleanup needs -ManifestDir, the manifests the failed run installed from' }
    $state = Get-InstalledState
    if ($state.Count -eq 0) { Write-Host "nothing to clean up: $PackageId is not installed"; return }
    $r = Invoke-Winget @('uninstall', '--manifest', (Resolve-Path -LiteralPath $ManifestDir).Path, '--purge', '--disable-interactivity', '--accept-source-agreements')
    if ($r.Code -eq $NoPackageFound) {
        # winget has no record, so what is left is debris: the package's own
        # folders and PATH entries naming folders that are gone.
        Write-Host "winget has no install record for $PackageId; removing its leftovers"
        foreach ($d in (Get-PackageDirs $env:LOCALAPPDATA)) { Write-Host "    removing $d"; Remove-Item -LiteralPath $d -Recurse -Force }
        $stale = Get-StalePathEntries (Get-UserPath) { param($x) Test-Path -LiteralPath $x }
        foreach ($e in $stale) { Write-Host "    removing user PATH entry $e" }
        if ($stale.Count -gt 0) { Remove-UserPathEntries $stale }
    } else {
        Assert "winget uninstall --manifest --purge (exit $(Format-Code $r.Code))" ($r.Code -eq 0)
        if ($r.Code -ne 0) { Show-Failure $r }
    }
    $left = Get-Leftovers $env:LOCALAPPDATA (Get-UserPath)
    Assert "nothing left after uninstall ($($left -join '; '))" ($left.Count -eq 0)
}

# New-Stub builds release/testdata/stub for GOOS/GOARCH, reporting $Version.
function New-Stub([string]$Version, [string]$Out, [string]$GoOs, [string]$GoArch) {
    $saved = @{ CGO_ENABLED = $env:CGO_ENABLED; GOOS = $env:GOOS; GOARCH = $env:GOARCH }
    $env:CGO_ENABLED = '0'; $env:GOOS = $GoOs; $env:GOARCH = $GoArch
    Push-Location (Join-Path (Join-Path $Here 'testdata') 'stub')
    try {
        & go build -trimpath -ldflags "-s -w -X 'main.version=$Version'" -o $Out .
        if ($LASTEXITCODE -ne 0) { throw 'stub build failed' }
    } finally {
        Pop-Location
        foreach ($k in $saved.Keys) { [Environment]::SetEnvironmentVariable($k, $saved[$k], 'Process') }
    }
}

# Copy-Manifests copies a manifest directory and applies one edit (a
# scriptblock taking the copy's path), for the negative cases.
function Copy-Manifests([string]$From, [string]$To, [scriptblock]$Edit) {
    $null = New-Item -ItemType Directory -Path $To
    Get-ChildItem -LiteralPath $From -File | ForEach-Object { Copy-Item -LiteralPath $_.FullName -Destination $To }
    & $Edit $To
    return $To
}

function Edit-File([string]$Path, [string]$Old, [string]$New) {
    $t = [IO.File]::ReadAllText($Path)
    if (-not $t.Contains($Old)) { throw "self-test fixture: '$Old' not in $Path" }
    [IO.File]::WriteAllText($Path, $t.Replace($Old, $New))
}

function Test-HasProblem($Check, [string]$Pattern) { return (@($Check.Problems | Where-Object { $_ -match $Pattern }).Count -gt 0) }

function Invoke-SelfTest {
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) { throw 'test-winget: -SelfTest needs Go on PATH (winget install --id GoLang.Go -e)' }
    $work = Join-Path ([IO.Path]::GetTempPath()) ('agentbus-wtest-' + [guid]::NewGuid().ToString('N'))
    $null = New-Item -ItemType Directory -Path $work
    try {
        $good = Join-Path $RepoRoot 'release/winget/1.16.0'
        $sums = [IO.File]::ReadAllLines((Join-Path $Here 'testdata/winget/SHA256SUMS-v1.16.0'))
        $c = Test-ManifestSet $good $sums
        foreach ($p in $c.Problems) { Write-Host "    $p" }
        Assert 'self: release/winget/1.16.0 matches the recorded v1.16.0 SHA256SUMS' (($c.Problems.Count -eq 0) -and ($c.Version -eq '1.16.0'))
        $inst = Join-Path $good "$PackageId.installer.yaml"
        $y = Read-Yaml $inst
        Assert 'self: Read-Yaml reads two installers with url and hash' (($y.Installers.Count -eq 2) -and ($y.Installers[1]['Architecture'] -eq 'arm64') -and ($y.Installers[1]['InstallerSha256'].Length -eq 64) -and ($y.Top['ManifestType'] -eq 'installer'))

        $n = 0
        $cases = @(
            @{ Name = 'a wrong hash'; Want = 'x64 InstallerSha256 .* does not match'; Edit = { param($d) Edit-File (Join-Path $d "$PackageId.installer.yaml") 'InstallerSha256: DE7B' 'InstallerSha256: DE7C' } },
            @{ Name = 'a version mismatch'; Want = "PackageVersion '1.16.0' differs from .*'1.16.1'"; Edit = { param($d) Edit-File (Join-Path $d "$PackageId.yaml") 'PackageVersion: 1.16.0' 'PackageVersion: 1.16.1' } },
            @{ Name = 'an unsupported architecture'; Want = "unsupported Architecture 'x86'"; Edit = { param($d) Edit-File (Join-Path $d "$PackageId.installer.yaml") 'Architecture: arm64' 'Architecture: x86' } },
            @{ Name = 'a missing arm64 installer'; Want = 'no arm64 installer'; Edit = { param($d) Edit-File (Join-Path $d "$PackageId.installer.yaml") 'Architecture: arm64' 'Architecture: x86' } },
            @{ Name = 'a duplicate architecture'; Want = 'lists x64 more than once'; Edit = { param($d) Edit-File (Join-Path $d "$PackageId.installer.yaml") 'Architecture: arm64' 'Architecture: x64' } },
            @{ Name = 'a URL for another tag'; Want = 'arm64 InstallerUrl'; Edit = { param($d) Edit-File (Join-Path $d "$PackageId.installer.yaml") 'download/v1.16.0/agentbus-v1.16.0-windows-arm64' 'download/v1.15.0/agentbus-v1.16.0-windows-arm64' } },
            @{ Name = 'another package identifier'; Want = "locale.en-US.yaml: PackageIdentifier 'ericfitz.other'"; Edit = { param($d) Edit-File (Join-Path $d "$PackageId.locale.en-US.yaml") "PackageIdentifier: $PackageId" 'PackageIdentifier: ericfitz.other' } },
            @{ Name = 'a stale release notes URL'; Want = 'ReleaseNotesUrl'; Edit = { param($d) Edit-File (Join-Path $d "$PackageId.locale.en-US.yaml") 'tag/v1.16.0' 'tag/v1.14.2' } },
            @{ Name = 'an extra file'; Want = 'unexpected file README.md'; Edit = { param($d) [IO.File]::WriteAllText((Join-Path $d 'README.md'), 'x') } },
            @{ Name = 'a missing locale manifest'; Want = 'missing manifest .*locale'; Edit = { param($d) Remove-Item -LiteralPath (Join-Path $d "$PackageId.locale.en-US.yaml") } }
        )
        foreach ($k in $cases) {
            $n++
            $d = Copy-Manifests $good (Join-Path $work "case$n") $k.Edit
            $r = Test-ManifestSet $d $sums
            Assert "self: manifest check rejects $($k.Name)" (Test-HasProblem $r $k.Want)
        }
        $r = Test-ManifestSet $good @($sums | Where-Object { $_ -notmatch 'windows-arm64' })
        Assert 'self: manifest check rejects SHA256SUMS without the arm64 zip' (Test-HasProblem $r 'SHA256SUMS has no line for agentbus-v1.16.0-windows-arm64.zip')
        $r = Test-ManifestSet $good @()
        Assert 'self: manifest check rejects an empty SHA256SUMS' (Test-HasProblem $r 'SHA256SUMS has no line for agentbus-v1.16.0-windows-amd64.zip')

        foreach ($a in @(@('x64', 'amd64'), @('arm64', 'arm64'))) { Assert "self: Get-GoArch $($a[0]) is $($a[1])" ((Get-GoArch $a[0]) -eq $a[1]) }
        $threw = $false; try { $null = Get-GoArch 'x86' } catch { $threw = $_.Exception.Message.Contains("unsupported architecture 'x86'") }
        Assert 'self: Get-GoArch rejects x86' $threw

        # settings-export-enabled.json is recorded from winget in the Windows
        # 11 ARM VM (2026-10-21, user name replaced); the disabled one is
        # derived from it. The format is undocumented, so a real run that
        # cannot read it only warns.
        $fx = Join-Path $Here 'testdata/winget'
        Assert 'self: archive scan failure is recognized' (Test-ArchiveScanFailure 'Archive malware scan failed.')
        Assert 'self: a hash mismatch is not an archive scan failure' (-not (Test-ArchiveScanFailure 'Installer hash does not match; this cannot be overridden when running as admin'))
        Assert 'self: LocalManifestFiles enabled reads true' ((Get-LocalManifestSetting ([IO.File]::ReadAllText((Join-Path $fx 'settings-export-enabled.json')))) -eq $true)
        Assert 'self: LocalManifestFiles disabled reads false' ((Get-LocalManifestSetting ([IO.File]::ReadAllText((Join-Path $fx 'settings-export-disabled.json')))) -eq $false)
        Assert 'self: LocalManifestFiles after a banner line still reads' ((Get-LocalManifestSetting ("Windows Package Manager v1.12`n" + [IO.File]::ReadAllText((Join-Path $fx 'settings-export-enabled.json')))) -eq $true)
        Assert 'self: LocalManifestFiles absent reads null' ($null -eq (Get-LocalManifestSetting '{"adminSettings":{}}'))
        Assert 'self: non-JSON settings output reads null' ($null -eq (Get-LocalManifestSetting 'Unrecognized command: export'))

        $found = "Scan starting...`nScan finished.`nScanning C:\x\agentbus.exe found 1 threats.`nThreat                  : Trojan:Win32/Example"
        $clean = "Scan starting...`nScan finished.`nScanning C:\x\agentbus.exe found no threats."
        foreach ($v in @(@(0, $clean, 'clean'), @(2, $found, 'threat'), @(2, 'CmdTool: Failed with hr = 0x80508023', 'error'), @(2, $clean, 'error'), @(1, $found, 'error'), @(-2147024891, '', 'error'))) {
            Assert "self: Defender exit $($v[0]) with '$(($v[1] -split "`n")[-1])' is $($v[2])" ((Get-DefenderVerdict $v[0] $v[1]) -eq $v[2])
        }

        $gone = 'C:\Users\u\AppData\Local\Microsoft\WinGet\Packages\ericfitz.agentbus__DefaultSource'
        $live = 'C:\Users\u\AppData\Local\Microsoft\WinGet\Packages\ericfitz.agentbus__Other'
        $st = Get-StalePathEntries "C:\bin;$gone;$live;C:\Users\u\AppData\Local\Microsoft\WinGet\Links;C:\x\ericfitz.agentbus__DefaultSource" { param($x) $x -eq $live }
        Assert 'self: stale PATH entries are only missing package folders' (($st.Count -eq 1) -and ($st[0] -eq $gone))
        Assert 'self: no stale PATH entries in an empty PATH' ((Get-StalePathEntries '' { $false }).Count -eq 0)

        $pw = (Get-Process -Id $PID).Path
        $mpDir = Join-Path $work 'mp'
        $null = New-Item -ItemType Directory -Path $mpDir
        $fakeMp = Join-Path $mpDir 'FakeMpCmdRun.ps1'
        [IO.File]::WriteAllText($fakeMp, @"
if (`$args -contains '-SignatureUpdate') { 'Signature update finished.'; exit 0 }
`$f = `$args[[array]::IndexOf(`$args, '-File') + 1]
if (`$f -like '*threat*') { "Scanning `$f found 1 threats."; exit 2 }
"Scanning `$f found no threats."; exit 0
"@)
        $cleanF = Join-Path $mpDir 'agentbus-x64.exe'; $badF = Join-Path $mpDir 'agentbus-threat.exe'; $lostF = Join-Path $mpDir 'agentbus-lost.exe'
        $job = New-DefenderJob $mpDir $fakeMp @($cleanF, $badF)
        $r = Invoke-Exe $pw @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $job.Script, $job.Input)
        $res = Read-DefenderResults $job.Out
        Assert 'self: Defender child runs and writes one update and two scan records' (($r.Code -eq 0) -and ($null -ne $res) -and (@($res).Count -eq 3) -and ($res[0].Kind -eq 'update') -and ($res[2].File -eq $badF))
        $fs = Get-DefenderFindings $res @{ $cleanF = 'x64'; $badF = 'arm64'; $lostF = 'lost' }
        $byName = @{}; foreach ($f in $fs) { $byName[$f.Name] = $f }
        Assert 'self: Defender findings pass the update and the clean scan' ($byName.ContainsKey('Defender -SignatureUpdate (exit 0)') -and $byName['Defender -SignatureUpdate (exit 0)'].Ok -and $byName.ContainsKey('x64 Defender scan: clean (exit 0)') -and $byName['x64 Defender scan: clean (exit 0)'].Ok)
        Assert 'self: Defender findings fail the threat as a threat' ($byName.ContainsKey('arm64 Defender scan: threat (exit 2)') -and -not $byName['arm64 Defender scan: threat (exit 2)'].Ok)
        Assert 'self: Defender findings fail a file that was never scanned' ($byName.ContainsKey('lost Defender scan ran') -and -not $byName['lost Defender scan ran'].Ok)
        Assert 'self: no Defender results file reads null' ($null -eq (Read-DefenderResults (Join-Path $mpDir 'absent.json')))
        Assert 'self: Test-Elevated is false outside Windows or unelevated CI' ((-not $IsWin) -or ((Test-Elevated) -is [bool]))

        $lad = Join-Path $work 'LocalAppData'
        $pkgs = Join-Path $lad 'Microsoft\WinGet\Packages'
        $null = New-Item -ItemType Directory -Path (Join-Path $pkgs 'Other.Package_Microsoft.Winget.Source_8wekyb3d8bbwe')
        $null = New-Item -ItemType Directory -Path (Join-Path $pkgs "${PackageId}x_Source")
        $null = New-Item -ItemType Directory -Path (Join-Path $lad 'Microsoft\WinGet\Links')
        Assert 'self: no leftovers in a clean layout' ((Get-Leftovers $lad 'C:\Users\u\AppData\Local\Microsoft\WinGet\Links;C:\bin').Count -eq 0)
        $before = @(Get-PackageDirs $lad)
        $pd = Join-Path $pkgs "${PackageId}__DefaultSource"
        $null = New-Item -ItemType Directory -Path $pd
        [IO.File]::WriteAllText((Get-LinksExe $lad), 'x')
        $after = @(Get-PackageDirs $lad)
        Assert 'self: Get-PackageDirs finds only the package folder' (($before.Count -eq 0) -and ($after.Count -eq 1) -and ($after[0] -eq $pd))
        $left = Get-Leftovers $lad "C:\bin;$pd"
        Assert 'self: leftovers name the alias, the folder and the PATH entry' (($left.Count -eq 3) -and ($left -contains (Get-LinksExe $lad)) -and ($left -contains $pd) -and ($left -contains "user PATH entry $pd"))

        $r = Invoke-Exe $pw @('-NoProfile', '-Command', 'exit 2')
        Assert 'self: Invoke-Exe reports exit 2' ($r.Code -eq 2)
        $r = Invoke-Exe $pw @('-NoProfile', '-Command', '[Console]::Error.WriteLine(''to-stderr''); exit 3')
        Assert 'self: Invoke-Exe captures stderr without throwing and reports exit 3' (($r.Code -eq 3) -and $r.Out.Contains('to-stderr'))
        Assert 'self: Format-Code shows HRESULTs in hex' ((Format-Code -1978335212) -eq '-1978335212 (0x8A150014)')

        foreach ($a in @(@('x64', 'amd64'), @('arm64', 'arm64'))) {
            $o = Join-Path $work "stub-$($a[1]).exe"
            New-Stub '1.16.0' $o 'windows' $a[1]
            Assert "self: Get-PeMachine reads a windows/$($a[1]) build as $($a[0])" ((Get-PeMachine $o) -eq $a[0])
        }
        Assert 'self: Get-PeMachine rejects a non-PE file' ((Get-PeMachine (Join-Path $good "$PackageId.yaml")) -eq 'not-pe')
        $native = Join-Path $work $(if ($IsWin) { 'stub.exe' } else { 'stub' })
        New-Stub '1.16.0' $native '' ''
        $r = Invoke-Exe $native
        Assert 'self: a bare run of the stub exits 0' ($r.Code -eq 0)
        $r = Invoke-Exe $native @('version')
        Assert 'self: stub version output trims to 1.16.0' ($r.Out.Trim() -eq '1.16.0')
    } finally {
        Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
        Assert 'self: work folder removed' (-not (Test-Path -LiteralPath $work))
    }
}

function Invoke-Main {
    $mutex = $null; $held = $false
    try {
        if ($SelfTest) { Invoke-SelfTest } else {
            $mutex = New-Object System.Threading.Mutex($false, 'Local\agentbus-test-winget')
            # A run killed while holding the mutex abandons it; the next
            # WaitOne then throws (wrapped by PowerShell) but owns it.
            try { $held = $mutex.WaitOne(0) } catch {
                $e = $_.Exception; while ($null -ne $e -and -not ($e -is [System.Threading.AbandonedMutexException])) { $e = $e.InnerException }
                if ($null -eq $e) { throw }
                $held = $true
            }
            if (-not $held) { throw 'test-winget: another test-winget.ps1 run is in progress; wait for it to finish' }
            if ($Cleanup) { Invoke-CleanupRun } else { Invoke-Run }
        }
    } catch {
        $script:Fail++
        Write-Host "FAIL harness error: $($_.Exception.Message)"
        Write-Host $_.ScriptStackTrace
    } finally {
        if ($held) { $mutex.ReleaseMutex() }
        if ($null -ne $mutex) { $mutex.Dispose() }
    }
    if ($script:Threat) { Write-Host 'DEFENDER FLAGGED agentbus.exe: submit the file at https://www.microsoft.com/wdsi/filesubmission before updating the winget pull request.' }
    Write-Host "test-winget.ps1 ($($PSVersionTable.PSVersion)): $script:Pass passed, $script:Fail failed"
    if ($script:Fail -ne 0) { exit 1 }
}

if ($MyInvocation.InvocationName -ne '.') { Invoke-Main }
