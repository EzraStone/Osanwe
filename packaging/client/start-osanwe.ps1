$ErrorActionPreference = "Stop"

$configPath = Join-Path $PSScriptRoot "osanwe.json"
$binaryPath = Join-Path $PSScriptRoot "bearer.exe"

if (-not (Test-Path -LiteralPath $configPath -PathType Leaf)) {
    Write-Host "This Osanwe client has not been enrolled yet."
    Write-Host "Ask your beta inviter for osanwe.json and place it beside this launcher."
    exit 2
}
if (-not (Test-Path -LiteralPath $binaryPath -PathType Leaf)) {
    Write-Host "bearer.exe is missing from this archive. Download a complete release again."
    exit 2
}

$config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
$secretValue = Read-Host "Paste the relay secret (it will not be saved)" -AsSecureString
$secretPointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secretValue)
$receiptPointer = [IntPtr]::Zero

try {
    $env:OSANWE_SECRET = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($secretPointer)
    if ([string]::IsNullOrWhiteSpace($env:OSANWE_SECRET)) {
        throw "The relay secret cannot be empty."
    }

    if (-not [string]::IsNullOrWhiteSpace([string]$config.mint)) {
        $receiptValue = Read-Host "Paste the beta entitlement (it will not be saved)" -AsSecureString
        $receiptPointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($receiptValue)
        $env:OSANWE_RECEIPT = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($receiptPointer)
        if ([string]::IsNullOrWhiteSpace($env:OSANWE_RECEIPT)) {
            throw "The beta entitlement cannot be empty."
        }
    }

    & $binaryPath -config $configPath -open-ui
    exit $LASTEXITCODE
}
finally {
    Remove-Item Env:OSANWE_SECRET -ErrorAction SilentlyContinue
    Remove-Item Env:OSANWE_RECEIPT -ErrorAction SilentlyContinue
    [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($secretPointer)
    if ($receiptPointer -ne [IntPtr]::Zero) {
        [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($receiptPointer)
    }
}
