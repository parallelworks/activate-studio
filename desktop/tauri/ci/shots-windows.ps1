# Screenshots on request for the smoke test (.req -> .png, then .done); for
# the folder dialog, Escape afterwards, which cancels it.
param([string]$Out)
Add-Type -AssemblyName System.Windows.Forms, System.Drawing
$shell = New-Object -ComObject WScript.Shell
Add-Type -Namespace Native -Name Mouse -MemberDefinition @'
[DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
[DllImport("user32.dll")] public static extern void mouse_event(uint flags, uint dx, uint dy, uint data, UIntPtr extra);
'@
$log = Join-Path $Out 'poller.log'
function Shot([string]$path) {
  $b = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
  $bmp = New-Object System.Drawing.Bitmap $b.Width, $b.Height
  $g = [System.Drawing.Graphics]::FromImage($bmp)
  $g.CopyFromScreen($b.Location, [System.Drawing.Point]::Empty, $b.Size)
  $bmp.Save($path, [System.Drawing.Imaging.ImageFormat]::Png)
  $g.Dispose(); $bmp.Dispose()
}
while ($true) {
  foreach ($req in Get-ChildItem -Path (Join-Path $Out 'shots') -Filter '*.req' -ErrorAction SilentlyContinue) {
    $name = [System.IO.Path]::GetFileNameWithoutExtension($req.Name)
    if ($name -notlike 'dialog-*') {
      # The default browser, opened by the outside-link checks, takes the focus.
      $null = $shell.AppActivate('ACTIVATE Studio')
      Start-Sleep -Milliseconds 400
    }
    try { Shot (Join-Path $Out "shots\$name.png") } catch { Add-Content $log "shot $name failed: $_" }
    if ($name -like 'click*') {
      # A person's click in the middle of the screen (the window fills it).
      $b = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
      [Native.Mouse]::SetCursorPos([int]($b.Width / 2), [int]($b.Height / 2)) | Out-Null
      Start-Sleep -Milliseconds 300
      [Native.Mouse]::mouse_event(0x0002, 0, 0, 0, [UIntPtr]::Zero)
      [Native.Mouse]::mouse_event(0x0004, 0, 0, 0, [UIntPtr]::Zero)
      Add-Content $log "$(Get-Date -Format T) clicked for $name"
    }
    if ($name -like 'dialog-*') {
      $ok = $shell.AppActivate('Smoke test folder dialog')
      Add-Content $log "$(Get-Date -Format T) AppActivate dialog: $ok"
      Start-Sleep -Milliseconds 300
      [System.Windows.Forms.SendKeys]::SendWait('{ESC}')
    }
    Move-Item $req.FullName (Join-Path $Out "shots\$name.taken") -Force
    New-Item -ItemType File -Path (Join-Path $Out "shots\$name.done") -Force | Out-Null
    Add-Content $log "$(Get-Date -Format T) took $name"
  }
  Start-Sleep -Milliseconds 200
}
