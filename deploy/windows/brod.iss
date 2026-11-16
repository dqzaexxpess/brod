; Установщик Brod для Windows (Inno Setup 6). Сам ничего не настраивает: копирует файлы сборки и запускает install.ps1
; (служба, права на каталоги, ярлык), а при удалении запускает uninstall.ps1.
;
; Сборка: ISCC.exe /DAppVersion=0.3.0 /DSourceDir=<каталог сборки> /O<куда положить> deploy\windows\brod.iss
; Каталог сборки — тот же, что собирает CI (Brod.exe, brodd.exe, brodctl.exe, wintun.dll, install.ps1, uninstall.ps1).
; Тихая установка: brod-<версия>-windows-amd64-setup.exe /VERYSILENT /USER=ДОМЕН\Имя
;
; Файл сохранён в UTF-8 с BOM: иначе русский текст в мастере искажается.

#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif
#ifndef SourceDir
  #define SourceDir "..\..\dist\brod"
#endif

[Setup]
AppId={{6F3C1B7E-92A4-4D58-A0E1-5B9D7C2F4A11}
AppName=Brod
AppVersion={#AppVersion}
AppPublisher=Brod
DefaultDirName={autopf}\Brod
DisableProgramGroupPage=yes
DisableDirPage=yes
DisableReadyPage=no
PrivilegesRequired=admin
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0
OutputBaseFilename=brod-{#AppVersion}-windows-amd64-setup
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
SetupIconFile=..\..\app\windows\runner\resources\app_icon.ico
UninstallDisplayIcon={app}\Brod.exe
UninstallDisplayName=Brod
CloseApplications=no
RestartApplications=no

[Languages]
Name: "russian"; MessagesFile: "compiler:Languages\Russian.isl"

[Files]
Source: "{#SourceDir}\*"; DestDir: "{app}"; Flags: recursesubdirs createallsubdirs ignoreversion

[Run]
; приложение запускается от имени того, кто открыл установщик, а не от администратора
Filename: "{app}\Brod.exe"; Description: "Запустить Brod"; Flags: nowait postinstall skipifsilent runasoriginaluser

[Code]
var
  UserPage: TInputQueryWizardPage;

function DefaultBrodUser(): String;
begin
  Result := GetEnv('USERDOMAIN') + '\' + GetEnv('USERNAME');
end;

function BrodUser(): String;
begin
  Result := ExpandConstant('{param:USER|}');
  if Result = '' then
    Result := Trim(UserPage.Values[0]);
  if Result = '' then
    Result := DefaultBrodUser();
end;

procedure InitializeWizard();
begin
  UserPage := CreateInputQueryPage(wpWelcome,
    'Кто будет пользоваться Brod',
    'Учётная запись для доступа к службе',
    'Служба Brod работает от имени системы. Доступ к ней и право её остановить получает одна учётная запись.' + #13#10 +
    'Обычно это ваша. Если установщик запущен от другого администратора, впишите свою учётную запись (ДОМЕН\Имя).');
  UserPage.Add('Учётная запись:', False);
  UserPage.Values[0] := ExpandConstant('{param:USER|}');
  if UserPage.Values[0] = '' then
    UserPage.Values[0] := DefaultBrodUser();
end;

function LogTail(const Path: String): String;
var
  Lines: TArrayOfString;
  I, First: Integer;
begin
  Result := '';
  if LoadStringsFromFile(Path, Lines) then
  begin
    First := GetArrayLength(Lines) - 12;
    if First < 0 then First := 0;
    for I := First to GetArrayLength(Lines) - 1 do
      Result := Result + Lines[I] + #13#10;
  end;
end;

procedure ExitProcess(ExitCode: Cardinal);
  external 'ExitProcess@kernel32.dll stdcall';

const
  ExitServiceFailed = 10;

function PowerShell(const Command: String; var Code: Integer): Boolean;
begin
  Result := Exec('powershell.exe', '-NoProfile -ExecutionPolicy Bypass -Command "' + Command + '"', '', SW_HIDE, ewWaitUntilTerminated, Code);
end;

// Учётная запись проверяется до копирования файлов: после копирования откатить установку нечем, а код возврата
// Inno Setup из CurStepChanged не меняется.
function UserIsValid(const Account: String): Boolean;
var
  Code: Integer;
begin
  Result := False;
  if (Pos('''', Account) > 0) or (Pos('"', Account) > 0) then Exit;
  Result := PowerShell('try { (New-Object Security.Principal.NTAccount(''' + Account +
    ''')).Translate([Security.Principal.SecurityIdentifier]) | Out-Null; exit 0 } catch { exit 1 }', Code) and (Code = 0);
end;

function NextButtonClick(CurPageID: Integer): Boolean;
begin
  Result := True;
  if CurPageID = UserPage.ID then
    if not UserIsValid(BrodUser()) then
    begin
      MsgBox('Не удалось найти учётную запись «' + BrodUser() + '». Укажите её в виде ДОМЕН\Имя или ИМЯ_КОМПЬЮТЕРА\Имя.', mbError, MB_OK);
      Result := False;
    end;
end;

// Служба и приложение держат свои файлы: без остановки замена brodd.exe и Brod.exe падает с «отказано в доступе».
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  Code: Integer;
begin
  Result := '';
  if not UserIsValid(BrodUser()) then
  begin
    Result := 'Не удалось найти учётную запись «' + BrodUser() + '» (параметр /USER=ДОМЕН\Имя). Установка не начата.';
    Exit;
  end;
  Log('Остановка службы Brod и приложения перед копированием файлов');
  PowerShell('$s = Get-Service Brod -ErrorAction SilentlyContinue; ' +
    'if ($s) { Stop-Service Brod -Force -ErrorAction SilentlyContinue; try { $s.WaitForStatus(''Stopped'', ''00:00:30'') } catch {} }; ' +
    'Get-Process Brod, brodd -ErrorAction SilentlyContinue | Stop-Process -Force; Start-Sleep 1', Code);
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  Code: Integer;
  LogFile, Cmd, Msg: String;
begin
  if CurStep = ssPostInstall then
  begin
    LogFile := ExpandConstant('{tmp}\brod-install.log');
    // chcp 1251: вывод PowerShell читается Inno Setup как ANSI, а консоль по умолчанию отдаёт OEM 866 (кириллица превращалась в мусор)
    Cmd := '/C chcp 1251 > nul & powershell.exe -NoProfile -ExecutionPolicy Bypass -File "' + ExpandConstant('{app}\install.ps1') +
           '" -Dir "' + ExpandConstant('{app}') + '" -User "' + BrodUser() + '" > "' + LogFile + '" 2>&1';
    Log('Запуск install.ps1');
    if (not Exec(ExpandConstant('{cmd}'), Cmd, '', SW_HIDE, ewWaitUntilTerminated, Code)) or (Code <> 0) then
    begin
      Msg := 'Не удалось установить службу Brod (код ' + IntToStr(Code) + ').' + #13#10#13#10 + LogTail(LogFile) + #13#10 +
        'Повторите установку или запустите install.ps1 вручную из ' + ExpandConstant('{app}') + '.';
      Log(Msg);
      // служба могла остаться остановленной после замены файлов
      PowerShell('Start-Service Brod -ErrorAction SilentlyContinue', Code);
      if not WizardSilent then MsgBox(Msg, mbError, MB_OK);
      // Abort здесь завершил бы установку с кодом 0: скрипты развёртывания приняли бы её за успешную
      ExitProcess(ExitServiceFailed);
    end;
  end;
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  Code: Integer;
  Args: String;
begin
  if CurUninstallStep = usUninstall then
  begin
    Args := '-NoProfile -ExecutionPolicy Bypass -File "' + ExpandConstant('{app}\uninstall.ps1') + '" -Dir "' + ExpandConstant('{app}') + '"';
    if SuppressibleMsgBox('Удалить также базу узлов, настройки и токены?' + #13#10 +
         'Если нет, они сохранятся и пригодятся при повторной установке.', mbConfirmation, MB_YESNO or MB_DEFBUTTON2, IDNO) = IDYES then
      Args := Args + ' -RemoveState';
    Exec('powershell.exe', Args, '', SW_HIDE, ewWaitUntilTerminated, Code);
  end;
end;
