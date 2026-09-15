#!/bin/bash
#
# Creates the Vice.app and Backshop.app bundles from the built binaries.
# Optionally signs and notarizes if Apple credentials are provided.
#
# Usage: ./osx/make-apps.sh
#
# Required:
#   - vice and backshop binaries must exist in current directory
#   - fonts/*.zst must exist
#
# Optional environment variables for signing/notarization:
#   APPLE_DEVELOPER_ID_APPLICATION - Developer ID for codesign
#   APPLE_DEVELOPER_ID_CERT_FILE   - Base64-encoded .p12 certificate
#   APPLE_DEVELOPER_ID_CERT_PASSWORD - Password for .p12 certificate
#   APPLE_CODESIGN_ID              - Apple ID for notarization
#   APPLE_CODESIGN_PASSWORD        - App-specific password for notarization
#   APPLE_TEAMID                   - Apple Team ID

set -e

for gui in vice backshop; do
    if [ ! -f "$gui" ]; then
        echo "Error: $gui binary not found in current directory"
        exit 1
    fi
done

for tool in crc2vice dat2vice viceserver; do
    if [ ! -f "$tool" ]; then
        echo "Error: $tool binary not found in current directory"
        exit 1
    fi
done

# make_app <bundle> <executable> <Info.plist> <iconset>. Each bundle gets its
# own copy of fonts/, since renderer/font.go looks for them next to the
# executable (Contents/Resources on macOS), and its own icon.icns, the name
# both Info.plist files give for CFBundleIconFile.
make_app() {
    local bundle="$1" exe="$2" plist="$3" iconset="$4"
    echo "=== Creating $bundle bundle ==="
    rm -rf "$bundle"
    mkdir -p "$bundle/Contents/MacOS"
    cp "$exe" "$bundle/Contents/MacOS/"
    cp "$plist" "$bundle/Contents/Info.plist"
    mkdir -p "$bundle/Contents/Resources/fonts"
    iconutil -c icns -o "$bundle/Contents/Resources/icon.icns" "$iconset"
    cp fonts/*zst "$bundle/Contents/Resources/fonts/"
}

make_app Vice.app vice osx/Info.plist cmd/vice/icons/macos/vice-icon.iconset
make_app Backshop.app backshop osx/Info-backshop.plist cmd/backshop/icons/macos/backshop-icon.iconset

# Check if signing credentials are available
if [ -z "$APPLE_DEVELOPER_ID_APPLICATION" ]; then
    echo "=== Skipping signing (no APPLE_DEVELOPER_ID_APPLICATION) ==="
    echo "Vice.app and Backshop.app created successfully (unsigned)"
    exit 0
fi

echo "=== Setting up keychain for signing ==="
EPHEMERAL_KEYCHAIN="ci-ephemeral-keychain"
EPHEMERAL_KEYCHAIN_PASSWORD="$(openssl rand -base64 100)"
security create-keychain -p "${EPHEMERAL_KEYCHAIN_PASSWORD}" "${EPHEMERAL_KEYCHAIN}"
EPHEMERAL_KEYCHAIN_FULL_PATH="$HOME/Library/Keychains/${EPHEMERAL_KEYCHAIN}-db"
echo "${APPLE_DEVELOPER_ID_CERT_FILE}" | base64 -d > cert.p12
security import ./cert.p12 -k "${EPHEMERAL_KEYCHAIN_FULL_PATH}" -P "${APPLE_DEVELOPER_ID_CERT_PASSWORD}" -T "$(command -v codesign)"
security -q set-key-partition-list -S "apple-tool:,apple:" -s -k "${EPHEMERAL_KEYCHAIN_PASSWORD}" "${EPHEMERAL_KEYCHAIN_FULL_PATH}"
security default-keychain -d "user" -s "${EPHEMERAL_KEYCHAIN_FULL_PATH}"
rm cert.p12

echo "=== Signing Vice.app ==="
codesign -s "${APPLE_DEVELOPER_ID_APPLICATION}" -f -v --timestamp --options runtime --entitlements osx/vice.entitlements Vice.app

# backshop needs no entitlements: it never records audio.
echo "=== Signing Backshop.app ==="
codesign -s "${APPLE_DEVELOPER_ID_APPLICATION}" -f -v --timestamp --options runtime Backshop.app

echo "=== Signing crc2vice, dat2vice, viceserver ==="
for tool in crc2vice dat2vice viceserver; do
    codesign -s "${APPLE_DEVELOPER_ID_APPLICATION}" -f -v --timestamp --options runtime "$tool"
done

# Check if notarization credentials are available
if [ -z "$APPLE_CODESIGN_ID" ] || [ -z "$APPLE_CODESIGN_PASSWORD" ] || [ -z "$APPLE_TEAMID" ]; then
    echo "=== Skipping notarization (missing credentials) ==="
    echo "Vice.app, Backshop.app, and helper tools created and signed successfully (not notarized)"
    exit 0
fi

echo "=== Notarizing the app bundles and helper tools ==="
zip -rv vice-notarize.zip Vice.app Backshop.app crc2vice dat2vice viceserver
xcrun notarytool submit \
    --wait \
    --apple-id "${APPLE_CODESIGN_ID}" \
    --password "${APPLE_CODESIGN_PASSWORD}" \
    --team-id "${APPLE_TEAMID}" \
    --timeout 30m \
    vice-notarize.zip
rm vice-notarize.zip

echo "=== Stapling notarization ==="
# stapler only works on bundles (.app, .pkg, .dmg). Standalone CLI binaries
# can't be stapled, but their notarization tickets are looked up online by
# Gatekeeper when first run.
xcrun stapler staple Vice.app
xcrun stapler staple Backshop.app

echo "Vice.app, Backshop.app, and helper tools created, signed, and notarized successfully"
