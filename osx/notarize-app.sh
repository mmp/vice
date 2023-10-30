#!/bin/zsh

set -e

# make a zip for notarization.
zip -rv vice.zip Vice.app

# get the zip file notarized
xcrun notarytool submit \
      --wait \
      --apple-id ${APPLE_CODESIGN_ID} \
      --password @env:APPLE_CODESIGN_PASSWORD \
      --team-id 325U8G3LXB \
      --timeout 5m \
      vice.zip

# notarized! staple the notarization
xcrun stapler staple Vice.app
