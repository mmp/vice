package eram

import "github.com/mmp/vice/pkg/renderer"

func (ep *ERAMPane) ERAMFont() *renderer.Font{
	return renderer.GetFont(renderer.FontIdentifier{Name: "ERAM", Size: 10})
	// return renderer.GetDefaultFont()
}