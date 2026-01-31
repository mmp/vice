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
#   --vulkan        Build with vulkan support
#   --release       Build release binary (with downloadresources tag)
#   --universal     Build universal binary on macOS (arm64 + amd64)
#   --help          Show this help message
#
# whisper-cpp is built automatically if needed.

set -e

# Expected whisper.cpp submodule SHA (update this when bumping the submodule)
WHISPER_EXPECTED_SHA="050f4ef8286ca6d49b1b0e131462b9d71959f5ff"

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

# Sync models from GCS if needed
sync_models() {
    local manifest="resources/models/manifest.json"
    local models_dir="resources/models"
    local stamp="$models_dir/.synced"

    if [ ! -f "$manifest" ]; then
        return 0  # No manifest = no models to sync
    fi

    # Determine the sha256sum command (shasum -a 256 on macOS, sha256sum on Linux)
    if command -v sha256sum &> /dev/null; then
        SHA256CMD="sha256sum"
    elif command -v shasum &> /dev/null; then
        SHA256CMD="shasum -a 256"
    else
        echo "Error: No SHA256 command found (sha256sum or shasum)"
        exit 1
    fi

    # Parse JSON - try jq first, fall back to python
    if command -v jq &> /dev/null; then
        JSON_PARSER="jq"
    elif command -v python3 &> /dev/null; then
        JSON_PARSER="python3"
    elif command -v python &> /dev/null; then
        JSON_PARSER="python"
    else
        echo "Error: No JSON parser found (jq, python3, or python)"
        exit 1
    fi

    # Get list of models from manifest
    if [ "$JSON_PARSER" = "jq" ]; then
        models=$(jq -r 'keys[]' "$manifest")
    else
        models=$($JSON_PARSER -c "import json; [print(k) for k in json.load(open('$manifest')).keys()]" 2>/dev/null)
    fi

    # Compute manifest hash for stamp file comparison
    local manifest_hash=$($SHA256CMD "$manifest" | cut -d' ' -f1)

    # Fast path: check if already synced
    if [ -f "$stamp" ] && [ "$(cat "$stamp")" = "$manifest_hash" ]; then
        # Verify all files exist (don't hash, just existence)
        local all_exist=true
        for model in $models; do
            if [ ! -f "$models_dir/$model" ]; then
                all_exist=false
                break
            fi
        done
        if [ "$all_exist" = true ]; then
            return 0  # Already synced
        fi
    fi

    for model in $models; do
        # Get expected hash
        if [ "$JSON_PARSER" = "jq" ]; then
            expected_hash=$(jq -r ".\"$model\"" "$manifest")
        else
            expected_hash=$($JSON_PARSER -c "import json; print(json.load(open('$manifest'))['$model'])")
        fi

        local model_path="$models_dir/$model"

        # Check if file exists with correct hash
        if [ -f "$model_path" ]; then
            actual_hash=$($SHA256CMD "$model_path" | cut -d' ' -f1)
            if [ "$actual_hash" = "$expected_hash" ]; then
                continue  # File is up to date
            fi
            echo "Model $model has wrong hash, re-downloading..."
        fi

        # Download from GCS (public bucket, no auth needed)
        echo "Downloading $model..."
        curl -L --progress-bar -o "$model_path" \
            "https://storage.googleapis.com/vice-resources/$expected_hash"

        # Verify the download
        actual_hash=$($SHA256CMD "$model_path" | cut -d' ' -f1)
        if [ "$actual_hash" != "$expected_hash" ]; then
            echo "Error: Downloaded file hash mismatch for $model"
            echo "  Expected: $expected_hash"
            echo "  Actual:   $actual_hash"
            rm -f "$model_path"
            exit 1
        fi
    done

    # Write stamp file to skip verification on next build
    echo "$manifest_hash" > "$stamp"
}

sync_models

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
DO_VULKAN=false

# Parse arguments
for arg in "$@"; do
    case "$arg" in
        --check)     DO_CHECK=true ;;
        --test)      DO_TEST=true ;;
        --release)   DO_RELEASE=true ;;
        --universal) DO_UNIVERSAL=true ;;
        --vulkan)    DO_VULKAN=true ;;
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
        if [ "$DO_VULKAN" = true ]; then
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
                -DGGML_VULKAN=ON \
                -DCMAKE_BUILD_TYPE=Release
        else
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
        if [ "$DO_VULKAN" = true ]; then
            BUILD_TAGS="$BUILD_TAGS,vulkan"
        fi
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
