# The shell killed outright (TerminateProcess, no shutdown): does the
# server, and what the server started, end too? (The Job Object's handle
# closes with the shell, and KILL_ON_JOB_CLOSE ends its processes.)
param(
  [Parameter(Mandatory = $true)][string]$Bin,
  [Parameter(Mandatory = $true)][string]$Out
)
$ErrorActionPreference = 'Continue'
New-Item -ItemType Directory -Force -Path $Out | Out-Null
$Out = (Resolve-Path $Out).Path
$data = Join-Path $env:RUNNER_TEMP 'data-hardkill'
New-Item -ItemType Directory -Force -Path $data | Out-Null
$env:STUDIO_NODE = (Get-Command node).Source
$env:STUDIO_SHELL_MODE = 'direct'
$env:STUDIO_SHELL_DATA_DIR = $data
$env:STUDIO_SHELL_TEST = '1'
$env:STUDIO_SHELL_TEST_PLAN = 'hold'
$env:STUDIO_SHELL_TEST_DIR = $Out
Remove-Item Env:STUDIO_SHELL_SCREENSHOT -ErrorAction SilentlyContinue
$p = Start-Process -FilePath $Bin -PassThru -RedirectStandardError "$Out\shell.log" -RedirectStandardOutput "$Out\shell-out.log"
for ($i = 0; $i -lt 120 -and -not (Test-Path "$Out\pids.json"); $i++) { Start-Sleep -Milliseconds 500 }
if (-not (Test-Path "$Out\pids.json")) {
  '{"error": "pids.json never appeared"}' | Set-Content "$Out\hardkill.json"
  Stop-Process -Id $p.Id -Force
  exit 0
}
Stop-Process -Id $p.Id -Force
Start-Sleep -Seconds 3
$j = Get-Content "$Out\pids.json" | ConvertFrom-Json
$alive = @()
foreach ($s in $j.servers) {
  foreach ($id in @($s.server, $s.helper)) {
    if (Get-Process -Id $id -ErrorAction SilentlyContinue) { $alive += $id }
  }
}
@{ shell = $p.Id; aliveAfterKill = $alive } | ConvertTo-Json | Set-Content "$Out\hardkill.json"
Get-Content "$Out\hardkill.json"
foreach ($id in $alive) { Stop-Process -Id $id -Force -ErrorAction SilentlyContinue }
exit 0
