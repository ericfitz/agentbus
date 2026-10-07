# Serves a fake GitHub releases tree for release/test-install.ps1 (the
# PowerShell twin of fixture-server.py).
#   fixture-server.ps1 -Root <dir> -Port <n>
# <root>/<variant>/latest -> tag; <root>/<variant>/<tag>/<asset> -> files.
# GET|HEAD /<variant>/releases/latest -> 302; /<variant>/releases/download/<tag>/<asset> -> file.
# Any other method is 405; a path segment that is empty, '.', '..' or holds a
# path or drive character is 400. The "slow" variant waits 3 s before
# answering a download.
param([Parameter(Mandatory)][string]$Root, [Parameter(Mandatory)][int]$Port)
Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
$listener = New-Object System.Net.HttpListener
$listener.Prefixes.Add("http://127.0.0.1:$Port/")
$listener.Start()
while ($listener.IsListening) {
    $ctx = $listener.GetContext()
    $req = $ctx.Request; $res = $ctx.Response
    try {
        $parts = @($req.Url.AbsolutePath.Trim('/') -split '/')
        if ($req.HttpMethod -ne 'GET' -and $req.HttpMethod -ne 'HEAD') { $res.StatusCode = 405 }
        elseif (@($parts | Where-Object { $_ -eq '' -or $_ -eq '.' -or $_ -eq '..' -or $_ -match '[\\:*?"<>|]' }).Count -gt 0) { $res.StatusCode = 400 }
        elseif ($parts.Count -eq 3 -and $parts[1] -eq 'releases' -and $parts[2] -eq 'latest') {
            $latest = Join-Path $Root (Join-Path $parts[0] 'latest')
            if (Test-Path -LiteralPath $latest -PathType Leaf) {
                $tag = (Get-Content -LiteralPath $latest -Raw).Trim()
                $res.StatusCode = 302
                $res.RedirectLocation = "/$($parts[0])/releases/tag/$tag"
            } else { $res.StatusCode = 404 }
        } elseif ($parts.Count -eq 5 -and $parts[1] -eq 'releases' -and $parts[2] -eq 'download') {
            $file = Join-Path $Root (Join-Path $parts[0] (Join-Path $parts[3] $parts[4]))
            if (Test-Path -LiteralPath $file -PathType Leaf) {
                if ($parts[0] -eq 'slow') { Start-Sleep -Seconds 3 }
                $bytes = [IO.File]::ReadAllBytes($file)
                $res.ContentLength64 = $bytes.Length
                if ($req.HttpMethod -eq 'GET') { $res.OutputStream.Write($bytes, 0, $bytes.Length) }
            } else { $res.StatusCode = 404 }
        } else { $res.StatusCode = 404 }
    } catch {
        # A client that hung up must not stop the server.
        [Console]::Error.WriteLine("fixture-server: $($_.Exception.Message)")
        try { $res.StatusCode = 500 } catch { $null = $_ }
    } finally {
        try { $res.Close() } catch { $null = $_ }
    }
}
