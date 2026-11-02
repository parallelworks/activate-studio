# Screenshots on request for the smoke test (.req -> .png, then .done); for
# the folder dialog, Escape afterwards, which cancels it.
param([string]$Out)
Add-Type -AssemblyName System.Windows.Forms, System.Drawing
$shell = New-Object -ComObject WScript.Shell
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
