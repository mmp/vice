// whisper/whisper.go
// Local faster-whisper integration via Python

package whisper

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mmp/vice/log"
)

// TranscriptionResult represents the result of a transcription
type TranscriptionResult struct {
	Text                string    `json:"text"`
	Language            string    `json:"language"`
	LanguageProbability float64   `json:"language_probability"`
	Segments            []Segment `json:"segments"`
	Device              string    `json:"device"`
	ComputeType         string    `json:"compute_type"`
	TranscriptionTime   float64   `json:"transcription_time"`
	LoadTime            float64   `json:"load_time"`
}

// Segment represents a segment of transcribed audio
type Segment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

// WhisperWrapper wraps the Python smart_whisper functionality
type WhisperWrapper struct {
	pythonPath string
	scriptPath string
	logger     *log.Logger
	// chosen settings from Setup
	chosenDevice      string
	chosenComputeType string
}

// NewWhisperWrapper creates a new WhisperWrapper instance
func NewWhisperWrapper(logger *log.Logger) (*WhisperWrapper, error) {
	// Find Python executable
	pythonPath, err := findPython()
	if err != nil {
		return nil, fmt.Errorf("failed to find Python: %v", err)
	}

	// Get the directory where this Go file is located
	_, filename, _, _ := runtime.Caller(0)
	scriptDir := filepath.Dir(filename)
	scriptPath := filepath.Join(scriptDir, "smart_whisper.py")

	return &WhisperWrapper{
		pythonPath: pythonPath,
		scriptPath: scriptPath,
		logger:     logger,
	}, nil
}

// findPython finds the Python executable
func findPython() (string, error) {
	// Try common Python names
	pythonNames := []string{"python", "python3", "py"}

	for _, name := range pythonNames {
		path, err := exec.LookPath(name)
		if err == nil {
			// Test if it's a working Python installation
			cmd := exec.Command(path, "--version")
			if err := cmd.Run(); err == nil {
				return path, nil
			}
		}
	}

	return "", fmt.Errorf("no working Python installation found")
}

// Setup detects and configures the best available device (CUDA or CPU)
// and installs necessary libraries if needed
func (w *WhisperWrapper) Setup() error {
	w.logger.Info("Setting up Whisper environment...")

	// Create a Python script to handle setup
	setupScript := `
import sys
import subprocess
import os

def install_package(package):
    """Install a package using pip"""
    subprocess.check_call([sys.executable, "-m", "pip", "install", package])

def check_nvidia_gpu():
    """Check if NVIDIA GPU is available"""
    try:
        result = subprocess.run(['nvidia-smi'], capture_output=True, text=True)
        return result.returncode == 0
    except FileNotFoundError:
        return False

def check_cuda_available():
    """Check if CUDA is available in PyTorch"""
    try:
        import torch
        return torch.cuda.is_available()
    except ImportError:
        return False

def install_cuda_pytorch():
    """Install CUDA-enabled PyTorch"""
    print("Installing CUDA-enabled PyTorch...")
    cuda_torch_url = "torch torchvision torchaudio --index-url https://download.pytorch.org/whl/cu121"
    subprocess.check_call([sys.executable, "-m", "pip", "install"] + cuda_torch_url.split())

def setup_gpu_acceleration():
    """Setup GPU acceleration if possible"""
    print("Checking for GPU acceleration capabilities...")
    
    # Check if we have NVIDIA GPU
    has_nvidia = check_nvidia_gpu()
    print(f"NVIDIA GPU detected: {has_nvidia}")
    
    if not has_nvidia:
        print("No NVIDIA GPU found. Using CPU.")
        return "cpu", "int8"
    
    # Check if CUDA is available
    has_cuda = check_cuda_available()
    print(f"CUDA available: {has_cuda}")
    
    if not has_cuda:
        print("CUDA not available. Installing CUDA-enabled PyTorch...")
        try:
            install_cuda_pytorch()
            print("CUDA PyTorch installed successfully.")
            has_cuda = check_cuda_available()
        except Exception as e:
            print(f"Failed to install CUDA PyTorch: {e}")
            print("Falling back to CPU.")
            return "cpu", "int8"
    
    if has_cuda:
        print("GPU acceleration enabled!")
        return "cuda", "float16"
    else:
        print("GPU setup incomplete. Using CPU.")
        return "cpu", "int8"

def install_faster_whisper():
    """Install faster-whisper if not already installed"""
    try:
        import faster_whisper
        print("faster-whisper already installed.")
    except ImportError:
        print("Installing faster-whisper...")
        install_package("faster-whisper")
        print("faster-whisper installed successfully.")

if __name__ == "__main__":
    # Install faster-whisper
    install_faster_whisper()
    
    # Setup device
    device, compute_type = setup_gpu_acceleration()
    
    # Return results as JSON
    import json
    result = {
        "device": device,
        "compute_type": compute_type,
        "success": True
    }
    print("SETUP_RESULT:" + json.dumps(result))
`

	// Write setup script to temporary file
	tmpFile, err := os.CreateTemp("", "whisper_setup_*.py")
	if err != nil {
		return fmt.Errorf("failed to create temporary setup script: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(setupScript); err != nil {
		return fmt.Errorf("failed to write setup script: %v", err)
	}
	tmpFile.Close()

	// Run setup script
	cmd := exec.Command(w.pythonPath, tmpFile.Name())
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("setup failed: %v\nStderr: %s", err, stderr.String())
	}

	// Parse setup result
	output := stdout.String()
	if strings.Contains(output, "SETUP_RESULT:") {
		jsonStr := strings.Split(output, "SETUP_RESULT:")[1]
		var result map[string]interface{}
		if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
			return fmt.Errorf("failed to parse setup result: %v", err)
		}

		device := result["device"].(string)
		computeType := result["compute_type"].(string)
		w.chosenDevice = device
		w.chosenComputeType = computeType
		modelName := "tiny"
		if device == "cuda" {
			modelName = "large"
		}
		fmt.Printf("Whisper setup complete: device=%s compute_type=%s model=%s\n", device, computeType, modelName)
	} else {
		return fmt.Errorf("setup script did not return expected result")
	}

	return nil
}

// PreloadModel loads a Whisper model into cache so the first transcription is fast
func (w *WhisperWrapper) PreloadModel() error {
	modelName := "tiny"
	if w.chosenDevice == "cuda" {
		modelName = "large"
	}
	w.logger.Infof("Preloading Whisper model (%s)...", modelName)
	preloadScript := `
import time
from faster_whisper import WhisperModel

def main():
    try:
        # Try CUDA first, fall back to CPU
        try:
            import torch
            device = "cuda" if torch.cuda.is_available() else "cpu"
        except Exception:
            device = "cpu"

        compute_type = "float16" if device == "cuda" else "int8"
        t0 = time.time()
        model_name = "large" if device == "cuda" else "tiny"
        model = WhisperModel(model_name, device=device, compute_type=compute_type)
        print(f"PRELOAD_OK device={device} compute_type={compute_type} model={model_name} load_time={time.time()-t0:.2f}s")
    except Exception as e:
        print("PRELOAD_ERR:" + str(e))

if __name__ == "__main__":
    main()
`

	tmpFile, err := os.CreateTemp("", "whisper_preload_*.py")
	if err != nil {
		return fmt.Errorf("failed to create preload script: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.WriteString(preloadScript); err != nil {
		return fmt.Errorf("failed to write preload script: %v", err)
	}
	tmpFile.Close()

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(w.pythonPath, tmpFile.Name())
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("preload failed: %v\nStderr: %s", err, stderr.String())
	}
	out := stdout.String()
	if strings.Contains(out, "PRELOAD_OK") {
		w.logger.Info(out)
		return nil
	}
	return fmt.Errorf("preload unexpected output: %s", out)
}

// Transcribe transcribes a WAV file and returns the transcription as a string
func (w *WhisperWrapper) Transcribe(audioFilePath string) (*TranscriptionResult, error) {
	// Check if audio file exists
	if _, err := os.Stat(audioFilePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("audio file not found: %s", audioFilePath)
	}

	// Create transcription script
	transcribeScript := `
import sys
import json
import time
import os
from faster_whisper import WhisperModel

def check_cuda_available():
    """Check if CUDA is available in PyTorch"""
    try:
        import torch
        return torch.cuda.is_available()
    except ImportError:
        return False

def transcribe_audio(audio_file):
    """Transcribe audio file with automatic device detection"""
    # Determine best device and compute type
    cuda_available = check_cuda_available()
    
    if cuda_available:
        device = "cuda"
        compute_type = "float16"
    else:
        device = "cpu"
        compute_type = "int8"
    
    print(f"Using device: {device}, compute_type: {compute_type}")
    
    # Load model
    load_start = time.time()
    model_name = "large" if device == "cuda" else "tiny"
    model = WhisperModel(model_name, device=device, compute_type=compute_type)
    load_time = time.time() - load_start
    
    # Transcribe
    transcribe_start = time.time()
    segments, info = model.transcribe(audio_file)
    
    # Get transcription and timing
    transcription_parts = []
    segment_list = []
    
    for segment in segments:
        transcription_parts.append(segment.text)
        segment_list.append({
            "start": segment.start,
            "end": segment.end,
            "text": segment.text
        })
    
    transcription_time = time.time() - transcribe_start
    full_transcription = " ".join(transcription_parts)
    
    # Create result
    result = {
        "text": full_transcription,
        "language": info.language,
        "language_probability": info.language_probability,
        "segments": segment_list,
        "device": device,
        "compute_type": compute_type,
        "transcription_time": transcription_time,
        "load_time": load_time
    }
    
    return result

if __name__ == "__main__":
    if len(sys.argv) != 2:
        print("Usage: python script.py <audio_file>")
        sys.exit(1)
    
    audio_file = sys.argv[1]
    
    try:
        result = transcribe_audio(audio_file)
        print("TRANSCRIPTION_RESULT:" + json.dumps(result))
    except Exception as e:
        error_result = {
            "error": str(e),
            "success": False
        }
        print("TRANSCRIPTION_RESULT:" + json.dumps(error_result))
`

	// Write transcription script to temporary file
	tmpFile, err := os.CreateTemp("", "whisper_transcribe_*.py")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary transcription script: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(transcribeScript); err != nil {
		return nil, fmt.Errorf("failed to write transcription script: %v", err)
	}
	tmpFile.Close()

	// Run transcription script
	cmd := exec.Command(w.pythonPath, tmpFile.Name(), audioFilePath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("transcription failed: %v\nStderr: %s", err, stderr.String())
	}

	// Parse transcription result
	output := stdout.String()
	if strings.Contains(output, "TRANSCRIPTION_RESULT:") {
		jsonStr := strings.Split(output, "TRANSCRIPTION_RESULT:")[1]

		var result map[string]interface{}
		if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
			return nil, fmt.Errorf("failed to parse transcription result: %v", err)
		}

		// Check for error
		if errorMsg, hasError := result["error"]; hasError {
			return nil, fmt.Errorf("transcription error: %s", errorMsg)
		}

		// Convert to TranscriptionResult
		transcriptionResult := &TranscriptionResult{
			Text:                result["text"].(string),
			Language:            result["language"].(string),
			LanguageProbability: result["language_probability"].(float64),
			Device:              result["device"].(string),
			ComputeType:         result["compute_type"].(string),
			TranscriptionTime:   result["transcription_time"].(float64),
			LoadTime:            result["load_time"].(float64),
		}

		// Convert segments
		if segments, ok := result["segments"].([]interface{}); ok {
			for _, seg := range segments {
				segmentMap := seg.(map[string]interface{})
				segment := Segment{
					Start: segmentMap["start"].(float64),
					End:   segmentMap["end"].(float64),
					Text:  segmentMap["text"].(string),
				}
				transcriptionResult.Segments = append(transcriptionResult.Segments, segment)
			}
		}

		return transcriptionResult, nil
	} else {
		return nil, fmt.Errorf("transcription script did not return expected result")
	}
}

// TranscribeSimple is a convenience function that returns just the transcription text
func (w *WhisperWrapper) TranscribeSimple(audioFilePath string) (string, error) {
	result, err := w.Transcribe(audioFilePath)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

// Settings returns the selected device and compute type
func (w *WhisperWrapper) Settings() (device, computeType string) {
	return w.chosenDevice, w.chosenComputeType
}
