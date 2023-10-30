#!/bin/zsh

set -e

# make a zip for notarization.
zip -rv vice.zip Vice.app

# get the zip file notarized
xcrun notarytool submit \
      --wait \
      --apple-id ${APPLE_CODESIGN_ID} \
      --password @env:APPLE_CODESIGN_PASSWORD \
      --timeout 5m \
      vice.zip

# notarized! staple the notarization
xcrun stapler staple Vice.app
