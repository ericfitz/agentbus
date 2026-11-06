# Runs `go test -json` from the repository root and prints a summary instead
# of the per-test output: on a clean run, one line of counts; otherwise the
# counts, the failed tests with their output (from the failing run only, so
# -count=N does not mix in a passing run's log), packages that failed without
# a failing test (build failures, timeouts, crashes) with their output, and
# anything go printed that was not a test event. The full JSON log is kept.
# Made for chasing flaky tests on Windows under the load of the whole suite.
#   powershell -ExecutionPolicy Bypass -File release\go-test-summary.ps1
#   ... -Count 3                 runs of each test (default 3)
#   ... -Run '<regexp>'          only matching tests (go test -run)
#   ... -Packages ./internal/tui packages (default ./...)
#   ... -Log <path>              full JSON log (default %TEMP%\agentbus-go-test.jsonl)
#   release\go-test-summary.ps1 -SelfTest  checks the parser against the
#                                          recorded output in release\testdata\gotest
#                                          (no Go needed; runs outside Windows)
# Exit status: 0 only when every package passed, at least one test ran and
# every event was recognized; 1 otherwise. The log is overwritten on each
# run; two runs at once with the same -Log clobber each other's log, so give
# each its own -Log.
# Dot-sourcing (. release\go-test-summary.ps1) loads the functions without running.
param(
    [switch]$SelfTest,
    [int]$Count = 3,
    [string]$Run,
    [string[]]$Packages = @('./...'),
    [string]$Log
)
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
$Here = Split-Path -Parent $MyInvocation.MyCommand.Path
$RepoRoot = (Resolve-Path (Join-Path $Here '..')).Path

# Every Action `go test -json` (test2json and the go command) emits. Anything
# else is reported as unrecognized and fails the run, so a format change in a
# future Go cannot pass silently.
$KnownActions = @('start', 'run', 'pause', 'cont', 'pass', 'fail', 'skip', 'output', 'bench', 'build-output', 'build-fail')
# Framing lines left out of failure output; FAIL lines and the test's own
# log stay in.
$NoiseLine = '^(=== (RUN|PAUSE|CONT|NAME)|--- (PASS|SKIP))'

function New-List { return , (New-Object System.Collections.Generic.List[string]) }

# Get-Field reads a property that may be absent (Set-StrictMode throws on a
# missing property of a PSCustomObject).
function Get-Field($Obj, [string]$Name) {
    $p = $Obj.PSObject.Properties[$Name]
    if ($null -eq $p) { return $null }
    return $p.Value
}

# Get-GoTestSummary turns the lines of `go test -json` output (JSON events
# mixed with whatever go wrote to stderr) into counts and failure details.
function Get-GoTestSummary([string[]]$Lines) {
    $runs = 0; $passed = 0; $failed = 0; $skipped = 0
    $failedTests = New-List
    $failedPkgs = New-List
    $pkgHasFailedTest = @{}
    $failureOutput = New-List
    $buildOutput = New-List
    $other = New-List
    $unknown = New-List
    $buf = @{}     # "<pkg>`t<test>" -> output of that test's current run
    $pkgOut = @{}  # package -> package-level output
    foreach ($raw in @($Lines)) {
        if ($null -eq $raw) { continue }
        $line = $raw.TrimEnd()
        if ($line -eq '') { continue }
        $e = $null
        if ($line.StartsWith('{')) { try { $e = $line | ConvertFrom-Json } catch { $e = $null } }
        if ($null -eq $e) { $other.Add($line); continue }
        $action = [string](Get-Field $e 'Action')
        if ($KnownActions -notcontains $action) { $unknown.Add($line); continue }
        $pkg = [string](Get-Field $e 'Package')
        $test = [string](Get-Field $e 'Test')
        $key = "$pkg`t$test"
        if ($action -eq 'build-output') {
            $buildOutput.Add(([string](Get-Field $e 'Output')).TrimEnd())
        } elseif ($action -eq 'run' -and $test) {
            $buf[$key] = New-List
        } elseif ($action -eq 'output') {
            $o = ([string](Get-Field $e 'Output')).TrimEnd()
            if ($test) {
                if (-not $buf.ContainsKey($key)) { $buf[$key] = New-List }
                $buf[$key].Add($o)
            } else {
                if (-not $pkgOut.ContainsKey($pkg)) { $pkgOut[$pkg] = New-List }
                $pkgOut[$pkg].Add($o)
            }
        } elseif ($action -in 'pass', 'fail', 'skip') {
            if (-not $test) {
                if ($action -eq 'fail' -and -not $failedPkgs.Contains($pkg)) { $failedPkgs.Add($pkg) }
                continue
            }
            $runs++
            switch ($action) {
                'pass' { $passed++ }
                'skip' { $skipped++ }
                'fail' {
                    $failed++
                    $name = "$pkg/$test"
                    if (-not $failedTests.Contains($name)) { $failedTests.Add($name) }
                    $pkgHasFailedTest[$pkg] = $true
                    $failureOutput.Add("--- $name")
                    if ($buf.ContainsKey($key)) {
                        foreach ($o in $buf[$key]) { if ($o -notmatch $NoiseLine) { $failureOutput.Add($o) } }
                    }
                }
            }
            $buf.Remove($key)
        }
    }
    # A test that started but never reported a result (a timeout or crash
    # killed the test binary) belongs with its package's failure.
    $noResult = New-List
    $noResultOutput = New-List
    foreach ($k in @($buf.Keys | Sort-Object)) {
        $pkg, $test = $k -split "`t", 2
        if (-not $failedPkgs.Contains($pkg)) { continue }
        $noResult.Add("$pkg/$test")
        $noResultOutput.Add("--- $pkg/$test (no result)")
        foreach ($o in $buf[$k]) { if ($o -notmatch $NoiseLine) { $noResultOutput.Add($o) } }
    }
    $pkgOnly = New-List
    $pkgOnlyOutput = New-List
    foreach ($p in $failedPkgs) {
        if ($pkgHasFailedTest.ContainsKey($p)) { continue }
        $pkgOnly.Add($p)
        $pkgOnlyOutput.Add("--- $p")
        if ($pkgOut.ContainsKey($p)) { foreach ($o in $pkgOut[$p]) { $pkgOnlyOutput.Add($o) } }
    }
    $bad = ($failed -gt 0) -or ($failedPkgs.Count -gt 0) -or ($unknown.Count -gt 0) -or ($runs -eq 0)
    return [pscustomobject]@{
        Runs = $runs; Passed = $passed; Failed = $failed; Skipped = $skipped
        FailedTests = $failedTests; FailedPackages = $failedPkgs
        FailureOutput = $failureOutput
        NoResult = $noResult; NoResultOutput = $noResultOutput
        PackagesWithoutFailedTest = $pkgOnly; PackageOutput = $pkgOnlyOutput
        BuildOutput = $buildOutput; Other = $other; Unrecognized = $unknown
        ExitCode = $(if ($bad) { 1 } else { 0 })
    }
}

# Format-GoTestSummary renders a summary: one line when ExitCode is 0.
function Format-GoTestSummary($S) {
    $out = New-List
    $out.Add(('test results: {0}, passed {1}, failed {2}, skipped {3}; packages failed: {4}' -f $S.Runs, $S.Passed, $S.Failed, $S.Skipped, $S.FailedPackages.Count))
    if ($S.ExitCode -eq 0) { return , $out }
    if ($S.Runs -eq 0) { $out.Add('no test results: no test ran (check -Run and -Packages)') }
    if ($S.FailedTests.Count -gt 0) {
        $out.Add('failed tests: ' + ($S.FailedTests -join ', '))
        $out.AddRange($S.FailureOutput)
    }
    if ($S.PackagesWithoutFailedTest.Count -gt 0) {
        $out.Add('packages failed without a failing test: ' + ($S.PackagesWithoutFailedTest -join ', '))
        $out.AddRange($S.PackageOutput)
    }
    if ($S.NoResult.Count -gt 0) {
        $out.Add('tests with no result in a failed package: ' + ($S.NoResult -join ', '))
        $out.AddRange($S.NoResultOutput)
    }
    if ($S.BuildOutput.Count -gt 0) { $out.Add('build output:'); $out.AddRange($S.BuildOutput) }
    if ($S.Unrecognized.Count -gt 0) { $out.Add("unrecognized go test -json events ($($S.Unrecognized.Count)):"); $out.AddRange($S.Unrecognized) }
    if ($S.Other.Count -gt 0) { $out.Add('other output from go:'); $out.AddRange($S.Other) }
    return , $out
}

function Invoke-GoTests {
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) { throw 'go-test-summary: needs Go on PATH (winget install --id GoLang.Go -e)' }
    if ($Count -lt 1) { throw "go-test-summary: -Count must be 1 or more, got $Count" }
    $logPath = $Log
    if (-not $logPath) { $logPath = Join-Path ([IO.Path]::GetTempPath()) 'agentbus-go-test.jsonl' }
    $goArgs = @('test', "-count=$Count", '-json')
    if ($Run) { $goArgs += "-run=$Run" }
    $goArgs += $Packages
    Write-Host "go $($goArgs -join ' ')   (full log: $logPath)"
    Push-Location $RepoRoot
    $prev = $ErrorActionPreference
    try {
        # Windows PowerShell 5.1 turns a native command's stderr into error
        # records; with 'Stop' the first one would abort the run.
        $ErrorActionPreference = 'Continue'
        $lines = @(& go @goArgs 2>&1 | ForEach-Object { "$_" })
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $prev
        Pop-Location
    }
    # WriteAllLines writes UTF-8 without a BOM, so each line stays one JSON event.
    [IO.File]::WriteAllLines($logPath, [string[]]$lines)
    $s = Get-GoTestSummary $lines
    foreach ($l in (Format-GoTestSummary $s)) { Write-Host $l }
    if ($code -ne 0 -and $s.ExitCode -eq 0) {
        Write-Host "go test exited $code although every test passed; see $logPath"
        return 1
    }
    return $s.ExitCode
}

function Invoke-SelfTest {
    $script:SelfFail = 0; $script:SelfPass = 0
    function Check([string]$Name, [bool]$Ok) {
        if ($Ok) { $script:SelfPass++ } else { $script:SelfFail++; Write-Host "FAIL $Name" }
    }
    $td = Join-Path (Join-Path $Here 'testdata') 'gotest'
    $fixture = { param($n) [IO.File]::ReadAllLines((Join-Path $td $n)) }

    $s = Get-GoTestSummary (& $fixture 'pass.jsonl')
    $t = (Format-GoTestSummary $s) -join "`n"
    Check 'pass: counts' (($s.Runs -eq 2) -and ($s.Passed -eq 1) -and ($s.Skipped -eq 1) -and ($s.Failed -eq 0) -and ($s.ExitCode -eq 0))
    Check 'pass: prints only the summary line' ($t -eq 'test results: 2, passed 1, failed 0, skipped 1; packages failed: 0')

    $lines = @(& $fixture 'pass.jsonl') + 'go: downloading example.com/x v1.0.0'
    $s = Get-GoTestSummary $lines
    Check 'pass with stderr noise: still one line, exit 0' (($s.ExitCode -eq 0) -and ((Format-GoTestSummary $s).Count -eq 1))

    $s = Get-GoTestSummary (& $fixture 'mixed.jsonl')
    $t = (Format-GoTestSummary $s) -join "`n"
    Check 'mixed: counts' (($s.Runs -eq 6) -and ($s.Passed -eq 2) -and ($s.Failed -eq 3) -and ($s.Skipped -eq 1) -and ($s.ExitCode -eq 1))
    Check 'mixed: failed packages' ((($s.FailedPackages | Sort-Object) -join ',') -eq 'example.com/fx/bad,example.com/fx/boom')
    Check 'mixed: failed tests named' ($s.FailedTests.Contains('example.com/fx/bad/TestBroken/sub') -and $s.FailedTests.Contains('example.com/fx/boom/TestPanics'))
    Check 'mixed: assertion text shown' ($t.Contains('headSel = -1, want 3'))
    Check 'mixed: panic shown' ($t.Contains('panic: kaboom'))
    Check 'mixed: no framing or passing-test lines' (-not ($t.Contains('--- PASS') -or $t.Contains('=== RUN') -or $t.Contains('TestGood')))
    Check 'mixed: no packages-without-failing-test section' ($s.PackagesWithoutFailedTest.Count -eq 0)

    $s = Get-GoTestSummary (& $fixture 'build.jsonl')
    $t = (Format-GoTestSummary $s) -join "`n"
    Check 'build failure: package failed, exit 1' (($s.ExitCode -eq 1) -and ($s.PackagesWithoutFailedTest -join ',') -eq 'example.com/fx/nobuild')
    Check 'build failure: compiler error shown' ($t.Contains('undefined: undefinedThing'))

    $s = Get-GoTestSummary (& $fixture 'flaky.jsonl')
    $t = (Format-GoTestSummary $s) -join "`n"
    Check 'count=2 flaky: one pass, one fail' (($s.Runs -eq 2) -and ($s.Passed -eq 1) -and ($s.Failed -eq 1))
    Check 'count=2 flaky: failing run output shown' ($t.Contains('flaky failure on run 2'))
    Check 'count=2 flaky: passing run output left out' (-not $t.Contains('pass-run-noise'))

    $lines = @(& $fixture 'pass.jsonl') + '{"Action":"weird","Package":"p"}'
    $s = Get-GoTestSummary $lines
    Check 'unknown action: rejected' (($s.Unrecognized.Count -eq 1) -and ($s.ExitCode -eq 1) -and ((Format-GoTestSummary $s) -join "`n").Contains('weird'))

    $lines = @(& $fixture 'mixed.jsonl') + 'go: some warning'
    Check 'failure: stderr noise shown' (((Format-GoTestSummary (Get-GoTestSummary $lines)) -join "`n").Contains('go: some warning'))

    $s = Get-GoTestSummary @()
    Check 'no output: exit 1, says no test ran' (($s.ExitCode -eq 1) -and ((Format-GoTestSummary $s) -join "`n").Contains('no test results'))

    $lines = @(
        '{"Action":"start","Package":"p"}',
        '{"Action":"run","Package":"p","Test":"TestHang"}',
        '{"Action":"output","Package":"p","Test":"TestHang","Output":"panic: test timed out after 10m0s\n"}',
        '{"Action":"output","Package":"p","Output":"FAIL\tp\t600.1s\n"}',
        '{"Action":"fail","Package":"p","Elapsed":600.1}'
    )
    $s = Get-GoTestSummary $lines
    $t = (Format-GoTestSummary $s) -join "`n"
    Check 'timeout: test with no result reported' (($s.ExitCode -eq 1) -and $s.NoResult.Contains('p/TestHang') -and $t.Contains('test timed out'))

    Write-Host "go-test-summary.ps1 self-test ($($PSVersionTable.PSVersion)): $script:SelfPass passed, $script:SelfFail failed"
    if ($script:SelfFail -ne 0) { return 1 }
    return 0
}

if ($MyInvocation.InvocationName -ne '.') {
    if ($SelfTest) { exit (Invoke-SelfTest) }
    exit (Invoke-GoTests)
}
