# Closes the shell's folder dialog as a person pressing Escape would.
$ws = New-Object -ComObject WScript.Shell
for ($i = 0; $i -lt 40; $i++) {
  if ($ws.AppActivate('Choose the folder')) {
    Start-Sleep -Milliseconds 300
    $ws.SendKeys('{ESC}')
    Write-Output "closed after $i tries"
    exit 0
  }
  Start-Sleep -Milliseconds 250
}
Write-Output 'no dialog window found'
exit 1
