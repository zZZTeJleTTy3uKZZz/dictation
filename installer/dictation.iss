; Версия продукта. Её же передаёт scripts/build-dictation.ps1 через
; /DAppVersion: одна версия у установщика, у пакета и у программы
; (internal/localdict/app.go). Расхождение ловят и сборка, и публикация.
#ifndef AppVersion
  #define AppVersion "1.13.3"
#endif
; Сколько установщик ждёт конца идущей записи, прежде чем отказаться ставить.
; Десять минут — предел самой программы по умолчанию (localdict.DefaultStopWait):
; установка не должна откладываться на часы, а фраза — обрываться на полуслове.
#ifndef StopWaitSeconds
  #define StopWaitSeconds 600
#endif
; Один AppId на все установки: местной модели больше нет, и отдельной личности
; «полной версии» с моделью тоже.
#define AppIdentity "{{A2B191A4-742E-42A2-850A-2465006E2C31}"
#define RunKey "Software\Microsoft\Windows\CurrentVersion\Run"
#define RunValue "Dictation"

[Setup]
AppId={#AppIdentity}
AppName=Диктовка
AppVersion={#AppVersion}
AppPublisher=Dictation
; Режим установки выбирает человек: «для всех» (нужны права администратора и
; будет служба Windows) или «только для меня» (автозапуск в сеансе). Значение
; lowest — умолчание, а не единственный режим: без /ALLUSERS установка тихая и
; ставится в профиль позвавшего, как и раньше. {autopf} сам превращается в
; Program Files для «всех» и в %LOCALAPPDATA%\Programs для одного человека.
DefaultDirName={autopf}\Dictation
DefaultGroupName=Диктовка
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog commandline
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
OutputDir=..\build\release
OutputBaseFilename=Dictation-Setup-{#AppVersion}
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
SetupIconFile=..\cmd\dictation\icon.ico
UninstallDisplayIcon={app}\dictation.exe
// Программу останавливаем сами, перед заменой файлов. Диспетчер перезапуска
// Windows видит только программы своей сессии: когда установку запускают
// издалека (по SSH, из другой сессии), он находит занятый dictation.exe, помочь
// не может и оставляет установщик ждать нажатия — молча, без окна.
CloseApplications=no
RestartApplications=no
// Установка не перезагружает машину: /RELEASEIMESPACE=1 просит выйти и войти в
// Windows, но перезагрузку не требует, а тихая выкатка идёт с /NORESTART.
RestartIfNeededByRun=no
DisableProgramGroupPage=yes
WizardImageFile=
WizardSmallImageFile=
[Languages]
Name: "russian"; MessagesFile: "compiler:Languages\Russian.isl"
[Tasks]
Name: "desktopicon"; Description: "Создать ярлык на рабочем столе"; Flags: unchecked
[Files]
Source: "..\build\dictation-package-{#AppVersion}\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs
; Новая сборка нужна ещё до установки: спросить у работающей диктовки, идёт ли
; запись, умеет только она (у прежней ключа --status нет). Файл распаковывается
; во временную папку и оттуда не переезжает.
Source: "..\build\dictation-package-{#AppVersion}\dictation.exe"; DestDir: "{tmp}"; Flags: dontcopy
[InstallDelete]
; Прежние выпуски оставляли рядом с программой то, чего в пакете больше нет:
; движок местного распознавания (onnxruntime, sherpa, dictation-runner) и ручные
; копии «.before-…». Без уборки папка программы весит 188 МБ вместо ~37 МБ, и
; человек видит старые движки, которых в диктовке уже нет.
Type: files; Name: "{app}\onnxruntime.dll"
Type: files; Name: "{app}\sherpa*-*.dll"
Type: files; Name: "{app}\dictation-runner.exe"
Type: filesandordirs; Name: "{app}\models"
Type: filesandordirs; Name: "{app}\model"
Type: files; Name: "{app}\*.before-*"
Type: filesandordirs; Name: "{app}\data.before-*"
[Icons]
Name: "{group}\Диктовка"; Filename: "{app}\dictation.exe"; Parameters: "--settings"
Name: "{autodesktop}\Диктовка"; Filename: "{app}\dictation.exe"; Parameters: "--settings"; Tasks: desktopicon
[Registry]
; Автозапуск в сеансе человека — только когда установка идёт «только для меня».
; При установке «для всех» половины поднимает служба, и запись в Run была бы
; вторым способом запуска: две копии дерутся за микрофон и сочетание клавиш.
; Тихая выкатка на устройства владельца идёт с /ALLUSERS и эту запись не создаёт.
Root: HKCU; Subkey: "{#RunKey}"; ValueType: string; ValueName: "{#RunValue}"; ValueData: """{app}\dictation.exe"" --supervise"; Flags: uninsdeletevalue; Check: IsCurrentUserInstall
[UninstallDelete]
; За собой установщик убирает и то, чего сам не клал: копии прежних сборок
; (.before-…), отчёты лекаря и прочие следы работы рядом с программой.
Type: filesandordirs; Name: "{app}"
[Code]
// РЕЖИМЫ И ЧЕСТНЫЙ ОТКАЗ (требование владельца).
//
// Прежде установщик всегда писал HKCU Run и всегда звал --install-service. Без
// прав администратора второй вызов молча падал, и человек не знал, работает у
// него служба или автозапуск. Теперь режим выбирается до установки (диалог
// «для всех / только для меня»), а в тихом режиме — ключами /ALLUSERS и
// /CURRENTUSER:
//
//   * для всех (есть права администратора) — служба Windows, она поднимает
//     сеансовую половину со значком в трее; HKCU Run не пишется;
//   * только для меня — HKCU Run (dictation.exe --supervise), служба не
//     ставится и о неудаче никто не умалчивает.
//
// Если службу поставить не удалось (редко, но бывает на урезанной машине),
// установщик пишет причину в журнал, честно говорит человеку и оставляет
// автозапуск в сеансе — диктовка не остаётся без способа запуска.
//
// ТИХАЯ УСТАНОВКА ИЗ СКРИПТА (без окон и вопросов):
//
//   Dictation-Setup-X.Y.Z.exe /VERYSILENT /SUPPRESSMSGBOXES /NORESTART
//     /ALLUSERS
//     /CONFIG="<файл со ссылкой провайдера>"
//     /RELEASEIMESPACE=1
//     /LOG="<файл журнала>"
//
// /CONFIG — путь к файлу, в котором ОДНОЙ строкой лежит ссылка-конфигурация
// провайдера (dictation://provider?…, формат и разбор — internal/providercfg).
// Ключ приходит только из файла, не из командной строки; файл после
// применения не удаляется; содержимое файла в журнал не пишется — установщик
// передаёт программе только путь. Строку о том, что ключ применён, журнал
// получает без самого ключа.
//
// КОДЫ ВОЗВРАТА УСТАНОВЩИКА:
//   0  — установка прошла (в том числе обновление);
//   3  — установка отменена до замены файлов: идёт запись (см.
//        RefuseWhileRecording), причина — строкой в /LOG;
//   11 — программа установлена, но /CONFIG не применён (файла нет, он пуст или
//        ссылка не разобралась). Причина — в журнале, ключ туда не попадает;
//   остальные — штатные коды Inno Setup (2 — отказ человека до установки,
//   5 — отмена во время установки, 1/4 — сбой установщика).
//
// /LOG="путь" — штатный журнал Inno Setup: шаги установки, решение о режиме,
// отказы. Секретов в нём нет.
//
// МЕСТНЫХ МОДЕЛЕЙ И СКАЧИВАНИЯ В УСТАНОВЩИКЕ НЕТ ВОВСЕ.
//
// Прежде здесь скачивалась модель GigaAM на 319 МБ: установка требовала
// интернета, на медленной сети висела минутами, а без неё не завершалась.
// Решение владельца — диктовка разбирается только облаком (Groq напрямую или
// свой провайдер), поэтому установка и первый запуск не скачивают ничего.
//
// РАБОТАЮЩАЯ ДИКТОВКА И ЗАПИСЬ. Windows не даёт перезаписать
// работающий exe, поэтому программу просят выйти. 11.11.2026 установка сделала
// это посреди диктовки владельца, и запись оборвалась: прежний --quit не ждал
// конца разговора, а следом все процессы гасились силой.
//
// Теперь установщик:
//   1. спрашивает --status новой сборкой (её же кладёт в {tmp}, key dontcopy);
//   2. если идёт запись — ждёт её конца не дольше StopWaitSeconds, ничего не
//      убивая, и по истечении отказывается ставить с понятной причиной;
//   3. только когда записи нет — просит программу выйти и гасит остатки,
//      которые держат файлы.
// В тихом режиме причина отказа уходит в лог установщика (/LOG), а установка
// завершается кодом 3.

var
  // ConfigFailed — /CONFIG передан, но применить его не удалось. Установку не
  // отменяем (файлы уже на месте), но выходим ненулевым кодом: тихая выкатка
  // обязана узнать, что провайдер не настроен.
  ConfigFailed: Boolean;

// IsCurrentUserInstall — установка «только для меня». Именно она пишет HKCU Run;
// при установке «для всех» автозапуск даёт служба.
function IsCurrentUserInstall: Boolean;
begin
  Result := not IsAdminInstallMode;
end;

// GetCustomSetupExitCode — код возврата после успешной установки. Штатные коды
// Inno (2, 3, 5) остаются за отказами и сбоями; 11 значит «файлы поставлены,
// конфигурация — нет».
function GetCustomSetupExitCode: Integer;
begin
  if ConfigFailed then
    Result := 11
  else
    Result := 0;
end;

// DictationStatus — код возврата `dictation.exe --status`: 0 — записи нет,
// 3 — идёт запись, иное — ответа нет (прежняя сборка, поломка).
function DictationStatus(Exe: String): Integer;
var
  Code: Integer;
begin
  Code := -1;
  if not FileExists(Exe) then
  begin
    Result := -1;
    exit;
  end;
  Exec(Exe, '--status', '', SW_HIDE, ewWaitUntilTerminated, Code);
  Result := Code;
end;

// RefuseWhileRecording — отказ ставить при идущей записи. Ничего не убиваем.
// Abort до замены файлов даёт код 3; причина уже лежит в журнале.
procedure RefuseWhileRecording;
begin
  Log('Dictation is recording: setup cancelled before any file was replaced (exit code 3)');
  if not WizardSilent then
    MsgBox('Диктовка сейчас записывает разговор, поэтому обновление отменено: ' +
      'обрывать запись нельзя. Закончите диктовку и повторите установку.',
      mbError, MB_OK);
  Abort;
end;

// WaitWhileRecording ждёт конца идущей записи, но не дольше StopWaitSeconds.
procedure WaitWhileRecording(StatusExe: String);
var
  Waited, Status: Integer;
begin
  Waited := 0;
  while True do
  begin
    Status := DictationStatus(StatusExe);
    if Status <> 3 then
    begin
      Log('Dictation is not recording (status=' + IntToStr(Status) +
        '), waited ' + IntToStr(Waited) + ' s');
      exit;
    end;
    if Waited >= {#StopWaitSeconds} then
    begin
      Log('Recording did not finish in ' + IntToStr({#StopWaitSeconds}) +
        ' s: setup refuses to continue');
      RefuseWhileRecording;
    end;
    Sleep(5000);
    Waited := Waited + 5;
  end;
end;

procedure StopRunningDictation;
var
  Code: Integer;
  StatusExe: String;
begin
  if not FileExists(ExpandConstant('{app}\dictation.exe')) then
    exit;
  // Спрашиваем НОВУЮ сборку: у прежней нет --status, и её --quit оборвал бы
  // запись. Заодно новый ответ понимает и старую диктовку (она отдаёт стадию
  // в /state) — поэтому обновление с прежней версии тоже проходит через
  // проверку записи.
  StatusExe := ExpandConstant('{tmp}\dictation.exe');
  try
    ExtractTemporaryFile('dictation.exe');
  except
    // Без новой сборки спросить нечем. Прежняя ключа --status не знает и на
    // него просто запустилась бы — то есть проверка превратилась бы в «ждать
    // вечно». Поэтому честно пропускаем её и пишем это в журнал; дальше
    // работают прежние шаги (просьба выйти и гашение остатков).
    Log('New build is not available for the status check: ' + GetExceptionMessage);
    StatusExe := '';
  end;
  if StatusExe <> '' then
    WaitWhileRecording(StatusExe)
  else
    Log('Recording check skipped: no new build to ask');
  Exec(ExpandConstant('{app}\dictation.exe'),
    '--quit --wait ' + IntToStr({#StopWaitSeconds}), '', SW_HIDE, ewWaitUntilTerminated, Code);
  Log('Asked the running dictation to quit, code=' + IntToStr(Code));
  // Запись могла начаться между проверкой и просьбой — тогда программа
  // отказывает тем же кодом 3, и обновление откладывается.
  if Code = 3 then
    RefuseWhileRecording;
  Sleep(2000);
  Exec(ExpandConstant('{sys}\taskkill.exe'), '/IM dictation.exe /F /T', '', SW_HIDE, ewWaitUntilTerminated, Code);
  Sleep(500);
  // The window and the recogniser hold their own files open. Left running they
  // turn an upgrade into "DeleteFile failed; code 5" and a rollback: the window
  // is started by hand or by the screenshot helper and outlives the daemon.
  Exec(ExpandConstant('{sys}\taskkill.exe'), '/IM dictation-ui.exe /F /T', '', SW_HIDE, ewWaitUntilTerminated, Code);
  // Compression of a finished recording runs for seconds and holds its own
  // .exe in {app} open; the daemon that started it is already gone by now, so
  // "/T" no longer reaches it.
  Exec(ExpandConstant('{sys}\taskkill.exe'), '/IM audio-compress.exe /F /T', '', SW_HIDE, ewWaitUntilTerminated, Code);
  Sleep(500);
end;

// ApplyConfigFile — тихая настройка провайдера из /CONFIG.
//
// Ключ берётся ТОЛЬКО из файла: установщик передаёт программе путь и больше
// ничего. Сам файл не удаляется — человеку и выкатке он ещё пригодится, — а его
// содержимое не попадает ни в журнал, ни в сообщения.
procedure ApplyConfigFile;
var
  Path, Exe: String;
  Code: Integer;
  Content: AnsiString;
begin
  Path := ExpandConstant('{param:CONFIG|}');
  if Path = '' then
    exit;
  Log('Provider configuration: applying the link from ' + Path);
  if not FileExists(Path) then
  begin
    ConfigFailed := True;
    Log('CONFIG file not found: ' + Path);
    if not WizardSilent then
      MsgBox('Файл конфигурации не найден: ' + Path, mbError, MB_OK);
    exit;
  end;
  if not LoadStringFromFile(Path, Content) then
  begin
    ConfigFailed := True;
    Log('CONFIG file cannot be read: ' + Path);
    if not WizardSilent then
      MsgBox('Файл конфигурации не читается: ' + Path, mbError, MB_OK);
    exit;
  end;
  if Trim(Content) = '' then
  begin
    ConfigFailed := True;
    Log('CONFIG file is empty: ' + Path);
    if not WizardSilent then
      MsgBox('Файл конфигурации пуст: ' + Path, mbError, MB_OK);
    exit;
  end;
  Exe := ExpandConstant('{app}\dictation.exe');
  Exec(Exe, '--apply-config "' + Path + '"', '', SW_HIDE, ewWaitUntilTerminated, Code);
  if Code <> 0 then
  begin
    ConfigFailed := True;
    Log('Provider configuration was not applied, code=' + IntToStr(Code) +
      ' (the file and its key are not written to this log)');
    if not WizardSilent then
      MsgBox('Провайдер не настроен: программа не приняла ссылку из файла ' +
        Path + '. Подробности — в журнале установки.', mbError, MB_OK);
    exit;
  end;
  Log('Provider configuration applied; the key is not written to this log');
end;

// InstallServiceOrAutostart — единственный способ запуска после установки.
//
// «Для всех» — служба Windows (она же поднимает значок в сеансе человека).
// «Только для меня» — запись HKCU Run, и --install-service не зовётся вовсе:
// молчаливый отказ прежней сборки выглядел как поломка диктовки.
procedure InstallServiceOrAutostart;
var
  Exe: String;
  Code: Integer;
begin
  Exe := ExpandConstant('{app}\dictation.exe');
  if not IsAdminInstallMode then
  begin
    Log('Install mode: current user only. Autostart: HKCU Run (--supervise). ' +
      'The Windows service is not installed in this mode.');
    exit;
  end;
  Log('Install mode: all users. Installing the Windows service');
  Exec(Exe, '--install-service', '', SW_HIDE, ewWaitUntilTerminated, Code);
  if Code = 0 then
  begin
    Log('Service installed and started; HKCU/HKLM Run entries are not needed');
    RegDeleteValue(HKCU, '{#RunKey}', '{#RunValue}');
    RegDeleteValue(HKEY_LOCAL_MACHINE, '{#RunKey}', '{#RunValue}');
    exit;
  end;
  // Службу поставить не удалось. Молчать нельзя: человек должен знать, каким
  // способом диктовка будет подниматься. Автозапуск в сеансе — запасной ход,
  // который работает и без службы.
  Log('Service was not installed, code=' + IntToStr(Code) +
    '; falling back to autostart in the user session');
  RegWriteStringValue(HKCU, '{#RunKey}', '{#RunValue}', '"' + Exe + '" --supervise');
  if not WizardSilent then
    MsgBox('Службу поставить не удалось (код ' + IntToStr(Code) + ').' + #13#10 +
      'Диктовка будет запускаться при входе в Windows для текущего человека.',
      mbError, MB_OK);
end;

// WantsReleaseIMESpace — установка с ключом «освободить системный Ctrl+Space».
//
// Тихая выкатка на устройства владельца передаёт /RELEASEIMESPACE=1: тогда
// системное переключение ввода отпускает Ctrl+Space, и после входа в Windows
// Диктовка занимает его обычной регистрацией. Без ключа ничего не трогаем.
function WantsReleaseIMESpace: Boolean;
var
  Value: String;
begin
  Value := LowerCase(ExpandConstant('{param:RELEASEIMESPACE|0}'));
  Result := (Value = '1') or (Value = 'yes') or (Value = 'true');
end;

procedure ReleaseIMESpace;
var
  Code: Integer;
begin
  if not WantsReleaseIMESpace then
    exit;
  Exec(ExpandConstant('{app}\dictation.exe'), '--release-ime-hotkey', '',
    SW_HIDE, ewWaitUntilTerminated, Code);
  Log('Released the system Ctrl+Space, code=' + IntToStr(Code));
  if not WizardSilent then
    MsgBox('Ctrl+Space освобождён у системного переключения ввода.' + #13#10 +
      'Чтобы он заработал, выйдите и войдите в Windows: перезагрузка не нужна.',
      mbInformation, MB_OK);
end;

// RemoveOtherUninstallEntry — у программы одна запись в «Программах и
// компонентах».
//
// 17.11.2026 после установки «для всех» осталась прежняя запись HKCU 1.11.1, и
// в списке оказались две строки с одним AppId. Убираем запись другого режима:
// администратору — HKCU, обычному человеку — HKLM, если прав хватает.
procedure RemoveOtherUninstallEntry;
var
  Key: String;
begin
  Key := 'Software\Microsoft\Windows\CurrentVersion\Uninstall\{A2B191A4-742E-42A2-850A-2465006E2C31}_is1';
  if IsAdminInstallMode then
  begin
    if RegDeleteKeyIncludingSubkeys(HKCU, Key) then
      Log('Removed the per-user uninstall entry of the same program')
    else
      Log('Per-user uninstall entry is absent or was not removed');
  end
  else if IsAdmin then
  begin
    if RegDeleteKeyIncludingSubkeys(HKEY_LOCAL_MACHINE, Key) then
      Log('Removed the all-users uninstall entry of the same program')
    else
      Log('All-users uninstall entry is absent or was not removed');
  end
  else
    Log('All-users uninstall entry may remain: removing it needs administrator rights');
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  if CurStep = ssInstall then
    StopRunningDictation;
  if CurStep = ssPostInstall then
  begin
    RemoveOtherUninstallEntry;
    // Сперва настройка провайдера, потом запуск: значок и служба читают
    // voice.json на старте, и порядок наоборот дал бы онбординг поверх уже
    // настроенного провайдера.
    ApplyConfigFile;
    InstallServiceOrAutostart;
    ReleaseIMESpace;
  end;
end;

// ShouldDeleteUserData — удалять ли историю и записи при удалении программы.
//
// По умолчанию данные остаются: человек может поставить диктовку снова, а
// потерянная история — это потерянные мысли. В обычном режиме спрашиваем
// подтверждением (кнопка «Нет» — по умолчанию); в тихом удалении молчание
// значит «оставить», а удалить можно ключом /DELETEUSERDATA=1.
function ShouldDeleteUserData: Boolean;
var
  Value: String;
  Answer: Integer;
begin
  Value := LowerCase(ExpandConstant('{param:DELETEUSERDATA|0}'));
  if (Value = '1') or (Value = 'yes') or (Value = 'true') then
  begin
    Result := True;
    exit;
  end;
  if UninstallSilent then
  begin
    Result := False;
    exit;
  end;
  Answer := MsgBox('Удалить также историю и записи Диктовки?' + #13#10 +
    'Нажмите «Нет», чтобы оставить их: тогда после повторной установки история ' +
    'и записи будут на месте.', mbConfirmation, MB_YESNO or MB_DEFBUTTON2);
  Result := Answer = IDYES;
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  Exe: String;
  Code: Integer;
begin
  if CurUninstallStep = usUninstall then
  begin
    Exe := ExpandConstant('{app}\dictation.exe');
    // Сперва просим выйти: при идущей записи --quit ждёт её конца, а не рвёт
    // разговор. Не дождался — удаление откладывается, файлы не тронуты.
    if FileExists(Exe) then
    begin
      Exec(Exe, '--quit --wait ' + IntToStr({#StopWaitSeconds}), '', SW_HIDE,
        ewWaitUntilTerminated, Code);
      Log('Asked the running dictation to quit before uninstall, code=' + IntToStr(Code));
      if Code = 3 then
      begin
        Log('Dictation is recording: uninstall cancelled before any file was removed');
        if not UninstallSilent then
          MsgBox('Диктовка сейчас записывает разговор, поэтому удаление отменено: ' +
            'обрывать запись нельзя. Закончите диктовку и повторите удаление.',
            mbError, MB_OK);
        Abort;
      end;
    end;
    // Службу снимает только установка «для всех»: у установки в профиль прав на
    // неё нет, и притворяться, что она снята, нельзя.
    if IsAdminInstallMode and FileExists(Exe) then
    begin
      Exec(Exe, '--uninstall-service', '', SW_HIDE, ewWaitUntilTerminated, Code);
      Log('Uninstalled the Windows service, code=' + IntToStr(Code));
    end;
    // Автозапуск убираем всегда: и запись, которую положил установщик, и
    // запасную, если служба когда-то не поставилась.
    RegDeleteValue(HKCU, '{#RunKey}', '{#RunValue}');
    RegDeleteValue(HKEY_LOCAL_MACHINE, '{#RunKey}', '{#RunValue}');
    // Системное сочетание возвращаем до удаления файлов — своей же сборкой.
    if FileExists(Exe) then
    begin
      Exec(Exe, '--restore-ime-hotkey', '', SW_HIDE, ewWaitUntilTerminated, Code);
      Log('Restored the system Ctrl+Space, code=' + IntToStr(Code));
    end;
  end;
  if CurUninstallStep = usPostUninstall then
  begin
    if ShouldDeleteUserData then
    begin
      DelTree(ExpandConstant('{localappdata}\Dictation'), True, True, True);
      DelTree(ExpandConstant('{commonappdata}\Dictation'), True, True, True);
      Log('User data removed: history, recordings and settings');
    end
    else
      Log('User data kept: history, recordings and settings');
  end;
end;
