#ifndef AppVersion
  #define AppVersion "dev"
#endif
#ifndef ClientDir
  #define ClientDir "."
#endif
#ifndef InstallerDir
  #define InstallerDir "."
#endif

[Setup]
AppId={{A5348AE8-36B7-49CB-A569-6D54CB5616B7}
AppName=Osanwe
AppVersion={#AppVersion}
AppPublisher=Ezra Stone
AppPublisherURL=https://github.com/EzraStone/Osanwe
AppSupportURL=https://github.com/EzraStone/Osanwe/issues
AppUpdatesURL=https://github.com/EzraStone/Osanwe/releases
DefaultDirName={localappdata}\Programs\Osanwe
DefaultGroupName=Osanwe
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
MinVersion=10.0.17763
OutputDir={#InstallerDir}
OutputBaseFilename=Osanwe-Setup_{#AppVersion}_windows_amd64
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
AppMutex=Local\OsanweDesktop
CloseApplications=yes
RestartApplications=no
UninstallDisplayName=Osanwe
UninstallDisplayIcon={app}\bearer.exe
SetupLogging=yes

[Tasks]
Name: "desktopicon"; Description: "Create a desktop shortcut"; GroupDescription: "Shortcuts:"; Flags: checkedonce

[Files]
Source: "{#ClientDir}\bearer.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#ClientDir}\start-osanwe.ps1"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#ClientDir}\QUICKSTART.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#ClientDir}\osanwe.example.json"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#ClientDir}\LICENSE"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#ClientDir}\NOTICE"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#ClientDir}\SECURITY.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#ClientDir}\THIRD_PARTY_NOTICES.md"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\Osanwe"; Filename: "{sys}\WindowsPowerShell\v1.0\powershell.exe"; Parameters: "-NoLogo -NoProfile -STA -ExecutionPolicy Bypass -WindowStyle Hidden -File ""{app}\start-osanwe.ps1"""; WorkingDir: "{app}"; Comment: "Open the local Osanwe app"
Name: "{group}\Change Osanwe enrollment"; Filename: "{sys}\WindowsPowerShell\v1.0\powershell.exe"; Parameters: "-NoLogo -NoProfile -STA -ExecutionPolicy Bypass -WindowStyle Hidden -File ""{app}\start-osanwe.ps1"" -ChangeEnrollment"; WorkingDir: "{app}"; Comment: "Choose a different beta enrollment file"
Name: "{group}\Uninstall Osanwe"; Filename: "{uninstallexe}"
Name: "{autodesktop}\Osanwe"; Filename: "{sys}\WindowsPowerShell\v1.0\powershell.exe"; Parameters: "-NoLogo -NoProfile -STA -ExecutionPolicy Bypass -WindowStyle Hidden -File ""{app}\start-osanwe.ps1"""; WorkingDir: "{app}"; Comment: "Open the local Osanwe app"; Tasks: desktopicon

[Run]
Filename: "{sys}\WindowsPowerShell\v1.0\powershell.exe"; Parameters: "-NoLogo -NoProfile -STA -ExecutionPolicy Bypass -WindowStyle Hidden -File ""{app}\start-osanwe.ps1"""; Description: "Open Osanwe"; Flags: nowait postinstall skipifsilent

[Code]
function InitializeUninstall(): Boolean;
begin
  Result := MsgBox(
    'Uninstalling removes the app and shortcuts. Enrollment and any browser-only conversation history remain in your local Osanwe data folder unless you delete them from Settings first.' + #13#10 + #13#10 +
    'Continue uninstalling?',
    mbConfirmation,
    MB_OKCANCEL
  ) = IDOK;
end;
