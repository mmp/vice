#!/bin/sh

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
/bin/mv icon.icns Vice.app/Contents/Resources

# get the code signed and create a dmg file
gon -log-level=info  gon-config.hcl

# upload it to github
mv Vice.dmg Vice-${VERSION}.dmg
#gh release create ${VERSION}
gh release upload ${VERSION} Vice-${VERSION}.dmg
