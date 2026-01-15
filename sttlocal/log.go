package sttlocal

import "fmt"

// logLocalStt logs local STT processing details.
// To disable logging, comment out the fmt.Printf line.
func logLocalStt(format string, args ...any) {
	fmt.Printf("[local-stt] "+format+"\n", args...)
}
