# The shell's smoke test on a Windows runner: the stream stub, a screenshot
# poller, then the shell in STUDIO_SMOKE=1 mode, whose page runs the checks.
# The first copy relaunches itself; the second closes its window.
# Afterwards: are any of the servers, or the children they started, still
# running, and did the default browser get the outside links?
#
# Usage: smoke-windows.ps1 -Bin SHELL_EXE -Out OUT_DIR -Repo REPO_ROOT
param([string]$Bin, [string]$Out, [string]$Repo)
$ErrorActionPreference = 'Continue'
$tmp = Join-Path $env:RUNNER_TEMP 'studio-smoke'
Remove-Item -Recurse -Force $tmp, $Out -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path (Join-Path $Out 'shots'), $tmp | Out-Null

$stub = Start-Process -FilePath node -ArgumentList "`"$Repo\desktop\tauri\test\stream-stub.mjs`"", '47123' -PassThru -WindowStyle Hidden `
  -RedirectStandardOutput (Join-Path $Out 'stub.log') -RedirectStandardError (Join-Path $Out 'stub.err.log')
Copy-Item -Recurse "$Repo\testdata\corpus" (Join-Path $tmp 'kb')
New-Item -ItemType Directory -Force -Path (Join-Path $tmp 'picked') | Out-Null

$poller = Start-Process -FilePath powershell -ArgumentList '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', "`"$Repo\desktop\tauri\ci\shots-windows.ps1`"", '-Out', "`"$Out`"" -PassThru -WindowStyle Hidden

$env:STUDIO_SMOKE = '1'
$env:STUDIO_SMOKE_OUT = $Out
$env:STUDIO_SMOKE_PRELOAD = "$Repo\desktop\tauri\test\smoke-preload.mjs"
$env:STUDIO_SMOKE_STUB = 'http://127.0.0.1:47123'
$env:STUDIO_SMOKE_FOLDER = (Join-Path $tmp 'picked')
$env:STUDIO_SMOKE_REAL_DIALOG = '1'
$env:STUDIO_APP_DIR = $Repo
$env:STUDIO_DATA_DIR = (Join-Path $tmp 'data')
$env:KB_ROOT = (Join-Path $tmp 'kb')
$env:AI_ENABLED = '0'

$sw = [System.Diagnostics.Stopwatch]::StartNew()
$p = Start-Process -FilePath $Bin -PassThru
$null = $p.Handle  # keeps the exit code readable
if (-not $p.WaitForExit(300000)) { "first copy still running after 300 s" | Tee-Object -Append (Join-Path $Out 'run.txt'); Stop-Process -Id $p.Id -Force }
"first copy exited with $($p.ExitCode) after $($sw.ElapsedMilliseconds) ms" | Tee-Object -Append (Join-Path $Out 'run.txt')

$r2 = Join-Path $Out 'results-2.json'
for ($i = 0; $i -lt 360; $i++) {
  if ((Test-Path $r2) -and ((Get-Content -Raw $r2) -match '"stop"')) { break }
  Start-Sleep -Milliseconds 500
}
if (Test-Path $r2) {
  $pid2 = (Get-Content -Raw $r2 | ConvertFrom-Json).shellPid
  for ($i = 0; $i -lt 60; $i++) { if (-not (Get-Process -Id $pid2 -ErrorAction SilentlyContinue)) { break }; Start-Sleep -Milliseconds 500 }
  if (Get-Process -Id $pid2 -ErrorAction SilentlyContinue) { "second copy $pid2 still running" } else { "second copy $pid2 exited" } | Tee-Object -Append (Join-Path $Out 'run.txt')
}
Start-Sleep -Seconds 1

# Orphans.
$rows = @()
$pidsFile = Join-Path $Out 'server-pids.jsonl'
if (Test-Path $pidsFile) { $rows = Get-Content $pidsFile | Where-Object { $_ } | ForEach-Object { $_ | ConvertFrom-Json } }
$left = @()
foreach ($r in $rows) { foreach ($x in @($r.server, $r.child)) { if (Get-Process -Id $x -ErrorAction SilentlyContinue) { $left += $x } } }
$nodes = @(Get-CimInstance Win32_Process -Filter "Name='node.exe'" | Where-Object { $_.ProcessId -ne $stub.Id } | Select-Object ProcessId, CommandLine)
[ordered]@{ serversRecorded = $rows.Count; pidsChecked = $rows.Count * 2; stillAlive = $left; otherNodeProcesses = $nodes; ok = ($left.Count -eq 0) } |
  ConvertTo-Json -Depth 4 | Tee-Object (Join-Path $Out 'orphans.json')

# The default browser: processes that got the smoke test's outside links.
$browsers = @(Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -match 'studio-smoke=' } | Select-Object ProcessId, Name, CommandLine)
$browsers | ConvertTo-Json -Depth 3 | Set-Content (Join-Path $Out 'browser-opened.json')
foreach ($b in $browsers) { Stop-Process -Id $b.ProcessId -Force -ErrorAction SilentlyContinue }

Copy-Item (Join-Path $tmp 'data\logs\*.log') $Out -ErrorAction SilentlyContinue
Get-ChildItem (Join-Path $env:USERPROFILE 'Downloads') -ErrorAction SilentlyContinue | Out-String | Set-Content (Join-Path $Out 'downloads-dir.txt')
Stop-Process -Id $poller.Id, $stub.Id -Force -ErrorAction SilentlyContinue
exit 0
