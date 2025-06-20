package radar

import "fmt"

const ( // interim alt types
	Normal = iota
	Procedure
	Local
)

func FormatAltitude[T ~int | ~float32](alt T) string { // should this go in pkg/util/generic.go?
	return fmt.Sprintf("%03v", int(alt+50)/100)
}
