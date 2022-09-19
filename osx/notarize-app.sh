#!/bin/zsh -x

set -e

# make a zip for notarization.
zip -rv vice.zip Vice.app

# get the zip file notarized
xcrun altool --notarize-app \
      --primary-bundle-id org.pharr.vice \
      -u ${APPLE_CODESIGN_ID} \
      -p @env:APPLE_CODESIGN_PASSWORD \
      -f vice.zip > notarize.out

# via https://www.msweet.org/blog/2020-12-10-macos-notarization.html
uuid=`grep RequestUUID notarize.out | awk '{print $3}'`
notarizestatus="in progress"

while [[ "$notarizestatus" = "in progress" ]]; do
  echo "    $uuid: $notarizestatus"
  sleep 10
  echo "xcrun altool --notarization-info ..."
  xcrun altool --notarization-info $uuid -u ${APPLE_CODESIGN_ID} --password @env:APPLE_CODESIGN_PASSWORD > notarize.out
  cat notarize.out
  notarizestatus="`grep Status: notarize.out | cut -b14-`"
done

echo "    $uuid: $notarizestatus"

# notarized! staple the notarization
xcrun stapler staple Vice.app
