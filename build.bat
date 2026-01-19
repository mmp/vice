@echo off
setlocal enabledelayedexpansion

REM Build script for vice (Windows)
REM
REM Usage: build.bat [options]
REM
REM Options:
REM   --check         Run gofmt and staticcheck
REM   --test          Run tests
REM   --all           Run all steps (--check --test, then build)
REM   --release       Build release binary (with downloadresources tag)
REM   --icons         Prepare Windows icon resources (requires go-winres)
REM   --help          Show this help message
REM
REM Prerequisites:
REM   - Go installed and in PATH
REM   - MinGW-w64 installed and in PATH (for gcc, cmake)
REM
REM whisper-cpp is built automatically if needed.

set DO_CHECK=0
set DO_TEST=0
set DO_RELEASE=0
set DO_ICONS=0

REM Expected whisper.cpp submodule SHA (update this when bumping the submodule)
set WHISPER_EXPECTED_SHA=d8e355def01e4f9c381c4e80873ac49bb5b594e1

REM Check that whisper.cpp submodule is at the expected commit
if not exist "whisper.cpp\.git" (
    echo Error: whisper.cpp submodule is not initialized.
    echo.
    echo Please run:
    echo   git submodule update --init --recursive
    exit /b 1
)

for /f "delims=" %%i in ('git -C whisper.cpp rev-parse HEAD 2^>nul') do set WHISPER_ACTUAL_SHA=%%i
if not defined WHISPER_ACTUAL_SHA (
    echo Error: Could not determine whisper.cpp submodule version.
    echo.
    echo Please run:
    echo   git submodule update --init --recursive
    exit /b 1
)

if not "!WHISPER_ACTUAL_SHA!"=="!WHISPER_EXPECTED_SHA!" (
    echo Error: whisper.cpp submodule is at wrong commit.
    echo.
    echo   Expected: !WHISPER_EXPECTED_SHA!
    echo   Actual:   !WHISPER_ACTUAL_SHA!
    echo.
    echo Please run:
    echo   git submodule update --init --recursive
    exit /b 1
)

REM Parse arguments
:parse_args
if "%~1"=="" goto done_parsing
if "%~1"=="--check" set DO_CHECK=1
if "%~1"=="--test" set DO_TEST=1
if "%~1"=="--release" set DO_RELEASE=1
if "%~1"=="--icons" set DO_ICONS=1
if "%~1"=="--all" (
    set DO_CHECK=1
    set DO_TEST=1
)
if "%~1"=="--help" (
    echo Build script for vice ^(Windows^)
    echo.
    echo Usage: build.bat [options]
    echo.
    echo Options:
    echo   --check         Run gofmt and staticcheck
    echo   --test          Run tests
    echo   --all           Run all steps
    echo   --release       Build release binary
    echo   --icons         Prepare Windows icon resources
    echo   --help          Show this help message
    exit /b 0
)
shift
goto parse_args
:done_parsing

REM Download and extract SDL2 if not present
if not exist "ext\SDL2-2.24.0" (
    echo === Downloading SDL2 ===
    if not exist "ext" mkdir ext
    curl -L -o ext\SDL2-devel-2.24.0-mingw.zip https://github.com/libsdl-org/SDL/releases/download/release-2.24.0/SDL2-devel-2.24.0-mingw.zip
    if errorlevel 1 exit /b 1
    echo Extracting SDL2...
    powershell -Command "Expand-Archive -Path 'ext\SDL2-devel-2.24.0-mingw.zip' -DestinationPath 'ext'"
    if errorlevel 1 exit /b 1
    del ext\SDL2-devel-2.24.0-mingw.zip
)

REM Set SDL2 paths only if not already set (allows CI to override)
if not defined CGO_CFLAGS (
    set SDL2_DIR=%CD%\ext\SDL2-2.24.0\x86_64-w64-mingw32
    set CGO_CFLAGS=-I !SDL2_DIR!\include
    set CGO_CPPFLAGS=-I !SDL2_DIR!\include
    set CGO_LDFLAGS=-L !SDL2_DIR!\lib
)   

REM Copy runtime DLLs from ext/ to windows/ if available
set MINGW_BIN=%CD%\ext\mingw\mingw64\bin
if exist "!MINGW_BIN!\libgcc_s_seh-1.dll" (
    if not exist "windows\libgcc_s_seh-1.dll" (
        echo Copying MinGW runtime DLLs to windows/
        copy "!MINGW_BIN!\libgcc_s_seh-1.dll" "windows\" >nul
        copy "!MINGW_BIN!\libstdc++-6.dll" "windows\" >nul
    )
)
if exist "!SDL2_DIR!\bin\SDL2.dll" (
    if not exist "windows\SDL2.dll" (
        echo Copying SDL2.dll to windows/
        copy "!SDL2_DIR!\bin\SDL2.dll" "windows\" >nul
    )
)

REM Check if Vulkan SDK is available
set VULKAN_AVAILABLE=0
if defined VULKAN_SDK (
    echo Checking for Vulkan SDK at !VULKAN_SDK!
    if exist "!VULKAN_SDK!\Bin\glslc.exe" (
        set VULKAN_AVAILABLE=1
        echo Vulkan SDK detected at !VULKAN_SDK!
    ) else (
        echo glslc.exe not found at !VULKAN_SDK!\Bin\glslc.exe
        dir "!VULKAN_SDK!\Bin" 2>nul || echo Bin directory not found
    )
) else (
    echo VULKAN_SDK environment variable not set
)

REM Build whisper-cpp if needed
if not exist "whisper.cpp\build_go\src\libwhisper.a" (
    echo === Building whisper-cpp ===
    REM Disable GGML_NATIVE to avoid using -march=native which would compile
    REM for the build machine's CPU. Instead, explicitly enable instruction sets
    REM that are available on computers from ~2013+ (Haswell era):
    REM - SSE4.2, AVX, F16C: Intel Ivy Bridge 2012+, AMD Piledriver 2012+
    REM - AVX2, FMA, BMI2: Intel Haswell 2013+, AMD Excavator 2015+
    if !VULKAN_AVAILABLE!==1 (
        echo Building with Vulkan GPU support...
        cmake -S whisper.cpp -B whisper.cpp\build_go ^
            -G "MinGW Makefiles" ^
            -DBUILD_SHARED_LIBS=OFF ^
            -DGGML_CPU=ON ^
            -DGGML_VULKAN=ON ^
            -DGGML_OPENMP=OFF ^
            -DGGML_NATIVE=OFF ^
            -DGGML_SSE42=ON ^
            -DGGML_AVX=ON ^
            -DGGML_AVX2=ON ^
            -DGGML_FMA=ON ^
            -DGGML_F16C=ON ^
            -DGGML_BMI2=ON ^
            -DCMAKE_BUILD_TYPE=Release ^
            -DCMAKE_C_FLAGS="-D_WIN32_WINNT=0x0601 -DWINVER=0x0601" ^
            -DCMAKE_CXX_FLAGS="-D_WIN32_WINNT=0x0601 -DWINVER=0x0601"
    ) else (
        echo Building without GPU support (Vulkan SDK not found^)...
        cmake -S whisper.cpp -B whisper.cpp\build_go ^
            -G "MinGW Makefiles" ^
            -DBUILD_SHARED_LIBS=OFF ^
            -DGGML_CPU=ON ^
            -DGGML_OPENMP=OFF ^
            -DGGML_NATIVE=OFF ^
            -DGGML_SSE42=ON ^
            -DGGML_AVX=ON ^
            -DGGML_AVX2=ON ^
            -DGGML_FMA=ON ^
            -DGGML_F16C=ON ^
            -DGGML_BMI2=ON ^
            -DCMAKE_BUILD_TYPE=Release ^
            -DCMAKE_C_FLAGS="-D_WIN32_WINNT=0x0601 -DWINVER=0x0601" ^
            -DCMAKE_CXX_FLAGS="-D_WIN32_WINNT=0x0601 -DWINVER=0x0601"
    )
    if errorlevel 1 exit /b 1

    cmake --build whisper.cpp\build_go --parallel 4
    if errorlevel 1 exit /b 1

    REM Ensure ggml libraries are in expected location
    if not exist whisper.cpp\build_go\ggml\src mkdir whisper.cpp\build_go\ggml\src
    for %%b in (ggml ggml-base ggml-cpu) do (
        if not exist "whisper.cpp\build_go\ggml\src\lib%%b.a" (
            for /r whisper.cpp\build_go %%f in (%%b.a lib%%b.a) do (
                if exist "%%f" copy /y "%%f" "whisper.cpp\build_go\ggml\src\lib%%b.a" >nul 2>&1
            )
        )
    )
    REM Copy Vulkan library if built with Vulkan support
    if !VULKAN_AVAILABLE!==1 (
        echo Looking for Vulkan library in build output...
        if not exist whisper.cpp\build_go\ggml\src\ggml-vulkan mkdir whisper.cpp\build_go\ggml\src\ggml-vulkan
        set VULKAN_LIB_FOUND=0
        for /r whisper.cpp\build_go %%f in (ggml-vulkan.a) do (
            if exist "%%f" (
                echo Found: %%f
                copy /y "%%f" "whisper.cpp\build_go\ggml\src\ggml-vulkan\ggml-vulkan.a"
                set VULKAN_LIB_FOUND=1
            )
        )
        if !VULKAN_LIB_FOUND!==0 (
            echo ERROR: ggml-vulkan.a not found in build output!
            echo Listing ggml build directories:
            dir /s /b whisper.cpp\build_go\ggml 2>nul | findstr /i "\.a$"
            exit /b 1
        )
    )
    echo whisper-cpp built successfully.
)

REM Verify Vulkan library exists if we're building with Vulkan support
if !VULKAN_AVAILABLE!==1 (
    if not exist "whisper.cpp\build_go\ggml\src\ggml-vulkan\ggml-vulkan.a" (
        echo ERROR: Vulkan support requested but ggml-vulkan.a not found!
        echo This may indicate a cached build without Vulkan. Cleaning and rebuilding...
        rmdir /s /q whisper.cpp\build_go 2>nul
        echo Please run build.bat again to rebuild with Vulkan support.
        exit /b 1
    )
    echo Vulkan library verified at whisper.cpp\build_go\ggml\src\ggml-vulkan\ggml-vulkan.a
)

REM Prepare icon resources
if %DO_ICONS%==1 (
    echo === Preparing icon resources ===
    go install github.com/tc-hib/go-winres@latest
    if errorlevel 1 exit /b 1
    go-winres make --in windows\winres.json --out cmd\vice\rsrc
    if errorlevel 1 exit /b 1
    echo Icon resources prepared.
)

REM Run checks
if %DO_CHECK%==1 (
    echo === Running checks ===

    echo Checking gofmt...
    for /f %%i in ('gofmt -l . 2^>^&1') do (
        echo The following files require reformatting with gofmt:
        gofmt -l .
        echo Run 'gofmt -w .' to fix.
        exit /b 1
    )
    echo gofmt: OK

    echo Running staticcheck...
    where staticcheck >nul 2>&1
    if errorlevel 1 (
        echo Installing staticcheck...
        go install honnef.co/go/tools/cmd/staticcheck@latest
    )
    staticcheck ./...
    if errorlevel 1 exit /b 1
    echo staticcheck: OK
)

REM Build vice
echo === Building vice ===

REM Set version
git describe --tags --abbrev=8 --dirty --always --long > resources\version.txt
for /f "delims=" %%v in (resources\version.txt) do echo Version: %%v

REM Determine build tags
set BUILD_TAGS=static
if %DO_RELEASE%==1 set BUILD_TAGS=!BUILD_TAGS!,downloadresources
if !VULKAN_AVAILABLE!==1 set BUILD_TAGS=!BUILD_TAGS!,vulkan

go build -tags !BUILD_TAGS! -ldflags="-s -w -H=windowsgui" -o vice.exe .\cmd\vice
if errorlevel 1 exit /b 1

echo Build complete: vice.exe

REM Run tests
if %DO_TEST%==1 (
    echo === Running tests ===
    go test -v ./...
    if errorlevel 1 exit /b 1
    echo Tests passed.
)

echo === Done ===
