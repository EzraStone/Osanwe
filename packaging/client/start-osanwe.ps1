[CmdletBinding()]
param(
    [switch]$ChangeEnrollment
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version 2.0

$appName = "Osanwe"
$installRoot = $PSScriptRoot
$dataRoot = Join-Path ([Environment]::GetFolderPath("LocalApplicationData")) $appName
$browserRoot = Join-Path $dataRoot "Browser"
$configPath = Join-Path $dataRoot "osanwe.json"
$binaryPath = Join-Path $installRoot "bearer.exe"
$appURL = "http://127.0.0.1:8080/_osanwe/"
$statusURL = "http://127.0.0.1:8080/_osanwe/status"

Add-Type -AssemblyName System.Drawing
Add-Type -AssemblyName System.Windows.Forms
[System.Windows.Forms.Application]::EnableVisualStyles()

function Show-OsanweMessage {
    param(
        [string]$Text,
        [System.Windows.Forms.MessageBoxIcon]$Icon = [System.Windows.Forms.MessageBoxIcon]::Information
    )
    [void][System.Windows.Forms.MessageBox]::Show(
        $Text,
        $appName,
        [System.Windows.Forms.MessageBoxButtons]::OK,
        $Icon
    )
}

function Resolve-ContainedPath {
    param(
        [string]$Parent,
        [string]$Relative
    )
    $parentFull = [IO.Path]::GetFullPath($Parent).TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    $candidate = [IO.Path]::GetFullPath((Join-Path $Parent $Relative))
    if (-not $candidate.StartsWith($parentFull, [StringComparison]::OrdinalIgnoreCase)) {
        throw "The enrollment file refers to a certificate outside its own folder. Ask the beta inviter for a self-contained enrollment package."
    }
    return $candidate
}

function Get-JsonValue {
    param(
        [object]$Object,
        [string]$Name
    )
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) { return $null }
    return $property.Value
}

function Import-OsanweEnrollment {
    Show-OsanweMessage "Choose the osanwe.json enrollment file supplied by your beta inviter. The relay secret and beta entitlement are never imported or saved."

    $picker = New-Object System.Windows.Forms.OpenFileDialog
    $picker.Title = "Choose Osanwe enrollment"
    $picker.Filter = "Osanwe enrollment (osanwe.json)|osanwe.json|JSON files (*.json)|*.json"
    $picker.CheckFileExists = $true
    $picker.Multiselect = $false
    try {
        if ($picker.ShowDialog() -ne [System.Windows.Forms.DialogResult]::OK) {
            return $false
        }
        $sourceConfig = $picker.FileName
    }
    finally {
        $picker.Dispose()
    }

    $config = Get-Content -LiteralPath $sourceConfig -Raw | ConvertFrom-Json
    if ([int](Get-JsonValue -Object $config -Name "schema_version") -ne 1) {
        throw "The selected enrollment uses an unsupported schema version. Ask the beta inviter for a current osanwe.json."
    }
    $relay = [string](Get-JsonValue -Object $config -Name "relay")
    $directories = @(Get-JsonValue -Object $config -Name "directories")
    if ([string]::IsNullOrWhiteSpace($relay) -and $directories.Count -eq 0) {
        throw "The selected enrollment does not name a relay or signed relay directory."
    }

    New-Item -ItemType Directory -Path $dataRoot -Force | Out-Null
    $upstreamCA = [string](Get-JsonValue -Object $config -Name "upstream_ca")
    if (-not [string]::IsNullOrWhiteSpace($upstreamCA) -and -not [IO.Path]::IsPathRooted($upstreamCA)) {
        $sourceDirectory = Split-Path -Parent $sourceConfig
        $sourceCertificate = Resolve-ContainedPath -Parent $sourceDirectory -Relative $upstreamCA
        if (-not (Test-Path -LiteralPath $sourceCertificate -PathType Leaf)) {
            throw "The enrollment names a gateway certificate that is not beside the selected file. Ask the beta inviter for the complete package."
        }
        $targetCertificate = Resolve-ContainedPath -Parent $dataRoot -Relative $upstreamCA
        New-Item -ItemType Directory -Path (Split-Path -Parent $targetCertificate) -Force | Out-Null
        Copy-Item -LiteralPath $sourceCertificate -Destination $targetCertificate -Force
    }
    Copy-Item -LiteralPath $sourceConfig -Destination $configPath -Force
    return $true
}

function Read-OsanweCredentials {
    param([bool]$NeedsEntitlement)

    $form = New-Object System.Windows.Forms.Form
    $form.Text = "Open Osanwe"
    $form.StartPosition = [System.Windows.Forms.FormStartPosition]::CenterScreen
    $form.FormBorderStyle = [System.Windows.Forms.FormBorderStyle]::FixedDialog
    $form.MaximizeBox = $false
    $form.MinimizeBox = $false
    $form.ShowInTaskbar = $true
    $form.ClientSize = if ($NeedsEntitlement) { New-Object System.Drawing.Size(470, 278) } else { New-Object System.Drawing.Size(470, 218) }

    $title = New-Object System.Windows.Forms.Label
    $title.Text = "Open your local Osanwe app"
    $title.Font = New-Object System.Drawing.Font("Segoe UI", 14, [System.Drawing.FontStyle]::Bold)
    $title.AutoSize = $true
    $title.Location = New-Object System.Drawing.Point(22, 18)
    $form.Controls.Add($title)

    $explanation = New-Object System.Windows.Forms.Label
    $explanation.Text = "These beta credentials stay in memory only for this session. Your provider API key is entered later in Settings."
    $explanation.AutoSize = $false
    $explanation.Size = New-Object System.Drawing.Size(420, 38)
    $explanation.Location = New-Object System.Drawing.Point(24, 53)
    $form.Controls.Add($explanation)

    $secretLabel = New-Object System.Windows.Forms.Label
    $secretLabel.Text = "Relay secret"
    $secretLabel.AutoSize = $true
    $secretLabel.Location = New-Object System.Drawing.Point(24, 99)
    $form.Controls.Add($secretLabel)

    $secretBox = New-Object System.Windows.Forms.TextBox
    $secretBox.UseSystemPasswordChar = $true
    $secretBox.Size = New-Object System.Drawing.Size(420, 24)
    $secretBox.Location = New-Object System.Drawing.Point(24, 119)
    $form.Controls.Add($secretBox)

    $receiptBox = $null
    if ($NeedsEntitlement) {
        $receiptLabel = New-Object System.Windows.Forms.Label
        $receiptLabel.Text = "Beta entitlement"
        $receiptLabel.AutoSize = $true
        $receiptLabel.Location = New-Object System.Drawing.Point(24, 157)
        $form.Controls.Add($receiptLabel)

        $receiptBox = New-Object System.Windows.Forms.TextBox
        $receiptBox.UseSystemPasswordChar = $true
        $receiptBox.Size = New-Object System.Drawing.Size(420, 24)
        $receiptBox.Location = New-Object System.Drawing.Point(24, 177)
        $form.Controls.Add($receiptBox)
    }

    $openButton = New-Object System.Windows.Forms.Button
    $openButton.Text = "Open Osanwe"
    $openButton.DialogResult = [System.Windows.Forms.DialogResult]::OK
    $openButton.Size = New-Object System.Drawing.Size(108, 32)
    $openButton.Location = if ($NeedsEntitlement) { New-Object System.Drawing.Point(336, 226) } else { New-Object System.Drawing.Point(336, 166) }
    $form.Controls.Add($openButton)
    $form.AcceptButton = $openButton

    $cancelButton = New-Object System.Windows.Forms.Button
    $cancelButton.Text = "Cancel"
    $cancelButton.DialogResult = [System.Windows.Forms.DialogResult]::Cancel
    $cancelButton.Size = New-Object System.Drawing.Size(86, 32)
    $cancelButton.Location = if ($NeedsEntitlement) { New-Object System.Drawing.Point(240, 226) } else { New-Object System.Drawing.Point(240, 166) }
    $form.Controls.Add($cancelButton)
    $form.CancelButton = $cancelButton

    $form.Add_Shown({ $secretBox.Select() })
    try {
        if ($form.ShowDialog() -ne [System.Windows.Forms.DialogResult]::OK) {
            return $null
        }
        $secret = $secretBox.Text
        $receipt = if ($null -ne $receiptBox) { $receiptBox.Text } else { "" }
        if ([string]::IsNullOrWhiteSpace($secret)) {
            Show-OsanweMessage "The relay secret cannot be empty." ([System.Windows.Forms.MessageBoxIcon]::Warning)
            return $null
        }
        if ($NeedsEntitlement -and [string]::IsNullOrWhiteSpace($receipt)) {
            Show-OsanweMessage "The beta entitlement cannot be empty." ([System.Windows.Forms.MessageBoxIcon]::Warning)
            return $null
        }
        return [pscustomobject]@{ Secret = $secret; Receipt = $receipt }
    }
    finally {
        $secretBox.Clear()
        if ($null -ne $receiptBox) { $receiptBox.Clear() }
        $form.Dispose()
    }
}

function Get-RunningOsanwe {
    try {
        $status = Invoke-RestMethod -UseBasicParsing -Uri $statusURL -Method Get -TimeoutSec 1
        if ($status.retained -eq "nothing" -and $null -ne $status.privacy -and $null -ne $status.build) {
            return $status
        }
    }
    catch {
        return $null
    }
    return $null
}

function Find-Edge {
    $candidates = @(
        (Join-Path ${env:ProgramFiles(x86)} "Microsoft\Edge\Application\msedge.exe"),
        (Join-Path $env:ProgramFiles "Microsoft\Edge\Application\msedge.exe"),
        (Join-Path $env:LOCALAPPDATA "Microsoft\Edge\Application\msedge.exe")
    )
    foreach ($candidate in $candidates) {
        if (-not [string]::IsNullOrWhiteSpace($candidate) -and (Test-Path -LiteralPath $candidate -PathType Leaf)) {
            return $candidate
        }
    }
    $command = Get-Command "msedge.exe" -ErrorAction SilentlyContinue
    if ($null -ne $command) { return $command.Source }
    return $null
}

function Open-OsanweWindow {
    param([bool]$WaitForClose)

    $edge = Find-Edge
    if ($null -eq $edge) {
        Start-Process $appURL
        if ($WaitForClose) {
            Show-OsanweMessage "Osanwe is open in your browser. Select OK here when you are finished to stop the local app."
        }
        return
    }

    New-Item -ItemType Directory -Path $browserRoot -Force | Out-Null
    $edgeInfo = New-Object System.Diagnostics.ProcessStartInfo
    $edgeInfo.FileName = $edge
    $edgeInfo.UseShellExecute = $true
    $edgeInfo.Arguments = '--app="' + $appURL + '" --user-data-dir="' + $browserRoot + '" --no-first-run --disable-sync --disable-background-mode'
    $edgeProcess = [System.Diagnostics.Process]::Start($edgeInfo)
    if ($WaitForClose -and $null -ne $edgeProcess) {
        $edgeProcess.WaitForExit()
    }
}

function Stop-OsanweClient {
    param([System.Diagnostics.Process]$Process)
    if ($null -eq $Process -or $Process.HasExited) { return }
    try {
        $Process.StandardInput.Close()
        if (-not $Process.WaitForExit(12000)) {
            $Process.Kill()
            $Process.WaitForExit()
        }
    }
    catch {
        if (-not $Process.HasExited) { $Process.Kill() }
    }
}

$client = $null
$appMutex = $null
$ownsMutex = $false
try {
    if (-not (Test-Path -LiteralPath $binaryPath -PathType Leaf)) {
        throw "bearer.exe is missing. Reinstall Osanwe from a complete release."
    }
    if ($ChangeEnrollment -or -not (Test-Path -LiteralPath $configPath -PathType Leaf)) {
        if ($null -ne (Get-RunningOsanwe)) {
            Show-OsanweMessage "Close the running Osanwe window before changing its enrollment." ([System.Windows.Forms.MessageBoxIcon]::Warning)
            exit 2
        }
        if (-not (Import-OsanweEnrollment)) { exit 0 }
    }

    if ($null -ne (Get-RunningOsanwe)) {
        Open-OsanweWindow -WaitForClose $false
        exit 0
    }

    $appMutex = New-Object System.Threading.Mutex($false, "Local\OsanweDesktop")
    $ownsMutex = $appMutex.WaitOne(0)
    if (-not $ownsMutex) {
        for ($attempt = 0; $attempt -lt 25; $attempt++) {
            if ($null -ne (Get-RunningOsanwe)) {
                Open-OsanweWindow -WaitForClose $false
                exit 0
            }
            Start-Sleep -Milliseconds 200
        }
        Show-OsanweMessage "Osanwe is already starting. Try opening it again in a few seconds."
        exit 0
    }

    $config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
    $needsEntitlement = -not [string]::IsNullOrWhiteSpace([string](Get-JsonValue -Object $config -Name "mint"))
    $credentials = Read-OsanweCredentials -NeedsEntitlement $needsEntitlement
    if ($null -eq $credentials) { exit 0 }

    $startInfo = New-Object System.Diagnostics.ProcessStartInfo
    $startInfo.FileName = $binaryPath
    $startInfo.Arguments = '-config "' + $configPath + '" -exit-on-stdin-close'
    $startInfo.WorkingDirectory = $dataRoot
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardInput = $true
    $startInfo.RedirectStandardError = $true
    $startInfo.EnvironmentVariables["OSANWE_SECRET"] = [string]$credentials.Secret
    if ($needsEntitlement) {
        $startInfo.EnvironmentVariables["OSANWE_RECEIPT"] = [string]$credentials.Receipt
    }

    $client = New-Object System.Diagnostics.Process
    $client.StartInfo = $startInfo
    if (-not $client.Start()) { throw "Windows could not start the Osanwe client." }
    $stderrTask = $client.StandardError.ReadToEndAsync()
    [void]$startInfo.EnvironmentVariables.Remove("OSANWE_SECRET")
    [void]$startInfo.EnvironmentVariables.Remove("OSANWE_RECEIPT")
    $credentials.Secret = $null
    $credentials.Receipt = $null
    $credentials = $null

    $ready = $false
    for ($attempt = 0; $attempt -lt 100; $attempt++) {
        if ($client.HasExited) {
            $details = $stderrTask.Result.Trim()
            if ([string]::IsNullOrWhiteSpace($details)) { $details = "The client stopped before its local interface became ready." }
            throw $details
        }
        if ($null -ne (Get-RunningOsanwe)) {
            $ready = $true
            break
        }
        Start-Sleep -Milliseconds 200
    }
    if (-not $ready) { throw "The local app did not become ready within 20 seconds." }

    try {
        Open-OsanweWindow -WaitForClose $true
    }
    finally {
        Stop-OsanweClient -Process $client
    }
}
catch {
    if ($null -ne $client) { Stop-OsanweClient -Process $client }
    Show-OsanweMessage $_.Exception.Message ([System.Windows.Forms.MessageBoxIcon]::Error)
    exit 1
}
finally {
    if ($ownsMutex -and $null -ne $appMutex) { $appMutex.ReleaseMutex() }
    if ($null -ne $appMutex) { $appMutex.Dispose() }
}
