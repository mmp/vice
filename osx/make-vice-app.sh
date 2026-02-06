#!/bin/bash
#
# Creates Vice.app bundle from the built vice binary.
# Optionally signs and notarizes if Apple credentials are provided.
#
# Usage: ./osx/make-vice-app.sh
#
# Required:
#   - vice binary must exist in current directory
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

if [ ! -f "vice" ]; then
    echo "Error: vice binary not found in current directory"
    exit 1
fi

echo "=== Creating icons ==="
mkdir -p icon.iconset
cp cmd/vice/icons/tower-rounded-inset-16x16.png icon.iconset/icon_16x16.png
cp cmd/vice/icons/tower-rounded-inset-32x32.png icon.iconset/icon_16x16@2.png
cp cmd/vice/icons/tower-rounded-inset-32x32.png icon.iconset/icon_32x32.png
cp cmd/vice/icons/tower-rounded-inset-64x64.png icon.iconset/icon_32x32@2.png
cp cmd/vice/icons/tower-rounded-inset-64x64.png icon.iconset/icon_64x64.png
cp cmd/vice/icons/tower-rounded-inset-128x128.png icon.iconset/icon_64x64@2.png
cp cmd/vice/icons/tower-rounded-inset-128x128.png icon.iconset/icon_128x128.png
cp cmd/vice/icons/tower-rounded-inset-256x256.png icon.iconset/icon_128x128@2.png
cp cmd/vice/icons/tower-rounded-inset-256x256.png icon.iconset/icon_256x256.png
cp cmd/vice/icons/tower-rounded-inset-512x512.png icon.iconset/icon_256x256@2.png
cp cmd/vice/icons/tower-rounded-inset-512x512.png icon.iconset/icon_512x512.png
cp cmd/vice/icons/tower-rounded-inset-1024x1024.png icon.iconset/icon_512x512@2.png
iconutil -c icns icon.iconset

echo "=== Creating Vice.app bundle ==="
rm -rf Vice.app
mkdir -p Vice.app/Contents/MacOS
cp vice Vice.app/Contents/MacOS/
cp osx/Info.plist Vice.app/Contents/
mkdir -p Vice.app/Contents/Resources
cp icon.icns Vice.app/Contents/Resources
mkdir -p Vice.app/Contents/Resources/fonts
cp fonts/*zst Vice.app/Contents/Resources/fonts/

# Check if signing credentials are available
if [ -z "$APPLE_DEVELOPER_ID_APPLICATION" ]; then
    echo "=== Skipping signing (no APPLE_DEVELOPER_ID_APPLICATION) ==="
    echo "Vice.app created successfully (unsigned)"
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

# Check if notarization credentials are available
if [ -z "$APPLE_CODESIGN_ID" ] || [ -z "$APPLE_CODESIGN_PASSWORD" ] || [ -z "$APPLE_TEAMID" ]; then
    echo "=== Skipping notarization (missing credentials) ==="
    echo "Vice.app created and signed successfully (not notarized)"
    exit 0
fi

echo "=== Notarizing Vice.app ==="
zip -rv vice-notarize.zip Vice.app
xcrun notarytool submit \
    --wait \
    --apple-id "${APPLE_CODESIGN_ID}" \
    --password "${APPLE_CODESIGN_PASSWORD}" \
    --team-id "${APPLE_TEAMID}" \
    --timeout 30m \
    vice-notarize.zip
rm vice-notarize.zip

echo "=== Stapling notarization ==="
xcrun stapler staple Vice.app

echo "Vice.app created, signed, and notarized successfully"
