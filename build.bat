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
REM   - SDL2 development files in ext\SDL2-2.24.0\x86_64-w64-mingw32\
REM
REM whisper-cpp is built automatically if needed.

set DO_CHECK=0
set DO_TEST=0
set DO_RELEASE=0
set DO_ICONS=0

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

REM Set SDL2 paths only if not already set (allows CI to override)
if not defined CGO_CFLAGS (
    set SDL2_DIR=%CD%\ext\SDL2-2.24.0\x86_64-w64-mingw32
    set CGO_CFLAGS=-I !SDL2_DIR!\include
    set CGO_CPPFLAGS=-I !SDL2_DIR!\include
    set CGO_LDFLAGS=-L !SDL2_DIR!\lib
)

REM Build whisper-cpp if needed
if not exist "whisper.cpp\build_go\src\libwhisper.a" (
    echo === Building whisper-cpp ===
    cmake -S whisper.cpp -B whisper.cpp\build_go ^
        -G "MinGW Makefiles" ^
        -DBUILD_SHARED_LIBS=OFF ^
        -DGGML_CPU=ON ^
        -DGGML_OPENMP=OFF ^
        -DCMAKE_BUILD_TYPE=Release ^
        -DCMAKE_C_FLAGS="-D_WIN32_WINNT=0x0601 -DWINVER=0x0601" ^
        -DCMAKE_CXX_FLAGS="-D_WIN32_WINNT=0x0601 -DWINVER=0x0601"
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
    echo whisper-cpp built successfully.
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
if %DO_RELEASE%==1 set BUILD_TAGS=%BUILD_TAGS%,downloadresources

go build -tags %BUILD_TAGS% -ldflags="-s -w -H=windowsgui" -o vice.exe .\cmd\vice
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
