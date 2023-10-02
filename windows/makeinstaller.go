package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
	Version       string
	ResourceFiles []InstallFile
	AudioFiles    []InstallFile
	FontFiles     []InstallFile
	VideoMapFiles []InstallFile
	ScenarioFiles []InstallFile
}

func main() {
	tag, err := os.ReadFile("tag.txt")
	if err != nil {
		cmd := exec.Command("git", "describe", "--tags", "--abbrev=0")
		var out bytes.Buffer
		cmd.Stdout = &out
		err := cmd.Run()
		if err != nil {
			panic(err)
		}
		tag = out.String()
	}

	tag = strings.TrimSpace(tag)
	if tag[0] != 'v' {
		panic(tag)
	}

	var r Release
	r.Version = tag[1:]

	initFiles := func(globs ...string) []InstallFile {
		var files []InstallFile

		for _, glob := range globs {
			matches, err := filepath.Glob(glob)
			if err != nil {
				panic(err)
			}

			for _, f := range matches {
				id := filepath.Base(f)
				id, _, _ = strings.Cut(id, ".")
				id = strings.Map(func(ch rune) rune {
					if unicode.IsLetter(ch) || unicode.IsDigit(ch) {
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

	r.ResourceFiles = initFiles("resources/*.zst", "resources/*json")
	r.AudioFiles = initFiles("resources/audio/*.wav")
	r.FontFiles = initFiles("resources/fonts/*.zst")
	r.VideoMapFiles = initFiles("resources/videomaps/*.zst")
	r.ScenarioFiles = initFiles("resources/scenarios/*.json")

	tmpl, err := template.New("installer.wxs").Parse(xmlTemplate)
	if err != nil {
		panic(err)
	}

	tmpl.Execute(os.Stdout, r)
}

const xmlTemplate = `
<?xml version='1.0' encoding='utf-8'?>
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

    <MajorUpgrade DowngradeErrorMessage="A later version of Vice is already installed. Setup will now exit." />

    <MediaTemplate EmbedCab="yes" />

    <Directory Id="TARGETDIR" Name="SourceDir">
      <Directory Id="ProgramFilesFolder">
        <Directory Id="INSTALLFOLDER" Name="Vice">
          <Component Id="ViceExe" Guid='A10E3C66-BA55-406A-B4E2-586D7108D622'>
            <File KeyPath="yes" Name="Vice.exe" Source="Vice.exe"></File>
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
          <Directory Id="ResourcesFolder" Name="resources">
            <Component Id="ResourcesFilesId" Guid="b5e58c58-4d43-4613-91f7-d55fb9fdde91">
{{range .ResourceFiles}}                <File Id="{{.Id}}" Source="{{.Source}}" {{if .KeyPath}}KeyPath="yes" {{end}}/>
{{end}}
            </Component>
            <Directory Id="AudioFolder" Name="audio">
              <Component Id="AudioId" Guid="e14412f8-b08c-45d3-a753-706bf5f560f5">
{{range .AudioFiles}}                <File Id="{{.Id}}" Source="{{.Source}}" {{if .KeyPath}}KeyPath="yes" {{end}}/>
{{end}}
              </Component>
            </Directory>
            <Directory Id="MyFontsFolder" Name="fonts">
              <Component Id="FontsId" Guid="263928e7-8110-4fae-8030-2ee477cb0595">
{{range .FontFiles}}                <File Id="{{.Id}}" Source="{{.Source}}" {{if .KeyPath}}KeyPath="yes" {{end}}/>
{{end}}
              </Component>
            </Directory>
            <Directory Id="VideoMapsFolder" Name="videomaps">
              <Component Id="VideoMapsId" Guid="f120781f-c141-4b3e-bd72-8ca98048be48">
{{range .VideoMapFiles}}                <File Id="{{.Id}}" Source="{{.Source}}" {{if .KeyPath}}KeyPath="yes" {{end}}/>
{{end}}
              </Component>
            </Directory>
            <Directory Id="ScenariosFolder" Name="scenarios">
              <Component Id="ScenariosId" Guid="3072033b-c670-4e11-b941-2ea9bf892a83">
{{range .ScenarioFiles}}                <File Id="{{.Id}}" Source="{{.Source}}" {{if .KeyPath}}KeyPath="yes" {{end}}/>
{{end}}
              </Component>
            </Directory>
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
      <ComponentRef Id="SDLDLL" />
      <ComponentRef Id="gccseh" />
      <ComponentRef Id="libstdcpp" />
      <ComponentRef Id="ResourcesFilesId" />
      <ComponentRef Id="AudioId" />
      <ComponentRef Id="FontsId" />
      <ComponentRef Id="ScenariosId" />
      <ComponentRef Id="VideoMapsId" />
      <ComponentRef Id="ApplicationShortcut" />
      <ComponentRef Id="ApplicationShortcutDesktop" />
    </Feature>
  </Product>
</Wix>
`
