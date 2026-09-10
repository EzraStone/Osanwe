param([string]$BaseUrl = 'https://osanwe.vercel.app')
$ErrorActionPreference = 'Stop'
$nodePath = (Get-Command node -ErrorAction Stop).Source
Write-Host 'This sends at most six synthetic requests using only z-ai/glm-5.3-free.'
Write-Host 'The key stays in process memory and is not saved. No paid fallback or retries.'
Write-Host 'The hosted site processes the key, synthetic prompts, and replies in transit.'
$confirmation = Read-Host 'Confirm this account/model is free for you today by typing FREE'
if ($confirmation -cne 'FREE') { throw 'Stopped without making requests.' }
$secret = Read-Host 'Paste your TokenRouter test key (hidden)' -AsSecureString
$pointer = [IntPtr]::Zero
$child = $null
try {
    $pointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secret)
    $plainKey = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($pointer)
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $nodePath
    $start.UseShellExecute = $false
    $start.RedirectStandardInput = $true
    $start.CreateNoWindow = $true
    # Arguments contain paths and the public host only, never credentials.
    $start.ArgumentList.Add((Join-Path $PSScriptRoot 'verify-free-tokenrouter.mjs'))
    $start.ArgumentList.Add($BaseUrl)
    $child = [Diagnostics.Process]::Start($start)
    $child.StandardInput.WriteLine($plainKey)
    $child.StandardInput.Close()
    $plainKey = $null
    $child.WaitForExit()
    if ($child.ExitCode -ne 0) { Write-Host 'Verification did not pass; no automatic retry was made.' }
} finally {
    $plainKey = $null
    if ($pointer -ne [IntPtr]::Zero) { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($pointer) }
    $secret.Dispose()
    if ($child) { $child.Dispose() }
}
