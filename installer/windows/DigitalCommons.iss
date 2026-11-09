; Digital Commons installer for Windows, compiled by Inno Setup 6.
; Build it with installer/build.py, which passes AppVersion, StageDir, OutputDir and
; IconFile. It installs for the current user only and needs no administrator rights.

#ifndef AppVersion
  #error Build this with installer/build.py
#endif

[Setup]
AppId={{6F1B2E7C-5A41-4C8E-9C0B-0D3A9E5B7F21}
AppName=Digital Commons
AppVersion={#AppVersion}
AppVerName=Digital Commons {#AppVersion} (beta)
AppPublisher=Digital Commons project
AppPublisherURL=https://github.com/BedrotAudio/digital-commons
AppSupportURL=https://github.com/BedrotAudio/digital-commons/issues
DefaultDirName={autopf}\Digital Commons
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0.17763
OutputDir={#OutputDir}
OutputBaseFilename=DigitalCommons-{#AppVersion}-windows-x64-setup
SetupIconFile={#IconFile}
UninstallDisplayIcon={app}\DigitalCommons.ico
UninstallDisplayName=Digital Commons
Compression=lzma2/ultra64
SolidCompression=yes
WizardStyle=modern
InfoBeforeFile=before-install.txt
CloseApplications=yes

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked

[Files]
Source: "{#StageDir}\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs

[Icons]
Name: "{autoprograms}\Digital Commons"; Filename: "{app}\runtime\python.exe"; Parameters: """{app}\dc.py"" start"; WorkingDir: "{app}"; IconFilename: "{app}\DigitalCommons.ico"; Comment: "AI workspace that runs on this computer"
Name: "{autodesktop}\Digital Commons"; Filename: "{app}\runtime\python.exe"; Parameters: """{app}\dc.py"" start"; WorkingDir: "{app}"; IconFilename: "{app}\DigitalCommons.ico"; Comment: "AI workspace that runs on this computer"; Tasks: desktopicon

[Run]
Filename: "{app}\runtime\python.exe"; Parameters: """{app}\dc.py"" start"; WorkingDir: "{app}"; Description: "Start Digital Commons and finish setting it up"; Flags: postinstall nowait skipifsilent

[UninstallRun]
Filename: "{app}\runtime\python.exe"; Parameters: """{app}\dc.py"" stop"; WorkingDir: "{app}"; Flags: runhidden waituntilterminated; RunOnceId: "StopGateway"

[UninstallDelete]
; Python may cache compiled files beside the ones installed. Only these folders, which
; belong to Digital Commons, are removed whole.
Type: filesandordirs; Name: "{app}\runtime"
Type: filesandordirs; Name: "{app}\backend"

[Code]
procedure StopRunningGateway();
var
  Python: String;
  Code: Integer;
begin
  Python := ExpandConstant('{app}\runtime\python.exe');
  if FileExists(Python) then
    Exec(Python, '"' + ExpandConstant('{app}\dc.py') + '" stop', ExpandConstant('{app}'), SW_HIDE, ewWaitUntilTerminated, Code);
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
begin
  { An update replaces files a running gateway holds open. }
  StopRunningGateway();
  Result := '';
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  Data: String;
begin
  if CurUninstallStep = usPostUninstall then
  begin
    Data := ExpandConstant('{localappdata}\DigitalCommons');
    if DirExists(Data) and (SuppressibleMsgBox(
        'Also delete your Digital Commons conversations and settings from this computer?' + #13#10#13#10 + Data,
        mbConfirmation, MB_YESNO or MB_DEFBUTTON2, IDNO) = IDYES) then
      DelTree(Data, True, True, True);
  end;
end;
