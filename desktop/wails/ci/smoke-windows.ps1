# One self-test run of the shell on Windows.
#
# Usage: smoke-windows.ps1 -Bin <exe> -Mode <mode> -Out <dir> [-OpenReal] [-FocusGuard off] [-Activate sfw|restore|alt-sfw|appactivate|synthetic|all]
# While the real folder dialog is open the self test activates the window
# (activate-windows.ps1); -FocusGuard off turns the shell's guard against that off.
# Env:   STUDIO_APP_DIR, STUDIO_KB_ROOT, STUDIO_SHELL_TEST_FOLDER (set by the workflow)
param(
  [Parameter(Mandatory = $true)][string]$Bin,
  [Parameter(Mandatory = $true)][string]$Mode,
  [Parameter(Mandatory = $true)][string]$Out,
  [switch]$OpenReal,
  [string]$FocusGuard = 'on',
  [string]$Activate = 'all'
)
$ErrorActionPreference = 'Continue'
$here = $PSScriptRoot
New-Item -ItemType Directory -Force -Path $Out | Out-Null
$Out = (Resolve-Path $Out).Path
$data = Join-Path $env:RUNNER_TEMP "data-$Mode"
Remove-Item -Recurse -Force $data -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $data | Out-Null

$browsersBefore = @(Get-Process msedge, chrome, firefox -ErrorAction SilentlyContinue).Count
$env:STUDIO_NODE = (Get-Command node).Source
$env:STUDIO_SHELL_MODE = $Mode
$env:STUDIO_SHELL_DATA_DIR = $data
$env:STUDIO_SHELL_TEST = '1'
$env:STUDIO_SHELL_TEST_DIR = $Out
$env:STUDIO_SHELL_SCREENSHOT = "powershell -NoProfile -ExecutionPolicy Bypass -File $here\screenshot-windows.ps1"
$env:STUDIO_SHELL_TEST_DIALOG_CLOSE = "powershell -NoProfile -ExecutionPolicy Bypass -File $here\close-dialog-windows.ps1"
$env:STUDIO_SHELL_TEST_DIALOG_ACTIVATE = "powershell -NoProfile -ExecutionPolicy Bypass -File $here\activate-windows.ps1"
$env:STUDIO_SHELL_FOCUS_GUARD = $FocusGuard
$env:STUDIO_SHELL_TEST_ACTIVATE = $Activate
$env:STUDIO_SHELL_TEST_OPEN_REAL = $(if ($OpenReal) { '1' } else { '0' })
$env:STUDIO_SHELL_LAUNCH_T0 = [string][DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds()
$p = Start-Process -FilePath $Bin -PassThru -RedirectStandardError "$Out\shell.log" -RedirectStandardOutput "$Out\shell-out.log"
$finished = $p.WaitForExit(400000)
if (-not $finished) { Stop-Process -Id $p.Id -Force; $code = -1 } else { $code = $p.ExitCode }
Set-Content -Path "$Out\exit-code.txt" -Value $code

Start-Sleep -Seconds 1
$alive = @()
if (Test-Path "$Out\pids.log") {
  foreach ($line in Get-Content "$Out\pids.log") {
    $r = $line | ConvertFrom-Json
    foreach ($id in @($r.server, $r.helper)) {
      # Windows reuses PIDs quickly: count it only if it is still a node
      # process that started before the shell ended.
      $proc = Get-Process -Id $id -ErrorAction SilentlyContinue
      if ($proc -and $proc.ProcessName -eq 'node') { $alive += $id }
    }
  }
}
# Any node still running the Studio's server (a listing; nothing is killed).
Get-CimInstance Win32_Process -Filter "Name = 'node.exe'" |
  Where-Object { $_.CommandLine -like '*server*dist*main.js*' } |
  Select-Object ProcessId, ParentProcessId, CommandLine | Format-List | Out-File "$Out\node-after-exit.txt"
$browsersAfter = @(Get-Process msedge, chrome, firefox -ErrorAction SilentlyContinue).Count
@{ exitCode = $code; aliveAfterExit = $alive; browserProcessesBefore = $browsersBefore; browserProcessesAfter = $browsersAfter } |
  ConvertTo-Json | Set-Content "$Out\after-exit.json"
Copy-Item "$data\logs\studio.log" "$Out\studio.log" -ErrorAction SilentlyContinue
Get-Content "$Out\after-exit.json"
exit 0
