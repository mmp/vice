#!/bin/bash
#
# Build script for vice (macOS and Linux)
#
# Usage: ./build.sh [options]
#
# Options:
#   --check         Run gofmt and staticcheck
#   --test          Run tests
#   --all           Run all steps (--check --test, then build)
#   --release       Build release binary (with downloadresources tag)
#   --universal     Build universal binary on macOS (arm64 + amd64)
#   --help          Show this help message
#
# whisper-cpp is built automatically if needed.

set -e

# Expected whisper.cpp submodule SHA (update this when bumping the submodule)
WHISPER_EXPECTED_SHA="9dc0d4695d97d5b57e4abe9d6a309fa9e05ae318"

# Check that whisper.cpp submodule is at the expected commit
check_whisper_submodule() {
    if [ ! -d "whisper.cpp/.git" ] && [ ! -f "whisper.cpp/.git" ]; then
        echo "Error: whisper.cpp submodule is not initialized."
        echo ""
        echo "Please run:"
        echo "  git submodule update --init --recursive"
        exit 1
    fi

    WHISPER_ACTUAL_SHA=$(git -C whisper.cpp rev-parse HEAD 2>/dev/null || echo "")
    if [ -z "$WHISPER_ACTUAL_SHA" ]; then
        echo "Error: Could not determine whisper.cpp submodule version."
        echo ""
        echo "Please run:"
        echo "  git submodule update --init --recursive"
        exit 1
    fi

    if [ "$WHISPER_ACTUAL_SHA" != "$WHISPER_EXPECTED_SHA" ]; then
        echo "Error: whisper.cpp submodule is at wrong commit."
        echo ""
        echo "  Expected: $WHISPER_EXPECTED_SHA"
        echo "  Actual:   $WHISPER_ACTUAL_SHA"
        echo ""
        echo "Please run:"
        echo "  git submodule update --init --recursive"
        exit 1
    fi
}

check_whisper_submodule

# Detect OS
OS="$(uname -s)"
case "$OS" in
    Darwin) OS_TYPE="macos" ;;
    Linux)  OS_TYPE="linux" ;;
    *)      echo "Unsupported OS: $OS"; exit 1 ;;
esac

# Default options
DO_CHECK=false
DO_TEST=false
DO_RELEASE=false
DO_UNIVERSAL=false

# Parse arguments
for arg in "$@"; do
    case "$arg" in
        --check)     DO_CHECK=true ;;
        --test)      DO_TEST=true ;;
        --release)   DO_RELEASE=true ;;
        --universal) DO_UNIVERSAL=true ;;
        --all)
            DO_CHECK=true
            DO_TEST=true
            ;;
        --help)
            head -15 "$0" | tail -13
            exit 0
            ;;
        *)
            echo "Unknown option: $arg"
            echo "Use --help for usage information"
            exit 1
            ;;
    esac
done

# Build whisper-cpp
build_whisper() {
    echo "=== Building whisper-cpp ==="

    if [ "$OS_TYPE" = "macos" ]; then
        # Disable GGML_NATIVE since we're building a universal binary.
        # x86 flags (AVX, etc.) only affect x86_64 compilation; ARM uses NEON.
        cmake -S whisper.cpp -B whisper.cpp/build_go \
            -DBUILD_SHARED_LIBS=OFF \
            -DGGML_CPU=ON \
            -DGGML_METAL=ON \
            -DGGML_BLAS=ON \
            -DGGML_METAL_EMBED_LIBRARY=ON \
            -DCMAKE_BUILD_TYPE=Release \
            -DCMAKE_OSX_ARCHITECTURES="x86_64;arm64" \
            -DCMAKE_OSX_DEPLOYMENT_TARGET=13.0
    elif [ "$OS_TYPE" = "linux" ]; then
        # Disable GGML_NATIVE to avoid -march=native. Enable instruction sets
        # safe for computers from ~2013+ (Haswell era, see build.bat for details).
        cmake -S whisper.cpp -B whisper.cpp/build_go \
            -DBUILD_SHARED_LIBS=OFF \
            -DGGML_CPU=ON \
            -DGGML_OPENMP=ON \
            -DGGML_NATIVE=OFF \
            -DGGML_SSE42=ON \
            -DGGML_AVX=ON \
            -DGGML_AVX2=ON \
            -DGGML_FMA=ON \
            -DGGML_F16C=ON \
            -DGGML_BMI2=ON \
            -DCMAKE_BUILD_TYPE=Release
    fi

    cmake --build whisper.cpp/build_go --parallel "$(nproc 2>/dev/null || sysctl -n hw.ncpu)"

    echo "whisper-cpp built successfully."
}

# Run checks (gofmt, staticcheck)
run_checks() {
    echo "=== Running checks ==="

    echo "Checking gofmt..."
    GOFMT_OUTPUT=$(gofmt -l . 2>&1 || true)
    if [ -n "$GOFMT_OUTPUT" ]; then
        echo "The following files require reformatting with gofmt:"
        echo "$GOFMT_OUTPUT"
        echo "Run 'gofmt -w .' to fix."
        exit 1
    fi
    echo "gofmt: OK"

    echo "Running staticcheck..."
    if ! command -v staticcheck &> /dev/null; then
        echo "Installing staticcheck..."
        go install honnef.co/go/tools/cmd/staticcheck@latest
    fi
    staticcheck ./...
    echo "staticcheck: OK"
}

# Build vice
build_vice() {
    echo "=== Building vice ==="

    # Set version
    git describe --tags --abbrev=8 --dirty --always --long > resources/version.txt
    echo "Version: $(cat resources/version.txt)"

    # Determine build tags
    BUILD_TAGS=""
    if [ "$OS_TYPE" = "macos" ]; then
        BUILD_TAGS="static"
    elif [ "$OS_TYPE" = "linux" ]; then
        BUILD_TAGS="imguifreetype"
    fi

    if [ "$DO_RELEASE" = true ]; then
        BUILD_TAGS="$BUILD_TAGS,downloadresources"
    fi

    # Build
    if [ "$OS_TYPE" = "macos" ]; then
        export MACOSX_DEPLOYMENT_TARGET='13.0'
        export CGO_CFLAGS='-mmacosx-version-min=13.0'
        export CGO_LDFLAGS='-mmacosx-version-min=13.0'

        if [ "$DO_UNIVERSAL" = true ]; then
            echo "Building universal binary..."
            CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -tags "$BUILD_TAGS" -o vice_amd64 ./cmd/vice
            CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -tags "$BUILD_TAGS" -o vice_arm64 ./cmd/vice
            lipo -create -output vice vice_amd64 vice_arm64
            rm vice_amd64 vice_arm64
        else
            CGO_ENABLED=1 GOOS=darwin go build -ldflags="-s -w" -tags "$BUILD_TAGS" -o vice ./cmd/vice
        fi
    elif [ "$OS_TYPE" = "linux" ]; then
        go build -tags "$BUILD_TAGS" -o vice ./cmd/vice
    fi

    echo "Build complete: ./vice"
}

# Run tests
run_tests() {
    echo "=== Running tests ==="
    go test -v ./...
    echo "Tests passed."
}

# Check if whisper-cpp needs to be built
needs_whisper_build() {
    if [ ! -f "whisper.cpp/build_go/src/libwhisper.a" ]; then
        return 0
    fi
    if [ "$OS_TYPE" = "macos" ]; then
        if [ ! -f "whisper.cpp/build_go/ggml/src/ggml-metal/libggml-metal.a" ]; then
            return 0
        fi
    fi
    return 1
}

# Main execution
if needs_whisper_build; then
    build_whisper
fi

if [ "$DO_CHECK" = true ]; then
    run_checks
fi

build_vice

if [ "$DO_TEST" = true ]; then
    run_tests
fi

echo "=== Done ==="
