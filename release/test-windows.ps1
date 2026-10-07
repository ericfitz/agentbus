# Runs the Windows checks natively in the user's Windows 11 ARM VM: go vet,
# go test, and the install.ps1 harness under both PowerShell hosts. Prints a
# summary; exits 1 if anything failed. Run from the repository root:
#   pwsh -File release\test-windows.ps1
param()
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Continue'
$root = (Resolve-Path (Join-Path (Split-Path -Parent $MyInvocation.MyCommand.Path) '..')).Path
Set-Location $root
$results = @()
function Step([string]$Name, [scriptblock]$Body) {
    Write-Host "==> $Name"
    & $Body
    $ok = ($LASTEXITCODE -eq 0)
    $script:results += [pscustomobject]@{ Step = $Name; Result = $(if ($ok) { 'PASS' } else { 'FAIL' }) }
}
Step 'go vet ./...' { go vet ./... }
Step 'go test ./...' { go test ./... }
Step 'test-install.ps1 -SelfTest (powershell 5.1)' { powershell -NoProfile -ExecutionPolicy Bypass -File release\test-install.ps1 -SelfTest }
Step 'test-install.ps1 (powershell 5.1)' { powershell -NoProfile -ExecutionPolicy Bypass -File release\test-install.ps1 }
if (Get-Command pwsh -ErrorAction SilentlyContinue) {
    Step 'test-install.ps1 (pwsh 7)' { pwsh -NoProfile -ExecutionPolicy Bypass -File release\test-install.ps1 }
} else { $results += [pscustomobject]@{ Step = 'test-install.ps1 (pwsh 7)'; Result = 'SKIP (pwsh not installed)' } }
$results | Format-Table -AutoSize
if (($results | Where-Object { $_.Result -eq 'FAIL' }).Count -ne 0) { exit 1 }
