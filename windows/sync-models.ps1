# Sync models from GCS based on manifest.json
# Usage: sync-models.ps1 <manifest-path> <models-dir> <stamp-path>

param(
    [Parameter(Mandatory=$true)][string]$ManifestPath,
    [Parameter(Mandatory=$true)][string]$ModelsDir,
    [Parameter(Mandatory=$true)][string]$StampPath
)

# Use .NET directly for hashing - more reliable across PowerShell versions
function Get-Sha256Hash($Path) {
    $sha256 = [System.Security.Cryptography.SHA256]::Create()
    $stream = [System.IO.File]::OpenRead($Path)
    try {
        $hash = $sha256.ComputeHash($stream)
        return [System.BitConverter]::ToString($hash).Replace('-', '').ToLower()
    }
    finally {
        $stream.Close()
        $sha256.Dispose()
    }
}

if (-not (Test-Path $ManifestPath)) {
    exit 0
}

$manifest = Get-Content $ManifestPath | ConvertFrom-Json
$manifestHash = Get-Sha256Hash $ManifestPath

# Fast path: check if already synced
if (Test-Path $StampPath) {
    $stampHash = Get-Content $StampPath -Raw
    if ($stampHash.Trim() -eq $manifestHash) {
        $allExist = $true
        $manifest.PSObject.Properties | ForEach-Object {
            $modelPath = Join-Path $ModelsDir $_.Name
            if (-not (Test-Path $modelPath)) {
                $allExist = $false
            }
        }
        if ($allExist) {
            exit 0
        }
    }
}

# Full sync path
$manifest.PSObject.Properties | ForEach-Object {
    $model = $_.Name
    $expectedHash = $_.Value
    $modelPath = Join-Path $ModelsDir $model
    $needDownload = $true

    if (Test-Path $modelPath) {
        $actualHash = Get-Sha256Hash $modelPath
        if ($actualHash -eq $expectedHash) {
            $needDownload = $false
        } else {
            Write-Host "Model $model has wrong hash, re-downloading..."
        }
    }

    if ($needDownload) {
        Write-Host "Downloading $model..."
        $url = 'https://storage.googleapis.com/vice-resources/' + $expectedHash
        curl.exe -L --progress-bar -o $modelPath $url
        if ($LASTEXITCODE -ne 0) {
            Write-Host "Error: curl failed to download $model (exit code $LASTEXITCODE)"
            exit 1
        }
        $actualHash = Get-Sha256Hash $modelPath
        if ($actualHash -ne $expectedHash) {
            Write-Host "Error: Downloaded file hash mismatch for $model"
            Write-Host "  Expected: $expectedHash"
            Write-Host "  Actual: $actualHash"
            Remove-Item $modelPath -Force
            exit 1
        }
    }
}

# Write stamp file
$manifestHash | Out-File -FilePath $StampPath -NoNewline
