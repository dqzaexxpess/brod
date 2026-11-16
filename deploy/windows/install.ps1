# Установка Brod на Windows (запускать от администратора из распакованной папки сборки):
#   powershell -ExecutionPolicy Bypass -File .\install.ps1
# Что делает:
#   * копирует файлы в %ProgramFiles%\Brod;
#   * создаёт службу Windows «Brod» (демон brodd.exe от LocalSystem: запускается вместе с системой, без окна и без запроса UAC; ему нужны
#     права на создание туннеля wintun и маршрутов). Запуск службы разрешён всем интерактивным пользователям (приложение само поднимает демон), остановка — только
#     администраторам и пользователю, указанному в -User (по умолчанию тот, кто запустил установку). Служба перезапускается при сбое (через 5, 10 и 60 с);
#   * состояние демона хранится в %ProgramData%\Brod (у службы нет профиля пользователя). Прежнее состояние из %LOCALAPPDATA%\vpnclient
#     переносится при первой установке (прежний каталог переименовывается в vpnclient.migrated, ничего не удаляется);
#   * удаляет прежнюю задачу планировщика «Brod daemon» (если она была), создаёт ярлык в меню «Пуск», запускает службу.
# Файлы не подписаны: SmartScreen/Защитник могут спросить.
#Requires -RunAsAdministrator
# Режим -Update запускает демон-служба Brod при установке обновления из приложения (кнопка «Установить»): от LocalSystem, без UAC, из распакованного
# релиза внутри каталога установки. В этом режиме не трогаются состояние и права на него (под LocalSystem «установивший пользователь» — не тот),
# прежние файлы сохраняются в <каталог>.prev и возвращаются при сбое, итог записывается в -Result (JSON), каталог распаковки удаляется.
# -User <учётная запись> — кому выдать доступ к состоянию (сокет, api-access.json) и право остановки службы. По умолчанию тот, от кого
# запущен скрипт; если установка идёт от учётной записи администратора, отличной от повседневной (ввод пароля в окне UAC), укажите повседневную:
#   powershell -ExecutionPolicy Bypass -File .\install.ps1 -User ИМЯ_КОМПЬЮТЕРА\ivan
# -RestrictPrivileges — урезать привилегии службы до перечня в $privs ниже. Без ключа служба получает полный набор LocalSystem. Перечень
# составлен по коду, не по запуску: если после установки туннель не поднимается или не ставятся фильтры, ключ не использовать (повторная
# установка без него возвращает полный набор).
param([string]$Dir = (Join-Path $env:ProgramFiles 'Brod'), [string]$User = '', [switch]$RestrictPrivileges, [switch]$Update, [string]$Result = '', [string]$From = '', [string]$To = '')
$ErrorActionPreference = 'Stop'
function Write-Result($ok, $err) {
    if (-not $Result) { return }
    $r = [ordered]@{ at = (Get-Date).ToUniversalTime().ToString('o'); from = $From; to = $To; ok = $ok }
    if ($err) { $r.error = $err }
    $tmp = "$Result.tmp"
    [IO.File]::WriteAllText($tmp, ($r | ConvertTo-Json -Compress), (New-Object Text.UTF8Encoding($false)))
    Move-Item -Force -Path $tmp -Destination $Result
}
# Установка прерывается посреди дела (файлы уже скопированы, прежнее остановлено): без этого пользователь остался бы без демона и без объяснения.
# Каждый шаг отката — в своём try: ошибка копирования (файл занят) при $ErrorActionPreference = 'Stop' прерывает обработчик несмотря на
# -ErrorAction SilentlyContinue, и тогда служба оставалась остановленной, а итог не записывался (проверено на Windows 11).
trap {
    $msg = $_.Exception.Message
    if ($Update -and (Test-Path "$Dir.prev")) { # вернуть прежние файлы поверх недокопированных; занятый файл — повтор через 2 с
        $left = @(Get-ChildItem -Path "$Dir.prev" -Force -ErrorAction SilentlyContinue)
        foreach ($attempt in 1, 2) {
            $failed = @()
            foreach ($item in $left) {
                try { Copy-Item -Path $item.FullName -Destination $Dir -Recurse -Force -ErrorAction Stop } catch { $failed += $item }
            }
            $left = $failed
            if ($left.Count -eq 0) { break }
            Start-Sleep -Seconds 2
        }
        if ($left.Count -gt 0) { $msg += "; не вернулись из $Dir.prev: $(($left | ForEach-Object Name) -join ', ')" }
        # файлы, которые принёс новый релиз и которых не было в прежней версии (.prev — полный снимок каталога), убрать
        $prevNames = @(Get-ChildItem -Path "$Dir.prev" -Force -ErrorAction SilentlyContinue | ForEach-Object Name)
        Get-ChildItem -Path $Dir -Force -ErrorAction SilentlyContinue | Where-Object { $_.Name -notlike '.update-*' -and $prevNames -notcontains $_.Name } |
            ForEach-Object { try { Remove-Item -LiteralPath $_.FullName -Recurse -Force -ErrorAction Stop } catch {} }
        try { Start-Service -Name 'Brod' -ErrorAction Stop } catch { $msg += "; служба не запустилась: $($_.Exception.Message)" }
    }
    try { Write-Result $false $msg } catch {}
    Write-Host ''
    Write-Host "УСТАНОВКА НЕ ЗАВЕРШЕНА: $msg" -ForegroundColor Red
    if (Get-Service -Name 'Brod' -ErrorAction SilentlyContinue) {
        Write-Host 'Служба Brod есть, но могла остаться остановленной. Запуск: Start-Service Brod; повторная установка безопасна.'
    } else {
        Write-Host 'Службы Brod нет, демон не работает. Повторите установку: скрипт можно запускать снова, он доделает недостающее.'
    }
    break
}
$src = Split-Path -Parent $MyInvocation.MyCommand.Path
$svc = 'Brod'
$task = 'Brod daemon'
$data = Join-Path $env:ProgramData 'Brod'

# Учётная запись пользователя приложения: имя и SID. В режиме -Update (служба от LocalSystem) её нет и ничего по ней не выдаётся.
if (-not $Update) {
    $user = if ($User) { $User } else { [Security.Principal.WindowsIdentity]::GetCurrent().Name }
    try { $userSid = (New-Object Security.Principal.NTAccount($user)).Translate([Security.Principal.SecurityIdentifier]).Value }
    catch { throw "не удалось определить учётную запись '$user' (параметр -User): $($_.Exception.Message)" }
    Write-Host "Доступ к приложению и остановка службы: $user ($userSid)."
}

foreach ($f in 'brodd.exe', 'Brod.exe', 'wintun.dll') {
    if (-not (Test-Path (Join-Path $src $f))) { throw "В папке сборки нет $f — установка прервана." }
}

# --- остановить всё прежнее ---
if (Get-Service -Name $svc -ErrorAction SilentlyContinue) {
    Stop-Service -Name $svc -Force -ErrorAction SilentlyContinue
    (Get-Service -Name $svc).WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
}
if (Get-ScheduledTask -TaskName $task -ErrorAction SilentlyContinue) {
    Stop-ScheduledTask -TaskName $task -ErrorAction SilentlyContinue
    Unregister-ScheduledTask -TaskName $task -Confirm:$false
}
Get-Process -Name brodd, vpnd, Brod, vpn_app -ErrorAction SilentlyContinue | Stop-Process -Force # vpnd и vpn_app — прежние имена
Start-Sleep -Seconds 1

# --- файлы программы ---
# Права на каталог программы задаются явно, без наследования от %ProgramFiles%: SYSTEM и администраторы — полный доступ, пользователи —
# чтение и запуск. Наследовать нельзя: на части машин у %ProgramFiles% бывает запись для пользователей, а brodd.exe работает от LocalSystem,
# и установка обновления распаковывает релиз (.update-stage) и берёт прежние файлы для отката (.prev) именно здесь.
function Protect-Dir($path) {
    & icacls.exe $path /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' '*S-1-5-32-545:(OI)(CI)RX' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "icacls не смог выставить права на $path" }
}
New-Item -ItemType Directory -Force -Path $Dir | Out-Null
Protect-Dir $Dir
if ($Update) {
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue "$Dir.prev"
    New-Item -ItemType Directory -Force -Path "$Dir.prev" | Out-Null
    Protect-Dir "$Dir.prev"
    Get-ChildItem -Path $Dir -Force | Where-Object { $_.Name -notlike '.update-*' } | Copy-Item -Destination "$Dir.prev" -Recurse -Force
}
if ((Resolve-Path $src).Path -ne (Resolve-Path $Dir).Path) {
    Copy-Item -Path (Join-Path $src '*') -Destination $Dir -Recurse -Force
}
foreach ($old in 'vpn_app.exe', 'vpnd.exe', 'vpnctl.exe') { Remove-Item -Force -ErrorAction SilentlyContinue (Join-Path $Dir $old) } # прежние имена файлов

# --- состояние: %ProgramData%\Brod ---
if ($Update) {
    # обновление с прежней версии: подкаталога run ещё нет, а демон создал бы его сам без прав пользователя. Создаём здесь: он унаследует права
    # старого каталога состояния (там пользователю уже выдано изменение), у разделённого состояния каталог уже есть и не трогается
    $runDir = Join-Path $data 'run'
    if (-not (Test-Path -LiteralPath $runDir)) { New-Item -ItemType Directory -Force -Path $runDir | Out-Null }
}
if (-not $Update) {
$old = Join-Path $env:LOCALAPPDATA 'vpnclient'
$fresh = -not (Test-Path (Join-Path $data 'state.db'))
# Каталог мог быть создан до установки обычным пользователем (в %ProgramData% это разрешено каждому): владелец вернул бы себе права в любой
# момент, а подложенный state.db служба от LocalSystem открыла бы как свой. Чужой владелец или ссылка (junction/symlink) — отказ;
# свой (SYSTEM, администраторы) — владелец закрепляется за администраторами и права сбрасываются до наследуемых у всего содержимого,
# и только потом выдаются заново. Содержимое чужого каталога не проверяется, поэтому в этом случае его надо убрать вручную.
if (Test-Path -LiteralPath $data) {
    $item = Get-Item -LiteralPath $data -Force
    if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) {
        throw "$data — ссылка (junction/symlink), а не каталог. Проверьте, кто её создал, удалите и повторите установку."
    }
    $owner = (Get-Acl -LiteralPath $data).GetOwner([Security.Principal.SecurityIdentifier]).Value
    if ($owner -notin 'S-1-5-18', 'S-1-5-32-544') {
        throw "каталог $data уже существует и принадлежит $owner, а не SYSTEM или администраторам: до установки его создал кто-то из пользователей, и в нём может лежать подложенная база. Установка прервана; осмотрите каталог, удалите его (или перенесите нужное) и повторите."
    }
    & icacls.exe $data /setowner '*S-1-5-32-544' /T /C /L | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "icacls не смог сменить владельца $data" }
    & icacls.exe $data /reset /T /C /L | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "icacls не смог сбросить права $data" }
}
New-Item -ItemType Directory -Force -Path $data | Out-Null
# права: данные службы (база, токен, ключи, журнал, updates) — только SYSTEM и администраторам. Установивший пользователь получает
# чтение самого каталога без наследования (чтобы приложение видело killswitch.active) и изменение подкаталога run, где лежат сокет API и
# api-access.json. Каталог состояния пользователь больше не может ни заменить, ни переименовать, ни подменить в нём файлы.
& icacls.exe $data /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' "*${userSid}:(RX)" | Out-Null
if ($LASTEXITCODE -ne 0) { throw "icacls не смог выставить права на $data" }
$run = Join-Path $data 'run' # то же имя в paths.WindowsRunSubdir и app/lib/src/platform_paths.dart
New-Item -ItemType Directory -Force -Path $run | Out-Null
& icacls.exe $run /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' "*${userSid}:(OI)(CI)M" | Out-Null
if ($LASTEXITCODE -ne 0) { throw "icacls не смог выставить права на $run" }
if ($fresh -and (Test-Path (Join-Path $old 'state.db'))) {
    Get-ChildItem -Path $old -Force | Where-Object { $_.Name -notin 'vpnd.lock', 'vpnd.sock', 'brodd.lock', 'brodd.sock' } |
        Copy-Item -Destination $data -Recurse -Force
    Rename-Item -Path $old -NewName 'vpnclient.migrated' -ErrorAction SilentlyContinue
    Write-Host "Состояние перенесено: $old -> $data (прежний каталог переименован в vpnclient.migrated)."
}
}

# --- служба ---
# Службу создаём командлетами, а не `sc.exe create binPath= ...`: Windows PowerShell 5.1 не экранирует кавычки внутри аргумента внешней
# программы, и путь с пробелом доходил до sc.exe как `binPath= ""C:\Program Files\...""` (ошибка 1639, службы нет). PowerShell 7 это прощает,
# поэтому проверка под pwsh на Linux такой ошибки не ловит.
# без --no-notify: уведомления демон отдаёт приложению Brod (служба в сеансе 0 сама их показывать не может)
$bin = '"' + (Join-Path $Dir 'brodd.exe') + '"'
if (Get-Service -Name $svc -ErrorAction SilentlyContinue) {
    # путь у существующей службы меняем прямо в реестре: так кавычки не проходят через командную строку
    Set-ItemProperty -Path "HKLM:\SYSTEM\CurrentControlSet\Services\$svc" -Name ImagePath -Value $bin
    Set-Service -Name $svc -StartupType Automatic -DisplayName 'Brod VPN'
} else {
    New-Service -Name $svc -BinaryPathName $bin -DisplayName 'Brod VPN' -StartupType Automatic | Out-Null
}
if (-not (Get-Service -Name $svc -ErrorAction SilentlyContinue)) { throw "служба $svc не создана" }
& sc.exe description $svc 'Демон Brod: узлы, проверки, туннель wintun и маршруты.' | Out-Null
& sc.exe failure $svc reset= 86400 actions= restart/5000/restart/10000/restart/60000 | Out-Null
& sc.exe failureflag $svc 1 | Out-Null
# киллсвитч ставит фильтры WFP: при загрузке служба должна стартовать после модуля фильтрации (BFE), иначе правила не поставятся
& sc.exe config $svc depend= BFE | Out-Null
# запуск (RP) — всем интерактивным пользователям (IU): приложение само поднимает демон. Остановка (WP) — только администраторам, SYSTEM и
# пользователю -User по его SID: штатная остановка снимает и туннель, и фильтры киллсвитча. В режиме -Update (под LocalSystem SID
# пользователя неизвестен) права на службу не трогаются, остаются выставленные ручной установкой.
if (-not $Update) {
    & sc.exe sdset $svc "D:(A;;CCLCSWRPWPDTLOCRRC;;;SY)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)(A;;CCLCSWRPWPLOCRRC;;;$userSid)(A;;CCLCSWRPLOCRRC;;;IU)(A;;CCLCSWLOCRRC;;;SU)" | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "sc sdset не выставил права на службу $svc" }
}
# привилегии службы: по умолчанию полный набор LocalSystem; с -RestrictPrivileges — только перечисленные (без SeDebug, SeTcb,
# SeTakeOwnership, SeCreateToken и прочих; SeLoadDriver оставлена для WinDivert). Не проверено на Windows. В режиме -Update не меняется.
if (-not $Update) {
    if ($RestrictPrivileges) {
        $privs = 'SeChangeNotifyPrivilege', 'SeCreateGlobalPrivilege', 'SeImpersonatePrivilege', 'SeIncreaseQuotaPrivilege', 'SeLoadDriverPrivilege', 'SeBackupPrivilege', 'SeRestorePrivilege'
        & sc.exe privs $svc ($privs -join '/') | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "sc privs не выставил привилегии службы $svc" }
        Write-Host "Привилегии службы урезаны: $($privs -join ', '). Если туннель или киллсвитч не работают, повторите установку без -RestrictPrivileges."
    } else {
        # сброс через диспетчер служб (пустой перечень), а не удалением значения RequiredPrivileges из реестра: диспетчер держит настройки
        # в памяти и после такого удаления видел пустой перечень — служба не запускалась («Недопустимые данные», 13) до пересоздания или
        # перезагрузки (проверено на Windows 11). Пустой аргумент: в Windows PowerShell 5.1 и PowerShell до 7.3 — '""', в 7.3+ — ''.
        $empty = if ($PSVersionTable.PSVersion -ge [version]'7.3') { '' } else { '""' }
        & sc.exe privs $svc $empty | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "sc privs не сбросил привилегии службы $svc" }
    }
}

# --- ярлык ---
$lnk = Join-Path ([Environment]::GetFolderPath('CommonPrograms')) 'Brod.lnk'
$sh = New-Object -ComObject WScript.Shell
$s = $sh.CreateShortcut($lnk)
$s.TargetPath = Join-Path $Dir 'Brod.exe'
$s.WorkingDirectory = $Dir
$s.Save()

Start-Service -Name $svc
if ($Update) {
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $src # каталог распаковки внутри каталога установки
    Write-Result $true ''
}
Write-Host "Готово: $Dir. Служба «$svc» запущена, состояние — $data, ярлык Brod — в меню «Пуск»."
