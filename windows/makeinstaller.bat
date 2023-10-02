go run windows/makeinstaller.go > windows/installer.wxs
candle.exe windows/installer.wxs
light.exe -ext WixUIExtension installer.wixobj
move installer.msi Vice-installer.msi
