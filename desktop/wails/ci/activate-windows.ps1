# Activates the shell's window while its folder dialog is open, as another
# program (or a second launch) could: SetForegroundWindow several times, then
# a synthetic WM_SETFOCUS. Each step is appended to dialog-activate.log in
# STUDIO_SHELL_TEST_DIR with the shell's state after it, so the log shows
# where the shell stopped if it ends.
$out = Join-Path $env:STUDIO_SHELL_TEST_DIR 'dialog-activate.log'
Remove-Item $out -ErrorAction SilentlyContinue
Add-Type -TypeDefinition @'
using System;
using System.Text;
using System.Runtime.InteropServices;
public static class Act {
  [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr h, int cmd);
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);
  [DllImport("user32.dll")] public static extern bool SetWindowPos(IntPtr h, IntPtr after, int x, int y, int cx, int cy, uint flags);
  [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
  [DllImport("user32.dll")] public static extern bool IsWindowEnabled(IntPtr h);
  [DllImport("user32.dll")] public static extern IntPtr GetLastActivePopup(IntPtr h);
  [DllImport("user32.dll", CharSet = CharSet.Unicode)] public static extern int GetWindowText(IntPtr h, StringBuilder s, int n);
  [DllImport("user32.dll")] public static extern void keybd_event(byte vk, byte scan, uint flags, UIntPtr extra);
  [DllImport("user32.dll")] public static extern IntPtr SendMessageTimeout(IntPtr h, uint msg, UIntPtr w, IntPtr l, uint flags, uint timeout, out UIntPtr result);
  public static string Title(IntPtr h) { var s = new StringBuilder(256); GetWindowText(h, s, 256); return s.ToString(); }
}
'@
function Log($step, $extra) {
  $p = Get-Process -Name 'activate-studio' -ErrorAction SilentlyContinue | Select-Object -First 1
  $fg = [Act]::GetForegroundWindow()
  $line = [ordered]@{
    at = (Get-Date).ToString('HH:mm:ss.fff'); step = $step; shellAlive = [bool]$p
    foreground = ('0x{0:X}' -f [int64]$fg); foregroundTitle = [Act]::Title($fg)
    mainEnabled = $(if ($script:hwnd) { [Act]::IsWindowEnabled($script:hwnd) } else { $null })
    extra = $extra
  }
  ($line | ConvertTo-Json -Compress) | Add-Content -Path $out
}
$p = Get-Process -Name 'activate-studio' -ErrorAction SilentlyContinue | Where-Object { $_.MainWindowHandle -ne 0 } | Select-Object -First 1
if (-not $p) { Log 'no-window' $null; Write-Output 'no shell window found'; exit 1 }
$script:hwnd = $p.MainWindowHandle
Log 'start' ('main window 0x{0:X} "{1}", popup 0x{2:X}' -f [int64]$hwnd, [Act]::Title($hwnd), [int64][Act]::GetLastActivePopup($hwnd))
# What the screenshot script did in the v2.17.0 crash: restore, topmost, activate.
[Act]::ShowWindow($hwnd, 9) | Out-Null
[Act]::SetWindowPos($hwnd, [IntPtr](-1), 0, 0, 0, 0, 0x0003) | Out-Null
Log 'restore-topmost' $null
for ($i = 1; $i -le 6; $i++) {
  # A tap on Alt lets a background process take the foreground.
  [Act]::keybd_event(0x12, 0, 0, [UIntPtr]::Zero); [Act]::keybd_event(0x12, 0, 2, [UIntPtr]::Zero)
  $ok = [Act]::SetForegroundWindow($hwnd)
  Start-Sleep -Milliseconds 500
  Log "SetForegroundWindow-$i" "returned $ok"
}
$ws = New-Object -ComObject WScript.Shell
$aa = $ws.AppActivate('ACTIVATE Studio')
Start-Sleep -Milliseconds 500
Log 'AppActivate' "returned $aa"
# Not what a person or another program would do, but the same message Windows
# sends when focus arrives: one WM_SETFOCUS (7) straight to the main window.
$res = [UIntPtr]::Zero
$r = [Act]::SendMessageTimeout($hwnd, 7, [UIntPtr]::Zero, [IntPtr]::Zero, 2, 8000, [ref]$res)
Start-Sleep -Milliseconds 500
Log 'synthetic-WM_SETFOCUS' "SendMessageTimeout returned $r"
Log 'done' $null
Write-Output (Get-Content $out -Raw)
