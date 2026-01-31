# Sync models from GCS based on manifest.json
# Usage: sync-models.ps1 <manifest-path> <models-dir> <stamp-path>

param(
    [Parameter(Mandatory=$true)][string]$ManifestPath,
    [Parameter(Mandatory=$true)][string]$ModelsDir,
    [Parameter(Mandatory=$true)][string]$StampPath
)

if (-not (Test-Path $ManifestPath)) {
    exit 0
}

$manifest = Get-Content $ManifestPath | ConvertFrom-Json
$manifestHash = (Get-FileHash -Path $ManifestPath -Algorithm SHA256).Hash.ToLower()

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
        $actualHash = (Get-FileHash -Path $modelPath -Algorithm SHA256).Hash.ToLower()
        if ($actualHash -eq $expectedHash) {
            $needDownload = $false
        } else {
            Write-Host "Model $model has wrong hash, re-downloading..."
        }
    }

    if ($needDownload) {
        Write-Host "Downloading $model..."
        $url = 'https://storage.googleapis.com/vice-resources/' + $expectedHash
        Invoke-WebRequest -Uri $url -OutFile $modelPath -UseBasicParsing
        $actualHash = (Get-FileHash -Path $modelPath -Algorithm SHA256).Hash.ToLower()
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
