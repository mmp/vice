#!/bin/zsh -x

set -e

if [[ ! -e osx ]]; then
    echo must run from top-level vice directory
    exit 1
fi

if [[ ! -e ~/Downloads/vice ]]; then
    echo vice not found in Downloads
    exit 1
fi

if [[ ! -v VERSION ]]; then
    echo VERSION environment variable must be set
    exit 1
fi

cd osx

# start creating Vice.app    
rm -rf Vice.app
mkdir -p Vice.app/Contents/MacOS
cp Info.plist Vice.App/Contents
mv ~/Downloads/vice Vice.app/Contents/MacOS/vice
chmod +x Vice.app/Contents/MacOS/vice

# take care of the icons
mkdir -p icon.iconset
cp ../icons/tower-rounded-inset-16x16.png icon.iconset/icon_16x16.png
cp ../icons/tower-rounded-inset-32x32.png icon.iconset/icon_16x16@2.png
cp ../icons/tower-rounded-inset-32x32.png icon.iconset/icon_32x32.png
cp ../icons/tower-rounded-inset-64x64.png icon.iconset/icon_32x32@2.png
cp ../icons/tower-rounded-inset-64x64.png icon.iconset/icon_64x64.png
cp ../icons/tower-rounded-inset-128x128.png icon.iconset/icon_64x64@2.png
cp ../icons/tower-rounded-inset-128x128.png icon.iconset/icon_128x128.png
cp ../icons/tower-rounded-inset-256x256.png icon.iconset/icon_128x128@2.png
cp ../icons/tower-rounded-inset-256x256.png icon.iconset/icon_256x256.png
cp ../icons/tower-rounded-inset-512x512.png icon.iconset/icon_256x256@2.png
cp ../icons/tower-rounded-inset-512x512.png icon.iconset/icon_512x512.png
cp ../icons/tower-rounded-inset-1024x1024.png icon.iconset/icon_512x512@2.png
iconutil -c icns icon.iconset
mkdir -p Vice.app/Contents/Resources
cp icon.icns Vice.app/Contents/Resources

# sign the app
codesign -s "${APPLE_DEVELOPER_ID_APPLICATION}" -f -v --timestamp --options runtime Vice.app

# make a zip for notarization
zip -rv vice.zip Vice.app

# get the zip file notarized
xcrun altool --notarize-app \
      --primary-bundle-id org.pharr.vice \
      -u ${AC_USERNAME} \
      -p @env:APPLE_CODESIGN_PASSWORD \
      -f vice.zip > notarize.out

# via https://www.msweet.org/blog/2020-12-10-macos-notarization.html
uuid=`grep RequestUUID notarize.out | awk '{print $3}'`
notarizestatus="in progress"

while [[ "$notarizestatus" = "in progress" ]]; do
  echo "    $uuid: $notarizestatus"
  sleep 10
  echo "xcrun altool --notarization-info ..."
  xcrun altool --notarization-info $uuid -u ${AC_USERNAME} --password @env:APPLE_CODESIGN_PASSWORD > notarize.out
  cat notarize.out
  notarizestatus="`grep Status: notarize.out | cut -b14-`"
done

echo "    $uuid: $notarizestatus"

# notarized! staple the notarization
xcrun stapler staple Vice.app

# create dmg
/bin/rm -f Vice.dmg
~/create-dmg/create-dmg --volname "Vice-${VERSION}" \
           --volicon icon.icns \
           --icon-size 100 \
           --icon "Vice.app" 10 180 \
           --app-drop-link 350 180 \
           --window-size 570 420 \
           --background dmg-background.png  \
           Vice.dmg \
           Vice.app

rm vice.zip

# on altool fail, do:
# xcrun altool --notarization-history 0 --username ${AC_USERNAME} --password ${APPLE_CODESIGN_PASSWORD}
# then:
# xcrun altool --notarization-info {REQUESTUUID} --username ${AC_USERNAME} --password ${APPLE_CODESIGN_PASSWORD}
# to get URL for logs...

# upload it to github
mv Vice.dmg Vice-${VERSION}.dmg
#gh release upload ${VERSION} Vice-${VERSION}.dmg
