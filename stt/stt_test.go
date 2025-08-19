// stt/stt_test.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stt

import (
	"testing"
)

func TestTranscribe(t *testing.T) {
	// Test with empty audio data
	audio := &AudioData{
		SampleRate: 44100,
		Channels:   1,
		Data:       []int16{},
	}
	
	_, err := Transcribe(audio)
	if err == nil {
		t.Error("Expected error for empty audio data")
	}
	
	// Test with short audio data (less than 0.5 seconds)
	shortAudio := &AudioData{
		SampleRate: 44100,
		Channels:   1,
		Data:       make([]int16, 22049), // Just under 0.5 seconds at 44.1kHz
	}
	
	_, err = Transcribe(shortAudio)
	if err == nil {
		t.Error("Expected error for audio too short to transcribe")
	}
	
	// Test with valid audio data
	validAudio := &AudioData{
		SampleRate: 44100,
		Channels:   1,
		Data:       make([]int16, 44100), // 1 second at 44.1kHz
	}
	
	transcription, err := Transcribe(validAudio)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if transcription == "" {
		t.Error("Expected non-empty transcription")
	}
}

func TestConvertToWAV(t *testing.T) {
	audio := &AudioData{
		SampleRate: 44100,
		Channels:   1,
		Data:       []int16{1000, 2000, 3000, 4000},
	}
	
	wavData, err := audio.ConvertToWAV()
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	
	if len(wavData) == 0 {
		t.Error("Expected non-empty WAV data")
	}
	
	// Check WAV header
	if len(wavData) < 44 {
		t.Error("WAV data too short for header")
	}
	
	// Check RIFF header
	if string(wavData[0:4]) != "RIFF" {
		t.Error("Invalid RIFF header")
	}
	
	if string(wavData[8:12]) != "WAVE" {
		t.Error("Invalid WAVE header")
	}
}