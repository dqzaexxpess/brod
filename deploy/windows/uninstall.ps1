# Удаление Brod (запускать от администратора): останавливает и удаляет службу «Brod» (и прежнюю задачу «Brod daemon»), ярлык и каталог программы.
# Состояние (%ProgramData%\Brod: база узлов, настройки) и настройки приложения (%APPDATA%\Brod) остаются; для их удаления добавьте -RemoveState.
#Requires -RunAsAdministrator
param([string]$Dir = (Join-Path $env:ProgramFiles 'Brod'), [switch]$RemoveState)
$ErrorActionPreference = 'Continue'
if (Get-Service -Name 'Brod' -ErrorAction SilentlyContinue) {
    Stop-Service -Name 'Brod' -Force -ErrorAction SilentlyContinue
    (Get-Service -Name 'Brod').WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
    & sc.exe delete 'Brod' | Out-Null
}
Stop-ScheduledTask -TaskName 'Brod daemon' -ErrorAction SilentlyContinue
Unregister-ScheduledTask -TaskName 'Brod daemon' -Confirm:$false -ErrorAction SilentlyContinue
# окно программы удаления работает из временной копии вне каталога установки; убиваем только процессы из каталога установки
Get-Process -Name brodd, vpnd, Brod, vpn_app -ErrorAction SilentlyContinue |
    Where-Object { -not $_.Path -or $_.Path.StartsWith($Dir.TrimEnd('\') + '\', [StringComparison]::OrdinalIgnoreCase) } |
    Stop-Process -Force # vpnd и vpn_app — прежние имена
Start-Sleep -Seconds 1
# правила киллсвитча (фильтры WFP) штатная остановка снимает сама; если демон до этого упал, они остались бы до перезагрузки
$ctl = Join-Path $Dir 'brodctl.exe'
if (Test-Path $ctl) { & $ctl killswitch release | Out-Null }
Remove-Item -Force -ErrorAction SilentlyContinue (Join-Path ([Environment]::GetFolderPath('CommonPrograms')) 'Brod.lnk')
Remove-Item -Force -ErrorAction SilentlyContinue 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\Brod', 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\{6F3C1B7E-92A4-4D58-A0E1-5B9D7C2F4A11}_is1'
if (Test-Path $Dir) { Remove-Item -Recurse -Force $Dir }
$data = Join-Path $env:ProgramData 'Brod'
if ($RemoveState) {
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $data
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue (Join-Path $env:APPDATA 'Brod')
    Write-Host 'Brod удалён вместе с состоянием.'
} else {
    Write-Host "Brod удалён. Состояние в $data сохранено (удалить: -RemoveState)."
}
