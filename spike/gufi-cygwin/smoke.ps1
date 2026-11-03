# Studio-shaped smoke tests for the Cygwin GUFI payload, run from PowerShell on a
# runner with no Cygwin installed. Every child process gets a cleared
# environment holding only PATH (what a launcher would set), unless a test says
# otherwise. Findings go to $Out\smoke-log.md; the script does not fail on a
# failed check, since the numbers are the point.
param(
  [Parameter(Mandatory)][string]$Payload,
  [Parameter(Mandatory)][string]$Out,
  [Parameter(Mandatory)][string]$Here,
  [string]$Work = 'C:\spike'
)
$ErrorActionPreference = 'Continue'
$PSNativeCommandArgumentPassing = 'Standard'
$utf8 = [Text.UTF8Encoding]::new($false)
[Console]::OutputEncoding = $utf8
$OutputEncoding = $utf8
New-Item -ItemType Directory -Force $Out, $Work | Out-Null
$Log = Join-Path $Out 'smoke-log.md'
Set-Content -Path $Log -Value '# Smoke log' -Encoding utf8
$Bin = Join-Path $Payload 'bin'
$Results = [System.Collections.Generic.List[object]]::new()

function Say([string]$s = '') { Add-Content -Path $Log -Value $s -Encoding utf8; Write-Host $s }
function Head([string]$s) { Say ''; Say "## $s"; Say '' }
function Code($s, [int]$max = 30, [int]$width = 400) {
  $txt = if ($s -is [array]) { $s -join "`n" } else { [string]$s }
  $lines = @($txt -split "`r?`n")
  if ($lines.Count -gt $max) { $lines = @($lines[0..($max - 1)]) + "... ($($lines.Count) lines in all)" }
  Say '```'
  foreach ($l in $lines) { if ($l.Length -gt $width) { $l = $l.Substring(0, $width) + '...' }; Say $l }
  Say '```'
}
function Rec([string]$test, [string]$result, [string]$detail = '') {
  $Results.Add([pscustomobject]@{ Test = $test; Result = $result; Detail = $detail })
  Say "**${test}: ${result}** $detail"
}
function Cyg([string]$p) {   # C:\a\b -> /cygdrive/c/a/b
  $p = $p -replace '\\', '/'
  if ($p -match '^([A-Za-z]):(.*)$') { return '/cygdrive/' + $Matches[1].ToLower() + $Matches[2] }
  return $p
}
function Run-Native {
  param([string]$Exe, [string[]]$Arguments = @(), [hashtable]$EnvVars, [string]$StdinFile, [int]$TimeoutSec = 1200)
  $psi = [Diagnostics.ProcessStartInfo]::new()
  $psi.FileName = if ([IO.Path]::IsPathRooted($Exe)) { $Exe } else { Join-Path $Bin "$Exe.exe" }
  foreach ($a in $Arguments) { [void]$psi.ArgumentList.Add($a) }
  $psi.UseShellExecute = $false
  $psi.RedirectStandardOutput = $true
  $psi.RedirectStandardError = $true
  $psi.RedirectStandardInput = [bool]$StdinFile
  $psi.StandardOutputEncoding = $utf8
  $psi.StandardErrorEncoding = $utf8
  $psi.Environment.Clear()
  foreach ($k in $EnvVars.Keys) { $psi.Environment[$k] = $EnvVars[$k] }
  $sw = [Diagnostics.Stopwatch]::StartNew()
  try { $p = [Diagnostics.Process]::Start($psi) }
  catch { return [pscustomobject]@{ Code = -9999; Out = ''; Err = "start failed: $_"; Sec = 0 } }
  $so = $p.StandardOutput.ReadToEndAsync()
  $se = $p.StandardError.ReadToEndAsync()
  if ($StdinFile) {
    $bytes = [IO.File]::ReadAllBytes($StdinFile)
    $p.StandardInput.BaseStream.Write($bytes, 0, $bytes.Length)
    $p.StandardInput.Close()
  }
  if (-not $p.WaitForExit($TimeoutSec * 1000)) { try { $p.Kill($true) } catch {}; return [pscustomobject]@{ Code = -8888; Out = ''; Err = 'timeout'; Sec = $sw.Elapsed.TotalSeconds } }
  $p.WaitForExit()
  [pscustomobject]@{ Code = $p.ExitCode; Out = $so.Result; Err = $se.Result; Sec = [math]::Round($sw.Elapsed.TotalSeconds, 2) }
}
function Lines($s) { @(([string]$s) -split "`r?`n" | Where-Object { $_ -ne '' }) }
function SumInts($s) { $t = [int64]0; foreach ($l in Lines $s) { if ($l -match '^\d+$') { $t += [int64]$l } }; $t }
function Octal($n) { [Convert]::ToString([int64]$n, 8) }

# What the launcher would set. SystemRoot is added only if a test shows it is needed.
$E1 = @{ PATH = $Bin }
$E2 = @{ PATH = $Bin; LANG = 'C.UTF-8' }

# ---------------------------------------------------------------------------
Head 'Environment'
Say "Runner: $([Environment]::OSVersion.VersionString), $([Environment]::ProcessorCount) logical processors, PowerShell $($PSVersionTable.PSVersion)"
$cyg = @('C:\cygwin', 'C:\cygwin64', 'C:\msys64\usr\bin\cygwin1.dll') | Where-Object { Test-Path $_ }
Say "Cygwin present on the runner outside the payload: $(if ($cyg) { $cyg -join ', ' } else { 'no' })"
$w = (Get-Command cygwin1.dll -ErrorAction SilentlyContinue)
Say "cygwin1.dll on the parent PATH: $(if ($w) { $w.Source } else { 'no' })"
$ver = (Get-Item (Join-Path $Bin 'cygwin1.dll')).VersionInfo
Say "Payload cygwin1.dll: $($ver.FileVersion) / $($ver.ProductVersion)"
Say "Defender real-time monitoring: $((Get-MpPreference -ErrorAction SilentlyContinue).DisableRealtimeMonitoring) (DisableRealtimeMonitoring)"
Say "Long paths registry (LongPathsEnabled): $((Get-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Control\FileSystem' -ErrorAction SilentlyContinue).LongPathsEnabled)"
$vol = Get-Volume -DriveLetter C
Say "C: is $($vol.FileSystem), free $([math]::Round($vol.SizeRemaining/1GB,1)) GB"
Say "Payload: $((Get-ChildItem $Payload -Recurse -File | Measure-Object Length -Sum).Sum / 1MB) MB unpacked, $((Get-ChildItem $Bin -Filter *.dll).Count) DLLs, $((Get-ChildItem $Bin -Filter *.exe).Count) exes"

# ---------------------------------------------------------------------------
Head '1. Launch from PowerShell with PATH only'
$r = Run-Native getfattr @('--version') $E1
if ($r.Code -ne 0 -or -not $r.Out) {
  Say "PATH alone failed (exit $($r.Code)): $($r.Err)"
  $E1['SystemRoot'] = $env:SystemRoot; $E2['SystemRoot'] = $env:SystemRoot
  $r = Run-Native getfattr @('--version') $E1
  Rec 'launch with PATH only' 'FAIL, needs SystemRoot' "with SystemRoot added: exit $($r.Code)"
} else { Rec 'launch with PATH only' 'PASS' "getfattr --version exit $($r.Code): $(($r.Out -split "`n")[0])" }
foreach ($t in 'gufi_query', 'gufi_dir2index', 'gufi_sqlite3') {
  $r = Run-Native $t @('-h') $E1
  $firstLine = (Lines ($r.Out + "`n" + $r.Err) | Select-Object -First 1)
  Rec "launch $t -h" $(if ($r.Code -eq -1073741515 -or $r.Code -eq -9999) { 'FAIL' } else { 'PASS' }) "exit $($r.Code) in $($r.Sec) s; $firstLine"
}
# DLL lookup without the payload on PATH: Windows looks in the exe's own directory first.
$r = Run-Native gufi_query @('-h') @{ SystemRoot = $env:SystemRoot }
Rec 'launch by full path with no PATH (DLLs found beside the exe)' $(if ($r.Code -eq -1073741515 -or $r.Code -eq -9999) { 'FAIL' } else { 'PASS' }) "exit $($r.Code)"
# What / is when the DLLs sit in <payload>\bin
$r = Run-Native sh @('-c', 'pwd') $E1
Say "(sh.exe is not in bin yet; start: $($r.Code) $($r.Err))"

# ---------------------------------------------------------------------------
Head '2. Test tree on C:'
$Tree = 'C:\tree'
$Manifest = Join-Path $Work 'manifest.json'
$sw = [Diagnostics.Stopwatch]::StartNew()
$genOut = & python (Join-Path $Here 'gen_tree.py') $Tree $Manifest 2>&1
Code ($genOut -join "`n")
Say "Generation: $([math]::Round($sw.Elapsed.TotalSeconds,1)) s"
$m = Get-Content $Manifest -Raw -Encoding utf8 | ConvertFrom-Json
# permission probes
$perm = Join-Path $Tree 'perm'; New-Item -ItemType Directory -Force $perm | Out-Null
foreach ($n in 'ro.txt', 'hidden.txt', 'denywrite.txt', 'tool.exe', 'script.sh', 'plain.txt') { Set-Content -Path (Join-Path $perm $n) -Value 'x' -Encoding ascii }
attrib +R (Join-Path $perm 'ro.txt')
attrib +H (Join-Path $perm 'hidden.txt')
icacls (Join-Path $perm 'denywrite.txt') /deny "$($env:USERNAME):(W,WD,AD)" | Out-Null
$ExpectedFiles = [int]$m.files + 6
$ExpectedDirs = [int]$m.dirs_below_root + 2   # plus the root and perm
Say "Created $($m.files) files in $($m.dirs_below_root + 1) directories (plus 6 permission probes); long paths: $($m.counts.long), longest $(($m.long_files | Measure-Object len -Maximum).Maximum) characters"
$sw.Restart()
$walk = (Get-ChildItem -LiteralPath $Tree -Recurse -File -Force | Measure-Object).Count
Say "Baseline, PowerShell Get-ChildItem -Recurse -File: $walk files in $([math]::Round($sw.Elapsed.TotalSeconds,1)) s"
$sw.Restart()
$walkPy = Join-Path $Work 'walk.py'
Set-Content -Path $walkPy -Encoding ascii -Value @'
import os, sys
n = 0
for r, d, f in os.walk('\\\\?\\' + sys.argv[1]):
    n += len(f)
print(n)
'@
$pyWalk = (& python $walkPy $Tree) -join ''
Say "Baseline, Python os.walk (extended-length path): $pyWalk files in $([math]::Round($sw.Elapsed.TotalSeconds,1)) s"

# ---------------------------------------------------------------------------
Head '3. Extended attributes on NTFS (before the index, so -x can see them)'
$Targets = @(
  @{ Win = $m.regular_example; What = 'regular file' },
  @{ Win = $m.long_files[0].path; What = "path of $($m.long_files[0].len) characters" },
  @{ Win = $m.unicode_files[0]; What = 'non-ASCII name' },
  @{ Win = $m.space_files[0]; What = 'name with spaces' },
  @{ Win = (Join-Path $Tree 'with spaces'); What = 'directory' }
)
$tagOk = 0
foreach ($t in $Targets) {
  $cp = Cyg $t.Win
  $r = Run-Native setfattr @('-n', 'user.studio.tags', '-v', 'proj-a,proj-b', $cp) $E2
  $g = Run-Native getfattr @('--only-values', '--absolute-names', '-n', 'user.studio.tags', $cp) $E2
  $ok = ($r.Code -eq 0 -and $g.Out.Trim() -eq 'proj-a,proj-b')
  if ($ok) { $tagOk++ }
  Say "- setfattr then getfattr, $($t.What): set exit $($r.Code), read back '$($g.Out.Trim())' $($r.Err.Trim()) $($g.Err.Trim())"
}
Rec 'setfattr/getfattr user.* on NTFS' $(if ($tagOk -eq $Targets.Count) { 'PASS' } else { "PARTIAL $tagOk of $($Targets.Count)" }) 'run with LANG=C.UTF-8 (the first test of these uses a non-ASCII path)'
# the same two with only PATH, for the non-ASCII and the long path
$cp = Cyg $m.unicode_files[1]
$r = Run-Native setfattr @('-n', 'user.studio.tags', '-v', 'proj-a', $cp) $E1
Say "- setfattr on a non-ASCII path with PATH only: exit $($r.Code) $($r.Err.Trim())"
$g = Run-Native getfattr @('-d', '--absolute-names', (Cyg $m.regular_example)) $E1
Code $g.Out
Say 'What NTFS holds (fsutil file queryEA):'
Code ((& fsutil file queryEA $m.regular_example 2>&1) -join "`n") 20
# Is it the NTFS EA, or an alternate stream? List streams.
Code ((Get-Item -LiteralPath $m.regular_example -Stream * | Format-Table Stream, Length -AutoSize | Out-String)) 10

# ---------------------------------------------------------------------------
Head '4. gufi_dir2index of the tree through /cygdrive/c'
$Idx = 'C:\idx'
New-Item -ItemType Directory -Force $Idx | Out-Null
function Index-Run([string]$name, [string[]]$flags, [hashtable]$envh, [string]$src) {
  $dst = Join-Path $Idx $name
  New-Item -ItemType Directory -Force $dst | Out-Null
  $r = Run-Native gufi_dir2index (@($flags) + @($src, (Cyg $dst))) $envh
  $dirsMade = (Get-ChildItem -LiteralPath $dst -Recurse -Directory -Force | Measure-Object).Count
  $dbs = (Get-ChildItem -LiteralPath $dst -Recurse -Filter db.db -File -Force | Measure-Object).Count
  $bytes = (Get-ChildItem -LiteralPath $dst -Recurse -File -Force | Measure-Object Length -Sum).Sum
  Say "- **$name** ``gufi_dir2index $($flags -join ' ') $src``: exit $($r.Code) in **$($r.Sec) s**; index has $dbs db.db files, $dirsMade directories, $([math]::Round($bytes/1MB,1)) MB"
  if ($r.Err.Trim()) {
    Code ((Lines $r.Err | ForEach-Object { if ($_.Length -gt 260) { $_.Substring(0, 60) + ' ... ' + $_.Substring($_.Length - 180) } else { $_ } }) -join "`n") 14 300
  }
  [pscustomobject]@{ Name = $name; Run = $r; Dst = $dst; Dbs = $dbs }
}
$Src = Cyg $Tree
$ia = Index-Run 'a' @('-x', '-n', '1') $E1 $Src
$ib = Index-Run 'b' @('-x', '-n', '4') $E1 $Src
$ic = Index-Run 'c' @('-x', '-n', '4') $E2 $Src
$id = Index-Run 'd' @('-n', '4') $E1 $Src
$ie = Index-Run 'e' @('-x', '-n', '4') $E1 $Tree
$if2 = Index-Run 'f' @('-x', '-n', '4') $E1 ($Tree -replace '\\', '/')
$cores = [Environment]::ProcessorCount
Rec 'gufi_dir2index 50k files, 1 thread, -x' $(if ($ia.Run.Code -eq 0) { 'PASS' } else { "exit $($ia.Run.Code)" }) "$($ia.Run.Sec) s"
Rec "gufi_dir2index 50k files, 4 threads ($cores cores), -x" $(if ($ib.Run.Code -eq 0) { 'PASS' } else { "exit $($ib.Run.Code)" }) "$($ib.Run.Sec) s"
Rec 'gufi_dir2index 4 threads, no -x' $(if ($id.Run.Code -eq 0) { 'PASS' } else { "exit $($id.Run.Code)" }) "$($id.Run.Sec) s"
Rec 'gufi_dir2index with LANG=C.UTF-8' $(if ($ic.Run.Code -eq 0) { 'PASS' } else { "exit $($ic.Run.Code)" }) "$($ic.Run.Sec) s"
Rec 'gufi_dir2index with a Windows-form source (C:\tree)' $(if ($ie.Run.Code -eq 0) { 'PASS' } else { "exit $($ie.Run.Code)" }) "$($ie.Run.Sec) s, $($ie.Dbs) db.db files"
Rec 'gufi_dir2index with a C:/tree source' $(if ($if2.Dbs -gt 10) { 'PASS' } else { 'FAIL' }) "$($if2.Run.Sec) s, $($if2.Dbs) db.db files"
Say "Baselines for the same tree: Get-ChildItem walk and Python os.walk are above. Expected entries: $ExpectedFiles files in $ExpectedDirs directories."

# ---------------------------------------------------------------------------
Head '5. gufi_query against the index'
$QB = (Cyg (Join-Path (Join-Path $Idx 'b') 'tree'))
Say "Index root used: $QB"
$topList = (Get-ChildItem (Join-Path $Idx 'b') -Force | ForEach-Object Name) -join ', '
Say "Contents of C:\idx\b: $topList"
function Count-Entries($idx, $envh) {
  $r = Run-Native gufi_query @('-n', '4', '-d', '|', '-E', 'SELECT count(*) FROM vrpentries;', $idx) $envh
  [pscustomobject]@{ Files = (SumInts $r.Out); Sec = $r.Sec; Run = $r }
}
function Count-Dirs($idx, $envh) {
  $r = Run-Native gufi_query @('-n', '4', '-d', '|', '-S', 'SELECT 1;', $idx) $envh
  (Lines $r.Out).Count
}
foreach ($pair in @(@('b', $E1), @('c', $E2), @('e', $E1), @('d', $E1))) {
  $n = $pair[0]
  $qi = Cyg (Join-Path (Join-Path $Idx $n) 'tree')
  $c = Count-Entries $qi $pair[1]
  $dcount = Count-Dirs $qi $pair[1]
  $ok = ($c.Files -eq $ExpectedFiles)
  Rec "entries in index $n" $(if ($ok) { 'PASS' } else { 'MISMATCH' }) "$($c.Files) entries (expected $ExpectedFiles), $dcount directories (expected $ExpectedDirs), per-directory count query $($c.Sec) s, exit $($c.Run.Code)"
}

# Studio's tree statistics (aggregate form)
$qa = @('-n', '4', '-d', '|', '-a', '1',
  '-I', 'CREATE TABLE t (k TEXT, n INT64, b INT64);',
  '-K', 'CREATE TABLE agg (k TEXT, n INT64, b INT64);',
  '-E', "INSERT INTO t SELECT lower(CASE WHEN name LIKE '%.%' THEN replace(name, rtrim(name, replace(name, '.', '')), '') ELSE '' END), count(*), sum(size) FROM vrpentries GROUP BY 1; INSERT INTO t SELECT char(1)||'summary', sum(totfiles), sum(totsize) FROM vrsummary; INSERT INTO t SELECT char(1)||'dirs', count(*), 0 FROM vrsummary;",
  '-J', 'INSERT INTO agg SELECT * FROM t;',
  '-G', "SELECT k, sum(n), sum(b) FROM agg GROUP BY k;", $QB)
$r = Run-Native gufi_query $qa $E1
Rec 'Studio tree-statistics aggregate query (-I -K -E -J -G)' $(if ($r.Code -eq 0) { 'PASS' } else { "exit $($r.Code)" }) "$($r.Sec) s"
Code (($r.Out -replace [char]1, '^A')) 12
if ($r.Err.Trim()) { Code $r.Err 6 }

# Name search, long paths, non-ASCII, paths
$r = Run-Native gufi_query @('-n', '4', '-d', '|', '-E', "SELECT rpath(sname, sroll)||'/'||name FROM vrpentries WHERE name LIKE '%sum%';", $QB) $E2
Rec 'name search LIKE %sum% (matches the r?sum? files)' $(if ((Lines $r.Out).Count -eq 20) { 'PASS' } else { 'CHECK' }) "$((Lines $r.Out).Count) rows (expected 20), $($r.Sec) s"
Code $r.Out 3
$r = Run-Native gufi_query @('-n', '4', '-d', '|', '-E', "SELECT length(rpath(sname, sroll)||'/'||name), rpath(sname, sroll)||'/'||name FROM vrpentries WHERE length(rpath(sname, sroll)||'/'||name) > 260;", $QB) $E1
$longRows = Lines $r.Out
Rec 'paths longer than 260 characters are indexed and returned' $(if ($longRows.Count -eq [int]$m.counts.long) { 'PASS' } else { 'MISMATCH' }) "$($longRows.Count) rows (expected $($m.counts.long)); longest returned $((($longRows | ForEach-Object { [int](($_ -split '\|')[0]) }) | Measure-Object -Maximum).Maximum) characters"
Code ($longRows | Select-Object -First 2) 2 260
# non-ASCII names come back intact?
$expectedNames = $m.unicode_files | ForEach-Object { Split-Path $_ -Leaf }
foreach ($pair in @(@('b (PATH only)', 'b', $E1), @('c (LANG=C.UTF-8 when indexed and queried)', 'c', $E2), @('b queried with LANG=C.UTF-8', 'b', $E2))) {
  $qi = Cyg (Join-Path (Join-Path $Idx $pair[1]) 'tree')
  $r = Run-Native gufi_query @('-n', '4', '-d', '|', '-E', "SELECT name FROM vrpentries WHERE rpath(sname, sroll) LIKE '%/unicode/%';", $qi) $pair[2]
  $got = Lines $r.Out
  $hit = @($expectedNames | Where-Object { $got -contains $_ }).Count
  Rec "non-ASCII names, index $($pair[0])" $(if ($hit -eq $expectedNames.Count) { 'PASS' } else { "$hit of $($expectedNames.Count) intact" }) "$($got.Count) rows"
  if ($hit -ne $expectedNames.Count) { Code ($got | Select-Object -First 4) 4 }
}
# how the unicode directories came out in the index (names of the index folders)
$udirs = (Get-ChildItem -LiteralPath (Join-Path (Join-Path $Idx 'b') 'tree\unicode') -Directory -Force | ForEach-Object Name) -join ' | '
$udirsC = (Get-ChildItem -LiteralPath (Join-Path (Join-Path $Idx 'c') 'tree\unicode') -Directory -Force | ForEach-Object Name) -join ' | '
Say "Index folder names for the non-ASCII directories, PATH only: $udirs"
Say "Index folder names for the non-ASCII directories, LANG=C.UTF-8: $udirsC"
# path forms
$r = Run-Native gufi_query @('-n', '1', '-d', '|', '-E', "SELECT path(), epath(), fpath(), rpath(sname, sroll), sname, sroll, name FROM vrpentries WHERE name = 'file_0004.json' LIMIT 1;", "$QB/d03/s04") $E1
Say 'Path forms returned (path(), epath(), fpath(), rpath(sname,sroll), sname, sroll, name), index given as /cygdrive/c/...:'
Code $r.Out 4
$r = Run-Native gufi_query @('-n', '1', '-d', '|', '-E', "SELECT rpath(sname, sroll)||'/'||name FROM vrpentries WHERE name = 'file_0004.json' LIMIT 1;", (Join-Path (Join-Path $Idx 'b') 'tree\d03\s04')) $E1
Say 'The same with the index given as a Windows path (C:\idx\b\tree\d03\s04):'
Code $r.Out 3
$r = Run-Native gufi_query @('-n', '1', '-d', '|', '-E', "SELECT rpath(sname, sroll)||'/'||name FROM vrpentries WHERE name = 'file_0004.json' LIMIT 1;", ((Join-Path (Join-Path $Idx 'b') 'tree\d03\s04') -replace '\\', '/')) $E1
Say 'The same with the index given as C:/idx/b/tree/d03/s04:'
Code $r.Out 3
# which source path is recorded for the entries of index e (Windows-form source)?
$r = Run-Native gufi_query @('-n', '1', '-d', '|', '-S', "SELECT name, rpath(sname, sroll) FROM vrsummary;", (Cyg (Join-Path (Join-Path $Idx 'e') 'tree\d03\s04'))) $E1
Code $r.Out 3
# sorted top-N
$r = Run-Native gufi_query @('-n', '4', '-d', '|', '-a', '1', '-I', 'CREATE TABLE t (p TEXT, s INT64);', '-K', 'CREATE TABLE agg (p TEXT, s INT64);', '-E', "INSERT INTO t SELECT rpath(sname, sroll)||'/'||name, size FROM vrpentries ORDER BY size DESC LIMIT 5;", '-J', 'INSERT INTO agg SELECT * FROM t;', '-G', 'SELECT p, s FROM agg ORDER BY s DESC LIMIT 5;', $QB) $E1
Rec 'top-5 largest files, aggregate' $(if ($r.Code -eq 0) { 'PASS' } else { "exit $($r.Code)" }) "$($r.Sec) s"

# ---------------------------------------------------------------------------
Head '6. uid, gid and mode that GUFI records for NTFS files'
$r = Run-Native gufi_query @('-n', '4', '-d', '|', '-E', 'SELECT uid, gid, mode, count(*) FROM vrpentries GROUP BY 1, 2, 3;', $QB) $E1
$agg = @{}
foreach ($l in Lines $r.Out) { $f = $l -split '\|'; if ($f.Count -eq 4) { $k = "uid=$($f[0]) gid=$($f[1]) mode=0$(Octal $f[2])"; $agg[$k] = [int64]$agg[$k] + [int64]$f[3] } }
Say 'Files (vrpentries), grouped:'
Code (($agg.GetEnumerator() | Sort-Object Value -Descending | ForEach-Object { "$($_.Key)  files=$($_.Value)" }) -join "`n") 12
$r = Run-Native gufi_query @('-n', '4', '-d', '|', '-S', 'SELECT uid, gid, mode, count(*) FROM vrsummary GROUP BY 1, 2, 3;', $QB) $E1
$agg = @{}
foreach ($l in Lines $r.Out) { $f = $l -split '\|'; if ($f.Count -eq 4) { $k = "uid=$($f[0]) gid=$($f[1]) mode=0$(Octal $f[2])"; $agg[$k] = [int64]$agg[$k] + [int64]$f[3] } }
Say 'Directories (vrsummary), grouped:'
Code (($agg.GetEnumerator() | Sort-Object Value -Descending | ForEach-Object { "$($_.Key)  dirs=$($_.Value)" }) -join "`n") 12
$r = Run-Native gufi_query @('-n', '1', '-d', '|', '-E', 'SELECT name, uid, gid, mode, size FROM vrpentries ORDER BY name;', "$QB/perm") $E1
Say 'Permission probes (name, uid, gid, mode as recorded):'
Code (((Lines $r.Out) | ForEach-Object { $f = $_ -split '\|'; "$($f[0])  uid=$($f[1]) gid=$($f[2]) mode=0$(Octal $f[3]) size=$($f[4])" }) -join "`n") 10
Say 'The probes on the Windows side:'
Code (((Get-ChildItem -LiteralPath $perm -Force | ForEach-Object { "$($_.Name)  attributes=$($_.Attributes)" }) -join "`n")) 10
Say "Current Windows identity: $([Security.Principal.WindowsIdentity]::GetCurrent().Name) $([Security.Principal.WindowsIdentity]::GetCurrent().User.Value)"
Code ((& icacls (Join-Path $perm 'plain.txt')) -join "`n") 6
$r = Run-Native gufi_query @('-n', '1', '-d', '|', '-S', 'SELECT name, uid, gid, mode FROM vrsummary;', "$QB/perm") $E1
Say 'perm directory (summary):'
Code $r.Out 3

# ---------------------------------------------------------------------------
Head '7. Extended attributes in the index (-x)'
$studioTags = @('-n', '4', '-d', '|', '-a', '1',
  '-I', 'CREATE TABLE t (k TEXT, n INT64);', '-K', 'CREATE TABLE agg (k TEXT, n INT64);',
  '-E', "INSERT INTO t SELECT CAST(x.value AS TEXT), count(*) FROM vrpentries v JOIN xattrs_pwd x ON v.inode = x.inode WHERE CAST(x.name AS TEXT) = 'user.studio.tags' GROUP BY 1; INSERT INTO t SELECT CAST(x.value AS TEXT), count(*) FROM vrsummary s JOIN xattrs_pwd x ON s.inode = x.inode WHERE CAST(x.name AS TEXT) = 'user.studio.tags' GROUP BY 1;",
  '-J', 'INSERT INTO agg SELECT * FROM t;', '-G', 'SELECT k, sum(n) FROM agg GROUP BY k;')
foreach ($pair in @(@('b (-x)', 'b'), @('d (no -x)', 'd'), @('e (-x, Windows-form source)', 'e'))) {
  $qi = Cyg (Join-Path (Join-Path $Idx $pair[1]) 'tree')
  $r = Run-Native gufi_query ($studioTags + $qi) $E1
  $rows = Lines $r.Out
  $expectedRows = "proj-a,proj-b|$($Targets.Count)"
  Rec "Studio label query on index $($pair[0])" $(if (($rows -join ';') -eq $expectedRows) { 'PASS' } elseif ($pair[1] -eq 'd') { 'as expected, no labels without -x' } else { 'CHECK' }) "rows: $($rows -join ' ; ') (expected $expectedRows for -x indexes); exit $($r.Code); $($r.Err.Trim())"
}
$r = Run-Native gufi_query @('-n', '4', '-d', '|', '-E', "SELECT rpath(sname, sroll)||'/'||v.name, CAST(x.name AS TEXT), CAST(x.value AS TEXT) FROM vrpentries v JOIN xattrs_pwd x ON v.inode = x.inode;", $QB) $E2
Say 'Entries with xattrs in index b (path, name, value):'
Code $r.Out 8 220
$r = Run-Native gufi_query @('-n', '1', '-d', '|', '-E', "SELECT name, count(*) FROM xattrs_pwd GROUP BY 1;", "$QB/with spaces") $E1
Say 'xattr names recorded in a directory (index b):'
Code $r.Out 6
# Are the xattr tables large? Size of an index with and without -x
$sx = (Get-ChildItem (Join-Path $Idx 'b') -Recurse -File -Force | Measure-Object Length -Sum).Sum
$sn = (Get-ChildItem (Join-Path $Idx 'd') -Recurse -File -Force | Measure-Object Length -Sum).Sum
Say "Index size with -x: $([math]::Round($sx/1MB,1)) MB, without: $([math]::Round($sn/1MB,1)) MB"

# ---------------------------------------------------------------------------
Head '8. AI pieces: vec0 and lembed'
$Model = Join-Path $Work 'minilm384.gguf'
$ModelUrl = 'https://huggingface.co/asg017/sqlite-lembed-model-examples/resolve/7a7bac37782986fe1d4f213de771a8a3d9170b35/all-MiniLM-L6-v2/all-MiniLM-L6-v2.e4ce9877.q8_0.gguf'
$sw = [Diagnostics.Stopwatch]::StartNew()
try { Invoke-WebRequest -Uri $ModelUrl -OutFile $Model -UseBasicParsing } catch { Say "model download failed: $_" }
$sha = (Get-FileHash $Model -Algorithm SHA256 -ErrorAction SilentlyContinue).Hash.ToLower()
Say "Model: $((Get-Item $Model).Length) bytes, sha256 $sha $(if ($sha -eq '71f1d177171468fb5f186c07019e303015aea17af275a67767760bba7be8d2e6') { '(matches the macOS CI pin)' } else { '(DIFFERS from the pin)' }), download $([math]::Round($sw.Elapsed.TotalSeconds,1)) s"
function Sql-File([string]$name, [string]$text) { $p = Join-Path $Work $name; [IO.File]::WriteAllText($p, $text.Replace("`r`n", "`n"), $utf8); $p }
foreach ($form in @(@('/cygdrive form', (Cyg $Model)), @('C:/ form', ($Model -replace '\\', '/')))) {
  $f = Sql-File 'embed.sql' @"
INSERT INTO temp.lembed_models(name, model) SELECT 'm', lembed_model_from_file('$($form[1])');
SELECT length(lembed('m', 'hello'));
"@
  $r = Run-Native gufi_sqlite3 @() $E1 -StdinFile $f
  Rec "lembed embeds 'hello' (model path as $($form[0]))" $(if ($r.Out.Trim() -eq '1536') { 'PASS' } else { 'FAIL' }) "got '$($r.Out.Trim())' (expected 1536 bytes = 384 floats), exit $($r.Code), $($r.Sec) s $($r.Err.Trim())"
}
$f = Sql-File 'search.sql' @"
INSERT INTO temp.lembed_models(name, model) SELECT 'm', lembed_model_from_file('$(Cyg $Model)');
CREATE VIRTUAL TABLE v USING vec0(id INTEGER PRIMARY KEY, fp384 float[384]);
INSERT INTO v(id, fp384) VALUES (1, lembed('m', 'a tree of per-directory databases holding file metadata')), (2, lembed('m', 'a recipe for lemon cake'));
CREATE TABLE q AS SELECT lembed('m', 'how is the file index laid out') AS e;
SELECT id, distance FROM v WHERE fp384 MATCH (SELECT e FROM q) ORDER BY distance LIMIT 2;
"@
$r = Run-Native gufi_sqlite3 @() $E1 -StdinFile $f
$first = (Lines $r.Out | Select-Object -First 1)
Rec 'vec0 nearest-neighbour over lembed vectors' $(if ($first -match '^1\b') { 'PASS' } else { 'FAIL' }) "$($r.Sec) s, exit $($r.Code)"
Code ($r.Out + $r.Err) 6
# vec functions inside gufi_query itself (what the one-pass search runs per directory)
$r = Run-Native gufi_query @('-n', '1', '-d', '|', '-E', "SELECT vec_version(), vec_distance_cosine(vec_f32('[1,0,0]'), vec_f32('[0,1,0]')), vec_distance_L2(vec_f32('[1,0,0]'), vec_f32('[0,1,0]'));", "$QB/d03/s04") $E1
Rec 'vec functions loaded inside gufi_query' $(if ($r.Out -match '1\.0\|1\.41') { 'PASS' } else { 'FAIL' }) "$(Lines $r.Out | Select-Object -First 1); $($r.Err.Trim())"
# gufi_vt starts gufi_query through /bin/sh
$f = Sql-File 'vt.sql' @"
CREATE VIRTUAL TABLE temp.v USING gufi_vt(index='$QB/d03/s04', threads=1, E="SELECT name FROM vrpentries;");
SELECT count(*) FROM temp.v;
"@
$r = Run-Native gufi_sqlite3 @() $E1 -StdinFile $f
$vt1 = (Lines $r.Out | Select-Object -First 1)
Rec 'gufi_vt without sh.exe in the payload' $(if ($vt1 -eq '100') { 'PASS (no sh needed)' } else { 'FAIL, as expected without /bin/sh' }) "result '$vt1' exit $($r.Code) $($r.Err.Trim())"
Copy-Item (Join-Path $Payload 'extras\sh.exe') (Join-Path $Bin 'sh.exe')
Get-ChildItem (Join-Path $Payload 'extras') -Filter *.dll | ForEach-Object { if (-not (Test-Path (Join-Path $Bin $_.Name))) { Copy-Item $_.FullName $Bin } }
$r = Run-Native gufi_sqlite3 @() $E1 -StdinFile $f
$vt2 = (Lines $r.Out | Select-Object -First 1)
Rec 'gufi_vt with sh.exe copied into bin' $(if ($vt2 -eq '100') { 'PASS' } else { 'FAIL' }) "result '$vt2' exit $($r.Code) $($r.Err.Trim())"
$r = Run-Native sh @('-c', 'pwd; ls / | head -20') $E1
Say 'What the Cygwin root is when the DLLs are in <payload>\bin (sh.exe: pwd, ls /):'
Code ($r.Out + $r.Err) 12


# ---------------------------------------------------------------------------
Head '8b. How deep a path GUFI indexes, and LongPathsEnabled'
$chainPy = Join-Path $Work 'chain.py'
Set-Content -Path $chainPy -Encoding ascii -Value @'
import os, sys
d = '\\\\?\\' + sys.argv[1]
for i in range(int(sys.argv[2])):
    d = os.path.join(d, ('c%02d_' % i) + 'y' * 21)
    os.makedirs(d, exist_ok=True)
    open(os.path.join(d, 'f.txt'), 'w').write('x')
print(len(d) - 4)
'@
$lpLen = (& python $chainPy 'C:\lp' 80) -join ''
Say "Chain tree C:\lp: 80 levels of 25-character names, deepest Windows path $lpLen characters"
function Chain-Index([string]$name) {
  $dst = Join-Path $Idx $name; New-Item -ItemType Directory -Force $dst | Out-Null
  $r = Run-Native gufi_dir2index @('-x', '-n', '4', '/cygdrive/c/lp', (Cyg $dst)) $E1
  $q = Run-Native gufi_query @('-n', '4', '-d', '|', '-S', 'SELECT length(rpath(sname, sroll)) FROM vrsummary;', (Cyg (Join-Path $dst 'lp'))) $E1
  $lens = @(Lines $q.Out | ForEach-Object { [int]$_ })
  $err = (Lines $r.Err | Where-Object { $_ -match 'Cannot open' } | Select-Object -First 1)
  $tail = if ($err) { $err.Substring([math]::Max(0, $err.Length - 160)) } else { '' }
  Say "- $name: exit $($r.Code); directories indexed $($lens.Count) of 81; longest indexed directory path $(if ($lens) { ($lens | Measure-Object -Maximum).Maximum } else { 0 }) characters (index form); first error ends: $tail"
}
Chain-Index 'lp1'
$fsKey = 'HKLM:\SYSTEM\CurrentControlSet\Control\FileSystem'
Set-ItemProperty -Path $fsKey -Name LongPathsEnabled -Value 0
Say "LongPathsEnabled now: $((Get-ItemProperty $fsKey).LongPathsEnabled)"
Chain-Index 'lp0'
$r = Run-Native gufi_query @('-n', '4', '-d', '|', '-E', 'SELECT count(*) FROM vrpentries;', $QB) $E1
Rec 'index b still readable with LongPathsEnabled=0' $(if ((SumInts $r.Out) -eq 50146) { 'PASS' } else { 'CHECK' }) "$(SumInts $r.Out) entries"
$r = Run-Native setfattr @('-n', 'user.studio.tags', '-v', 'lp', (Cyg $m.long_files[0].path)) $E2
Rec 'setfattr on a 360-character path with LongPathsEnabled=0' $(if ($r.Code -eq 0) { 'PASS' } else { 'FAIL' }) "exit $($r.Code) $($r.Err.Trim())"
Set-ItemProperty -Path $fsKey -Name LongPathsEnabled -Value 1

# ---------------------------------------------------------------------------
Head '9. Plain PowerShell invocation'
$old = $env:PATH
$env:PATH = "$Bin;$env:SystemRoot\system32"
$o = & gufi_query -n 2 -d '|' -E 'SELECT count(*) FROM vrpentries;' (Cyg (Join-Path (Join-Path $Idx 'b') 'tree\d03'))
$env:PATH = $old
Say "gufi_query run directly from PowerShell with `$env:PATH set (no process wrapper): exit $LASTEXITCODE, rows $((@($o)).Count), sum $(SumInts ($o -join "`n"))"

# ---------------------------------------------------------------------------
Head 'Summary'
$tbl = @('| Test | Result | Detail |', '|---|---|---|') + ($Results | ForEach-Object { "| $($_.Test) | $($_.Result) | $(($_.Detail -replace '\|', '/' -replace "`r?`n", ' ')) |" })
$tbl | ForEach-Object { Add-Content -Path $Log -Value $_ -Encoding utf8 }
if ($env:GITHUB_STEP_SUMMARY) { Add-Content -Path $env:GITHUB_STEP_SUMMARY -Value (@('## Smoke results') + $tbl) -Encoding utf8 }
$Results | ConvertTo-Json -Depth 3 | Set-Content (Join-Path $Out 'smoke-results.json') -Encoding utf8
exit 0
