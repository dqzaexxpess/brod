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

procedure CurStepChanged(CurStep: TSetupStep);
var
  Code: Integer;
  Log, Cmd: String;
begin
  if CurStep = ssPostInstall then
  begin
    Log := ExpandConstant('{tmp}\brod-install.log');
    Cmd := '/C powershell.exe -NoProfile -ExecutionPolicy Bypass -File "' + ExpandConstant('{app}\install.ps1') +
           '" -Dir "' + ExpandConstant('{app}') + '" -User "' + BrodUser() + '" > "' + Log + '" 2>&1';
    if (not Exec(ExpandConstant('{cmd}'), Cmd, '', SW_HIDE, ewWaitUntilTerminated, Code)) or (Code <> 0) then
    begin
      SuppressibleMsgBox('Не удалось установить службу Brod (код ' + IntToStr(Code) + ').' + #13#10#13#10 + LogTail(Log) + #13#10 +
        'Повторите установку или запустите install.ps1 вручную из ' + ExpandConstant('{app}') + '.', mbError, MB_OK, IDOK);
      Abort;
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
