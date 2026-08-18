# Checks that every DLL vice.exe imports will resolve on a stock Windows
# install: each one must either be part of Windows or ship in the installer.
# An unresolved import kills the process in the loader before main() runs, so
# there is no window, no log file, and nothing to go on in a bug report.

param(
    [string]$Exe = "vice.exe",
    [string]$ShippedDir = "windows"
)

$ErrorActionPreference = 'Stop'

# Part of a stock Windows install. Note that DLLs installed by GPU drivers
# (vulkan-1.dll) or by the Visual C++ redistributable do not belong here.
$windowsDlls = @(
    'ADVAPI32.dll',
    'GDI32.dll',
    'KERNEL32.dll',
    'msvcrt.dll',
    'OPENGL32.dll',
    'SHELL32.dll',
    'USER32.dll'
)

$shipped = Get-ChildItem $ShippedDir -Filter *.dll | ForEach-Object { $_.Name }
$known = $windowsDlls + $shipped

$imports = (objdump -p $Exe) |
    Select-String -Pattern '^\s*DLL Name:\s*(\S+)' |
    ForEach-Object { $_.Matches[0].Groups[1].Value }

if (-not $imports) {
    throw "Found no imports in $Exe; is objdump on PATH?"
}

$missing = $imports | Where-Object { $known -notcontains $_ }

if ($missing) {
    Write-Host "$Exe imports DLLs that are neither part of Windows nor shipped:"
    $missing | ForEach-Object { Write-Host "    $_" }
    Write-Host ""
    Write-Host "Copy them to $ShippedDir and add a component in windows\makeinstaller.go,"
    Write-Host "or, if they really are part of Windows, list them in this script."
    exit 1
}

Write-Host "$Exe imports $($imports.Count) DLLs, all shipped or part of Windows"
