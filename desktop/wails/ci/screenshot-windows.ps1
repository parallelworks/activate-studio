# A screenshot for the self test. The shell's window is captured with
# PrintWindow, so the runner's console in front of it does not matter and
# the window is not activated (activating it while the folder dialog is open
# made Wails' focus handler fail and end the shell). The dialog's own
# picture, and any picture when the window is not found, is the whole screen.
param([Parameter(Mandatory = $true)][string]$Out)
Add-Type -AssemblyName System.Windows.Forms, System.Drawing
Add-Type -TypeDefinition @'
using System;
using System.Collections.Generic;
using System.Runtime.InteropServices;
public static class Win {
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int Left, Top, Right, Bottom; }
  public delegate bool EnumProc(IntPtr h, IntPtr l);
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
  [DllImport("user32.dll")] public static extern bool PrintWindow(IntPtr h, IntPtr hdc, uint flags);
  [DllImport("user32.dll")] public static extern bool EnumWindows(EnumProc p, IntPtr l);
  [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr h, out uint pid);
  [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
  public static IntPtr Largest(uint pid) {
    IntPtr best = IntPtr.Zero; long area = 0;
    EnumWindows(delegate (IntPtr h, IntPtr l) {
      uint p; GetWindowThreadProcessId(h, out p);
      RECT r;
      if (p == pid && IsWindowVisible(h) && GetWindowRect(h, out r)) {
        long a = (long)(r.Right - r.Left) * (r.Bottom - r.Top);
        if (a > area) { area = a; best = h; }
      }
      return true;
    }, IntPtr.Zero);
    return best;
  }
}
'@
$b = [System.Windows.Forms.SystemInformation]::VirtualScreen
$done = $false
if ((Split-Path $Out -Leaf) -notlike 'dialog*') {
  $p = Get-Process -Name 'activate-studio' -ErrorAction SilentlyContinue | Select-Object -First 1
  if ($p) {
    $hwnd = [Win]::Largest([uint32]$p.Id)
    $r = New-Object Win+RECT
    if ($hwnd -ne [IntPtr]::Zero -and [Win]::GetWindowRect($hwnd, [ref]$r)) {
      $w = $r.Right - $r.Left; $h = $r.Bottom - $r.Top
      if ($w -gt 0 -and $h -gt 0) {
        $bmp = New-Object System.Drawing.Bitmap $w, $h
        $g = [System.Drawing.Graphics]::FromImage($bmp)
        $hdc = $g.GetHdc()
        $ok = [Win]::PrintWindow($hwnd, $hdc, 2)
        $g.ReleaseHdc($hdc)
        if ($ok) { $bmp.Save($Out, [System.Drawing.Imaging.ImageFormat]::Png); $done = $true }
        $g.Dispose(); $bmp.Dispose()
      }
    }
  }
}
if (-not $done) {
  $bmp = New-Object System.Drawing.Bitmap $b.Width, $b.Height
  $g = [System.Drawing.Graphics]::FromImage($bmp)
  $g.CopyFromScreen($b.Left, $b.Top, 0, 0, $bmp.Size)
  $bmp.Save($Out, [System.Drawing.Imaging.ImageFormat]::Png)
  $g.Dispose(); $bmp.Dispose()
}
