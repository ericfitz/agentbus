agentbus for Windows
====================

Unzipping this archive does not install agentbus. The harnesses (Claude
Code, Codex, Grok Build) start agentbus.exe by name, so it must be on your
PATH, and `agentbus init` refuses to run until it is.

Recommended: install with the script, which verifies the download, copies
agentbus.exe to %LocalAppData%\Programs\agentbus and adds that directory to
your user PATH:

    irm https://github.com/ericfitz/agentbus/releases/latest/download/install.ps1 | iex

Or with a package manager:

    scoop bucket add ericfitz https://github.com/ericfitz/scoop-bucket
    scoop install agentbus

To install this copy by hand instead, in PowerShell from this folder:

    $dir = "$env:LOCALAPPDATA\Programs\agentbus"
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    Copy-Item .\agentbus.exe $dir
    $p = [Environment]::GetEnvironmentVariable('Path', 'User')
    [Environment]::SetEnvironmentVariable('Path', "$p;$dir", 'User')

Then open a new terminal and run:

    agentbus init --global      (once per machine)
    agentbus init               (inside each repository)

Documentation: https://github.com/ericfitz/agentbus/blob/main/docs/install.md
