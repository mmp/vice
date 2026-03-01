# Sync models from R2 based on manifest.json
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

# Separate kokoro zip/file entries from individual download entries
$kokoroZipHash = $null
$kokoroFiles = @()
$individualFiles = @()

$manifest.PSObject.Properties | ForEach-Object {
    if ($_.Name -eq 'kokoro-multi-lang-v1_0.zip') {
        $script:kokoroZipHash = $_.Value.hash
    } elseif ($_.Name -like 'kokoro-multi-lang-v1_0/*') {
        $script:kokoroFiles += $_.Name
    } else {
        $script:individualFiles += $_.Name
    }
}

# Fast path: check if already synced
if (Test-Path $StampPath) {
    $stampHash = Get-Content $StampPath -Raw
    if ($stampHash.Trim() -eq $manifestHash) {
        $allExist = $true
        foreach ($model in $individualFiles) {
            if (-not (Test-Path (Join-Path $ModelsDir $model))) {
                $allExist = $false
                break
            }
        }
        if ($allExist) {
            foreach ($model in $kokoroFiles) {
                if (-not (Test-Path (Join-Path $ModelsDir $model))) {
                    $allExist = $false
                    break
                }
            }
        }
        if ($allExist) {
            exit 0
        }
    }
}

# Check if any kokoro files are missing; download and extract zip if so
if ($kokoroFiles.Count -gt 0) {
    $kokoroMissing = $false
    foreach ($model in $kokoroFiles) {
        if (-not (Test-Path (Join-Path $ModelsDir $model))) {
            $kokoroMissing = $true
            break
        }
    }

    if ($kokoroMissing) {
        Write-Host "Downloading kokoro-multi-lang-v1_0.zip..."
        $zipPath = Join-Path $ModelsDir 'kokoro-multi-lang-v1_0.zip'
        $url = 'https://vice-resources.pharr.org/' + $kokoroZipHash
        curl.exe -L --progress-bar -o $zipPath $url
        if ($LASTEXITCODE -ne 0) {
            Write-Host "Error: curl failed to download kokoro-multi-lang-v1_0.zip (exit code $LASTEXITCODE)"
            exit 1
        }

        $actualHash = Get-Sha256Hash $zipPath
        if ($actualHash -ne $kokoroZipHash) {
            Write-Host "Error: Downloaded file hash mismatch for kokoro-multi-lang-v1_0.zip"
            Write-Host "  Expected: $kokoroZipHash"
            Write-Host "  Actual: $actualHash"
            Remove-Item $zipPath -Force
            exit 1
        }

        Write-Host "Extracting kokoro-multi-lang-v1_0.zip..."
        Expand-Archive -Path $zipPath -DestinationPath $ModelsDir -Force
        Remove-Item $zipPath -Force
    }
}

# Download individual files (whisper models)
foreach ($model in $individualFiles) {
    $expectedHash = $manifest.$model.hash
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
        $parentDir = Split-Path -Parent $modelPath
        if (-not (Test-Path $parentDir)) {
            New-Item -ItemType Directory -Path $parentDir -Force | Out-Null
        }
        $url = 'https://vice-resources.pharr.org/' + $expectedHash
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
