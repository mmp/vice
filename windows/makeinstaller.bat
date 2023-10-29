go run windows\makeinstaller.go > installer.wxs
dir
candle.exe installer.wxs
light.exe -ext WixUIExtension installer.wixobj
move installer.msi Vice-installer.msi
