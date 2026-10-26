; Inno Setup script for the Windows installer (docs/packaging.md). Built in CI by
; packaging/windows/build_installer.sh:
;
;   ISCC /DAppVersion=2026.10.0 /DAppNumericVersion=2026.10.0 /DSourceDir=...\dist\OpenArcanum /DOutputDir=...\dist
;        /DIconFile=...\build\icons\openarcanum.ico [/DSignInstaller "/Sopenarcanumsign=..."]
;        packaging\windows\openarcanum.iss
;
; Installs the PyInstaller bundle (dist\OpenArcanum) per user by default (no
; administrator rights; the user may choose all users), adds Start menu and
; optional desktop shortcuts, and registers the openarcanum:// URI scheme so
; invite links open the app (openarcanum/instance.py). The auto-updater
; (openarcanum/updates.py) runs this installer with /SILENT /SUPPRESSMSGBOXES
; /NORESTART after the app has exited; a silent run starts the app again.
; Uninstalling removes the program files, shortcuts and the scheme
; registration; the user's profile under %LOCALAPPDATA%\OpenArcanum is kept.

#ifndef AppVersion
  #error AppVersion is required (ISCC /DAppVersion=X.Y.Z)
#endif
#ifndef SourceDir
  #error SourceDir is required (the PyInstaller output folder)
#endif
; Windows' version resources take numbers only: YYYY.MM.PATCH without a
; -beta.N suffix (AppVersion keeps it).
#ifndef AppNumericVersion
  #define AppNumericVersion AppVersion
#endif
#ifndef OutputDir
  #define OutputDir "."
#endif

#define AppName "OpenArcanum"
#define AppExe "OpenArcanum.exe"
; Never change it: upgrades and the uninstaller find the installation by it.
#define AppGuid "DA31AE2B-D4DF-4D44-B882-69D226BADB41"

[Setup]
AppId={{{#AppGuid}}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} {#AppVersion}
AppPublisher={#AppName}
VersionInfoVersion={#AppNumericVersion}
VersionInfoProductTextVersion={#AppVersion}
DefaultDirName={autopf}\{#AppName}
DefaultGroupName={#AppName}
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0
OutputDir={#OutputDir}
OutputBaseFilename={#AppName}-Setup-{#AppVersion}
#ifdef IconFile
SetupIconFile={#IconFile}
#endif
UninstallDisplayIcon={app}\{#AppExe}
UninstallDisplayName={#AppName}
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
; A running copy is closed (Restart Manager) before files are replaced; the
; [Run] entry below starts the new version after a silent update.
CloseApplications=yes
RestartApplications=no
#ifdef SignInstaller
SignTool=openarcanumsign
SignedUninstaller=yes
#endif

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked

[InstallDelete]
; An upgrade replaces the whole bundle, so files a newer version dropped
; don't linger next to the new ones.
Type: filesandordirs; Name: "{app}\_internal"

[Files]
Source: "{#SourceDir}\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs

[Icons]
Name: "{autoprograms}\{#AppName}"; Filename: "{app}\{#AppExe}"
Name: "{autodesktop}\{#AppName}"; Filename: "{app}\{#AppExe}"; Tasks: desktopicon

[Registry]
; openarcanum://invite/... links start the app with the link as its argument.
; HKA is HKCU for a per-user install and HKLM for an all-users one.
Root: HKA; Subkey: "Software\Classes\openarcanum"; ValueType: string; ValueName: ""; ValueData: "URL:{#AppName} invite link"; Flags: uninsdeletekey
Root: HKA; Subkey: "Software\Classes\openarcanum"; ValueType: string; ValueName: "URL Protocol"; ValueData: ""
Root: HKA; Subkey: "Software\Classes\openarcanum\DefaultIcon"; ValueType: string; ValueName: ""; ValueData: """{app}\{#AppExe}"",0"
Root: HKA; Subkey: "Software\Classes\openarcanum\shell\open\command"; ValueType: string; ValueName: ""; ValueData: """{app}\{#AppExe}"" -- ""%1"""

[Run]
Filename: "{app}\{#AppExe}"; Description: "{cm:LaunchProgram,{#AppName}}"; Flags: nowait postinstall skipifsilent
; After a silent upgrade (the auto-updater's), start the new version.
Filename: "{app}\{#AppExe}"; Flags: nowait runasoriginaluser; Check: SilentUpgrade

[Code]
var
  WasInstalled: Boolean;

function InitializeSetup(): Boolean;
var
  Key: String;
begin
  Key := 'Software\Microsoft\Windows\CurrentVersion\Uninstall\{{#AppGuid}}_is1';
  WasInstalled := RegKeyExists(HKCU, Key) or RegKeyExists(HKLM, Key);
  Result := True;
end;

function SilentUpgrade(): Boolean;
begin
  Result := WizardSilent and WasInstalled;
end;
