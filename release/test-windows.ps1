# Runs the Windows checks natively in the user's Windows 11 ARM VM: go vet,
# go test, and the install.ps1 harness under both PowerShell hosts. Prints a
# summary; exits 1 if anything failed. Needs Go (go.dev/dl or
# `winget install --id GoLang.Go -e`) and Git for Windows (`winget install
# --id Git.Git -e`; the harness uses its OpenSSL 3). Run from the repository
# root in Windows PowerShell or pwsh:
#   powershell -ExecutionPolicy Bypass -File release\test-windows.ps1
param()
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Continue'
$missing = @(@('go', 'git') | Where-Object { -not (Get-Command $_ -ErrorAction SilentlyContinue) })
if ($missing.Count -gt 0) {
    Write-Host "test-windows.ps1: not on PATH: $($missing -join ', '). Install Go (winget install --id GoLang.Go -e) and Git for Windows (winget install --id Git.Git -e), open a new terminal, and rerun."
    exit 1
}
$root = (Resolve-Path (Join-Path (Split-Path -Parent $MyInvocation.MyCommand.Path) '..')).Path
$results = @()
function Step([string]$Name, [scriptblock]$Body) {
    Write-Host "==> $Name"
    $global:LASTEXITCODE = 0
    try { & $Body; $ok = ($LASTEXITCODE -eq 0) }
    catch { Write-Host "error: $_"; $ok = $false }
    $script:results += [pscustomobject]@{ Step = $Name; Result = $(if ($ok) { 'PASS' } else { 'FAIL' }) }
}
Push-Location $root
try {
    Step 'go vet ./...' { go vet ./... }
    Step 'go test ./...' { go test ./... }
    Step 'test-install.ps1 -SelfTest (powershell 5.1)' { powershell -NoProfile -ExecutionPolicy Bypass -File release\test-install.ps1 -SelfTest }
    Step 'test-install.ps1 (powershell 5.1)' { powershell -NoProfile -ExecutionPolicy Bypass -File release\test-install.ps1 }
    if (Get-Command pwsh -ErrorAction SilentlyContinue) {
        Step 'test-install.ps1 (pwsh 7)' { pwsh -NoProfile -ExecutionPolicy Bypass -File release\test-install.ps1 }
    } else { $results += [pscustomobject]@{ Step = 'test-install.ps1 (pwsh 7)'; Result = 'SKIP (pwsh not installed)' } }
    $results | Format-Table -AutoSize
    $failed = @($results | Where-Object { $_.Result -eq 'FAIL' }).Count
} finally { Pop-Location }
if ($failed -ne 0) { exit 1 }
