package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"unicode"
)

type InstallFile struct {
	Source  string
	Id      string
	KeyPath bool
}

type Release struct {
	Version   string
	FontFiles []InstallFile
}

func getLatestGitTag() string {
	cmd := exec.Command("git", "describe", "--tags", "--abbrev=0")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		panic(err)
	}
	return strings.TrimSpace(out.String())
}

// wixVersion converts a git tag like "v0.14.4" or "v0.14.4-beta2" into a
// WiX-compatible four-field version. Beta N of X.Y.Z uses N as the fourth
// field; the final X.Y.Z uses one more than the highest beta number that
// was ever tagged for X.Y.Z (or 1 if none were).
func wixVersion(tag string) string {
	re := regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(?:-beta(\d+))?$`)
	m := re.FindStringSubmatch(tag)
	if m == nil {
		panic("unexpected tag format: " + tag)
	}
	if m[4] != "" {
		return fmt.Sprintf("%s.%s.%s.%s", m[1], m[2], m[3], m[4])
	}
	return fmt.Sprintf("%s.%s.%s.%d", m[1], m[2], m[3], maxBetaNumber(m[1], m[2], m[3])+1)
}

// maxBetaNumber returns the highest N such that a tag vX.Y.Z-betaN exists in
// the repo, or 0 if none do.
func maxBetaNumber(x, y, z string) int {
	prefix := fmt.Sprintf("v%s.%s.%s-beta", x, y, z)
	cmd := exec.Command("git", "tag", "-l", prefix+"*")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		panic(err)
	}
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `(\d+)$`)
	max := 0
	for line := range strings.SplitSeq(out.String(), "\n") {
		m := re.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err == nil && n > max {
			max = n
		}
	}
	return max
}

func main() {
	tag := getLatestGitTag()

	var r Release
	r.Version = wixVersion(tag)

	initFiles := func(globs ...string) []InstallFile {
		var files []InstallFile

		for _, glob := range globs {
			matches, err := filepath.Glob(glob)
			if err != nil {
				panic(err)
			}

			for _, f := range matches {
				id := filepath.Base(f)
				if unicode.IsDigit(rune(id[0])) {
					// Can't start with a digit
					id = "_" + id
				}
				id = strings.Map(func(ch rune) rune {
					if unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '.' {
						return ch
					}
					return '_'
				}, id)
				files = append(files, InstallFile{
					Source: strings.ReplaceAll(f, "/", `\\`),
					Id:     id})
			}
		}

		sort.Slice(files, func(i, j int) bool { return files[i].Source < files[j].Source })
		files[0].KeyPath = true

		return files
	}

	r.FontFiles = initFiles("fonts/*.zst")

	tmpl, err := template.New("installer.wxs").Parse(xmlTemplate)
	if err != nil {
		panic(err)
	}

	if err := tmpl.Execute(os.Stdout, r); err != nil {
		panic(err)
	}

}

const xmlTemplate = `<?xml version='1.0' encoding='utf-8'?>
<Wix xmlns='http://schemas.microsoft.com/wix/2006/wi'>
  <Product Name='Vice'
	   Manufacturer='Matt Pharr'
	   Id='*'
	   UpgradeCode='A10E3C66-BA55-406A-B4E2-586D7108D622'
	   Language='1033'
	   Codepage='1252'
	   Version='{{.Version}}'
	   >
    <Package Id='*'
    	     Keywords='Installer'
	     Description="Vice Installer"
	     Manufacturer='Matt Pharr'
	     InstallerVersion='100'
	     Languages='1033'
	     Compressed='yes'
	     SummaryCodepage='1252'
	     />

    <MajorUpgrade AllowSameVersionUpgrades="yes" DowngradeErrorMessage="A later version of Vice is already installed. Setup will now exit." />

    <MediaTemplate EmbedCab="yes" />

    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="ProgramFilesFolder">
        <Directory Id="INSTALLFOLDER" Name="Vice">
          <Component Id="ViceExe" Guid='A10E3C66-BA55-406A-B4E2-586D7108D622'>
            <File KeyPath="yes" Name="Vice.exe" Source="Vice.exe"></File>
          </Component>
          <Component Id="Crc2viceExe" Guid='c1d2e3f4-a5b6-4789-9c0d-1e2f3a4b5c6d'>
            <File KeyPath="yes" Name="crc2vice.exe" Source="crc2vice.exe"></File>
          </Component>
          <Component Id="Dat2viceExe" Guid='d2e3f4a5-b6c7-489a-9d0e-1f2a3b4c5d6e'>
            <File KeyPath="yes" Name="dat2vice.exe" Source="dat2vice.exe"></File>
          </Component>
          <Component Id="SDLDLL" Guid='85535501-4016-47c4-9466-846df4cf49a5'>
            <File KeyPath="yes" Source="windows/SDL2.dll"></File>
          </Component>
          <Component Id="gccseh" Guid='68f22a6b-1710-4987-820c-b5cbad791dbe'>
            <File KeyPath="yes" Source="windows/libgcc_s_seh-1.dll"></File>
          </Component>
          <Component Id="libstdcpp" Guid='a7080cc5-8ddf-45b9-bf09-466652cc8b06'>
            <File KeyPath="yes" Source="windows/libstdc++-6.dll"></File>
          </Component>
          <Component Id="libwinpthread" Guid='b2c3d4e5-f6a7-4b89-0c1d-2e3f4a5b6c7d'>
            <File KeyPath="yes" Source="windows/libwinpthread-1.dll"></File>
          </Component>
          <Component Id="sherpaonnx" Guid='f3a1b2c4-d5e6-4f78-9a0b-1c2d3e4f5a6b'>
            <File KeyPath="yes" Source="windows/sherpa-onnx-c-api.dll"></File>
          </Component>
          <Component Id="onnxruntime" Guid='a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d'>
            <File KeyPath="yes" Source="windows/onnxruntime.dll"></File>
          </Component>
          <Directory Id="MyFontsFolder" Name="fonts">
            <Component Id="FontsId" Guid="333b7858-8503-4310-b039-e1341613dada">
{{range .FontFiles}}                <File Id="{{.Id}}" Source="{{.Source}}" {{if .KeyPath}}KeyPath="yes" {{end}}/>
{{end}}
            </Component>
          </Directory>
        </Directory>

	<Directory Id="ProgramMenuFolder">
	  <Directory Id="ApplicationProgramsFolder" Name="Vice"/>
	</Directory>

	<Directory Id="DesktopFolder" Name="Desktop">
          <Component Id="ApplicationShortcutDesktop" Guid="*">
            <Shortcut Id="ApplicationDesktopShortcut"
                      Name="Vice ATC"
                      Description="Vice ATC Simulator"
                      Target="[#Vice.exe]"
                      WorkingDirectory="INSTALLFOLDER"/>
            <RemoveFolder Id="DesktopFolder" On="uninstall"/>
            <RegistryValue
                Root="HKCU"
                Key="Software\Matt Pharr\Vice"
                Name="installed"
                Type="integer"
                Value="1"
                KeyPath="yes"/>
          </Component>
	</Directory>

      </Directory>
    </Directory>

    <UI Id="UserInterface">
      <Property Id="WIXUI_INSTALLDIR" Value="TARGETDIR" />
      <Property Id="WixUI_Mode" Value="Custom" />

      <TextStyle Id="WixUI_Font_Normal" FaceName="Tahoma" Size="8" />
      <TextStyle Id="WixUI_Font_Bigger" FaceName="Tahoma" Size="9" Bold="yes" />
      <TextStyle Id="WixUI_Font_Title"  FaceName="Tahoma" Size="9" Bold="yes" />

      <Property Id="DefaultUIFont" Value="WixUI_Font_Normal" />

      <DialogRef Id="ProgressDlg" />
      <DialogRef Id="ErrorDlg" />
      <DialogRef Id="FilesInUse" />
      <DialogRef Id="FatalError" />
      <DialogRef Id="UserExit" />

      <Publish Dialog="ExitDialog" Control="Finish" Event="EndDialog" Value="Return" Order="999">1</Publish>
      <Publish Dialog="WelcomeDlg" Control="Next" Event="EndDialog" Value="Return" Order="2"></Publish>

    </UI>
    <UIRef Id="WixUI_Common" />

    <WixVariable Id="WixUIDialogBmp" Value="windows\dialog.bmp" />

    <DirectoryRef Id="ApplicationProgramsFolder">
      <Component Id="ApplicationShortcut" Guid='93fae481-57c0-499a-84c2-517067428f13'>
        <Shortcut Id="ApplicationStartMenuShortcut"
                  Name="Vice"
                  Description="ATC Simulator"
                  Target="[#Vice.exe]"
                  WorkingDirectory="INSTALLFOLDER"/>
        <RemoveFolder Id="ApplicationProgramsFolder" On="uninstall"/>
        <RegistryValue Root="HKCU" Key="Software\Matt Pharr\Vice" Name="installed" Type="integer" Value="1" KeyPath="yes"/>
      </Component>
    </DirectoryRef>

    <Feature Id="MyFeature">
      <ComponentRef Id="ViceExe" />
      <ComponentRef Id="Crc2viceExe" />
      <ComponentRef Id="Dat2viceExe" />
      <ComponentRef Id="SDLDLL" />
      <ComponentRef Id="gccseh" />
      <ComponentRef Id="libstdcpp" />
      <ComponentRef Id="libwinpthread" />
      <ComponentRef Id="sherpaonnx" />
      <ComponentRef Id="onnxruntime" />
      <ComponentRef Id="FontsId" />
      <ComponentRef Id="ApplicationShortcut" />
      <ComponentRef Id="ApplicationShortcutDesktop" />
    </Feature>
  </Product>
</Wix>
`
