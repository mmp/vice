// Package autowhisper provides a simple cross-OS API over whisper.cpp Go bindings.
//
// It automatically converts WAV input to 16 kHz mono and runs transcription
// without requiring users to set C_INCLUDE_PATH or LIBRARY_PATH. Consumers only
// need to provide a path to a whisper GGML model and a WAV file path.
//
// Example:
//
//	text, err := autowhisper.TranscribeFile("/path/to/ggml-tiny.bin", "/path/to/audio.wav", autowhisper.Options{ Language: "auto" })
//	if err != nil { log.Fatal(err) }
//	fmt.Println(text)
package autowhisper
