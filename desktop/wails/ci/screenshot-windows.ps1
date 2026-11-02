# A screenshot for the self test: the shell's window, brought to the front
# (the runner's console is otherwise on top of it), or the whole desktop
# when the window is not found.
param([Parameter(Mandatory = $true)][string]$Out)
Add-Type -AssemblyName System.Windows.Forms, System.Drawing
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class Win {
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int Left, Top, Right, Bottom; }
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
  [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr h, int cmd);
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);
  [DllImport("user32.dll")] public static extern bool SetWindowPos(IntPtr h, IntPtr after, int x, int y, int cx, int cy, uint flags);
}
'@
$b = [System.Windows.Forms.SystemInformation]::VirtualScreen
$left = $b.Left; $top = $b.Top; $w = $b.Width; $h = $b.Height
$p = Get-Process -Name 'activate-studio' -ErrorAction SilentlyContinue | Where-Object { $_.MainWindowHandle -ne 0 } | Select-Object -First 1
if ($p) {
  $hwnd = $p.MainWindowHandle
  [Win]::ShowWindow($hwnd, 9) | Out-Null                                   # restore
  [Win]::SetWindowPos($hwnd, [IntPtr](-1), 0, 0, 0, 0, 0x0003) | Out-Null  # topmost, no move or size
  [Win]::SetForegroundWindow($hwnd) | Out-Null
  Start-Sleep -Milliseconds 700
  $r = New-Object Win+RECT
  if ([Win]::GetWindowRect($hwnd, [ref]$r)) {
    $left = [Math]::Max($r.Left, $b.Left); $top = [Math]::Max($r.Top, $b.Top)
    $w = [Math]::Min($r.Right, $b.Right) - $left; $h = [Math]::Min($r.Bottom, $b.Bottom) - $top
  }
}
$bmp = New-Object System.Drawing.Bitmap $w, $h
$g = [System.Drawing.Graphics]::FromImage($bmp)
$g.CopyFromScreen($left, $top, 0, 0, $bmp.Size)
$bmp.Save($Out, [System.Drawing.Imaging.ImageFormat]::Png)
$g.Dispose(); $bmp.Dispose()
