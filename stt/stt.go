// stt/stt.go
// Copyright(c) 2022-2024 vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stt

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	vclog "github.com/mmp/vice/log"
)

// AudioData represents audio data in memory
type AudioData struct {
	SampleRate int
	Channels   int
	Data       []int16 // PCM audio data
}

var (
	whisperOnce    sync.Once
	whisperInitErr error
	whisperWrap    *whisperWrapper
)

// Initializes the wrapper and preloads model
func Prepare(lg *vclog.Logger) error {
	whisperOnce.Do(func() {
		ww, err := newWhisperWrapper(lg)
		if err != nil {
			whisperInitErr = fmt.Errorf("init whisper: %w", err)
			return
		}
		if err := ww.Setup(); err != nil {
			whisperInitErr = fmt.Errorf("setup whisper: %w", err)
			return
		}
		// Preload model
		if err := ww.PreloadModel(); err != nil {
			lg.Warnf("Whisper preload failed (continuing): %v", err)
		}
		whisperWrap = ww
		if whisperWrap != nil {
			dev, ct := whisperWrap.Settings()
			modelName := "tiny"
			if dev == "cuda" { // Use a large model if CUDA is available
				modelName = "large"
			}
			lg.Infof("Whisper model ready: device=%s compute_type=%s model=%s", dev, ct, modelName)
		}
	})
	return whisperInitErr
}

// Transcribe takes audio data in memory and returns the transcribed text
func Transcribe(audio *AudioData) (string, error) {
	if len(audio.Data) == 0 {
		return "", fmt.Errorf("no audio data provided")
	}

	if whisperWrap == nil {
		return "", fmt.Errorf("whisper wrapper not initialized - call Prepare() first")
	}

	// Write audio to a temp WAV file for the Python wrapper
	wav, err := audio.ConvertToWAV()
	if err != nil {
		return "", fmt.Errorf("convert to WAV: %w", err)
	}
	tmp, err := os.CreateTemp("", "vice_ptt_*.wav")
	if err != nil {
		return "", fmt.Errorf("create temp wav: %w", err)
	}
	if _, err := bytes.NewBuffer(wav).WriteTo(tmp); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", fmt.Errorf("write wav: %w", err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	// Transcribe
	text, err := whisperWrap.TranscribeSimple(tmp.Name())
	if err != nil {
		return "", err
	}
	return text, nil
}

// ConvertToWAV converts audio data to WAV format in memory
func (audio *AudioData) ConvertToWAV() ([]byte, error) {
	var buf bytes.Buffer

	// WAV header
	header := make([]byte, 44)

	// RIFF header
	copy(header[0:4], []byte("RIFF"))
	binary.LittleEndian.PutUint32(header[4:8], uint32(36+len(audio.Data)*2)) // File size
	copy(header[8:12], []byte("WAVE"))

	// fmt chunk
	copy(header[12:16], []byte("fmt "))
	binary.LittleEndian.PutUint32(header[16:20], 16) // fmt chunk size
	binary.LittleEndian.PutUint16(header[20:22], 1)  // PCM format
	binary.LittleEndian.PutUint16(header[22:24], uint16(audio.Channels))
	binary.LittleEndian.PutUint32(header[24:28], uint32(audio.SampleRate))
	binary.LittleEndian.PutUint32(header[28:32], uint32(audio.SampleRate*audio.Channels*2)) // Byte rate
	binary.LittleEndian.PutUint16(header[32:34], uint16(audio.Channels*2))                  // Block align
	binary.LittleEndian.PutUint16(header[34:36], 16)                                        // Bits per sample

	// data chunk
	copy(header[36:40], []byte("data"))
	binary.LittleEndian.PutUint32(header[40:44], uint32(len(audio.Data)*2)) // Data size

	// Write header
	buf.Write(header)

	// Write audio data
	for _, sample := range audio.Data {
		binary.Write(&buf, binary.LittleEndian, sample)
	}

	return buf.Bytes(), nil
}

// ---- Merged Whisper wrapper with persistent process ----

type whisperWrapper struct {
	pythonPath        string
	logger            *vclog.Logger
	chosenDevice      string
	chosenComputeType string
	// persistent process
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
	reader *bufio.Reader
}

type Segment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

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

func newWhisperWrapper(lg *vclog.Logger) (*whisperWrapper, error) {
	pythonPath, err := findPython()
	if err != nil {
		return nil, err
	}
	return &whisperWrapper{pythonPath: pythonPath, logger: lg}, nil
}

func findPython() (string, error) {
	names := []string{"python", "python3", "py"}
	for _, n := range names {
		p, err := exec.LookPath(n)
		if err == nil {
			if err := exec.Command(p, "--version").Run(); err == nil {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("no working Python installation found")
}

func (w *whisperWrapper) Setup() error {
	setupScript := `
import sys, subprocess, json

def install_package(pkg):
    subprocess.check_call([sys.executable, "-m", "pip", "install", pkg])

def check_nvidia_gpu():
    try:
        result = subprocess.run(['nvidia-smi'], capture_output=True, text=True)
        return result.returncode == 0
    except FileNotFoundError:
        return False

def check_cuda_available():
    try:
        import torch
        return torch.cuda.is_available()
    except ImportError:
        return False

def install_cuda_pytorch():
    url = "torch torchvision torchaudio --index-url https://download.pytorch.org/whl/cu121"
    subprocess.check_call([sys.executable, "-m", "pip", "install"] + url.split())

def setup_gpu():
    if not check_nvidia_gpu():
        return "cpu", "int8"
    if not check_cuda_available():
        try:
            install_cuda_pytorch()
        except Exception:
            return "cpu", "int8"
    try:
        import torch
        if torch.cuda.is_available():
            return "cuda", "float16"
    except Exception:
        pass
    return "cpu", "int8"

def main():
    try:
        install_package("faster-whisper")
    except Exception:
        pass
    device, compute = setup_gpu()
    print("SETUP_RESULT:" + json.dumps({"device": device, "compute_type": compute, "success": True}))

if __name__ == "__main__":
    main()
`

	tmp, err := os.CreateTemp("", "whisper_setup_*.py")
	if err != nil {
		return fmt.Errorf("create setup script: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(setupScript); err != nil {
		return fmt.Errorf("write setup script: %w", err)
	}
	_ = tmp.Close()

	var out, er bytes.Buffer
	cmd := exec.Command(w.pythonPath, tmp.Name())
	cmd.Stdout = &out
	cmd.Stderr = &er
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("setup failed: %w stderr: %s", err, er.String())
	}
	s := out.String()
	if !strings.Contains(s, "SETUP_RESULT:") {
		return fmt.Errorf("setup: unexpected output: %s", s)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(strings.SplitN(s, "SETUP_RESULT:", 2)[1]), &res); err != nil {
		return fmt.Errorf("parse setup json: %w", err)
	}
	w.chosenDevice = res["device"].(string)
	w.chosenComputeType = res["compute_type"].(string)
	return nil
}

func (w *whisperWrapper) PreloadModel() error {
	modelName := "tiny"
	if w.chosenDevice == "cuda" {
		modelName = "large"
	}
	w.logger.Infof("Preloading Whisper model (%s)...", modelName)

	persistent := `
import sys, json, time, os, unicodedata, re
from faster_whisper import WhisperModel

model = None
device = None
compute_type = None
model_name = None

# -------------------- digit expansion + letters-only -----------------------

DIGIT_MAP_ICAO = {
    '0':'zero','1':'wun','2':'too','3':'tree','4':'fower',
    '5':'fife','6':'six','7':'seven','8':'ait','9':'niner'
}

# Replace all punctuation with spaces, then expand digit runs to ICAO words
def expand_digits_to_words(s: str) -> str:
    # 1) Turn punctuation into spaces so we never lose word boundaries
    # Includes common ASCII and unicode dashes
    s = re.sub(r"[,\.\-–—:;\/\\\(\)\[\]\{\}!?\+_=*\"'<>|]", " ", s)
    # 2) Collapse any weird whitespace runs early (optional but nice)
    s = re.sub(r"\s+", " ", s).strip()

    # 3) Per-digit expansion for any remaining digit runs
    def repl(match):
        return " ".join(DIGIT_MAP_ICAO[ch] for ch in match.group(0))
    return re.sub(r"\d+", repl, s)

def letters_only(s: str, ascii_only: bool = True, lowercase: bool = False) -> str:
    """
    Keep only letters and spaces (no punctuation, ever).
    Spaces are always preserved (multiple spaces collapsed to one).
    """
    out = []
    for ch in s:
        if ascii_only:
            is_letter = ch.isascii() and ch.isalpha()
        else:
            is_letter = unicodedata.category(ch).startswith('L')

        if is_letter:
            out.append(ch.lower() if lowercase else ch)
        elif ch.isspace():
            out.append(' ')
        # else: drop punctuation and everything else

    # Collapse runs of whitespace and strip
    return ' '.join(''.join(out).split())

def get_filter_cfg():
    # Always keep spaces; no punctuation is kept.
    ascii_only = os.getenv("ONLY_LETTERS_ASCII", "1") == "1"
    lowercase   = os.getenv("ONLY_LETTERS_LOWER", "0") == "1"
    return ascii_only, lowercase

# --------------------------------------------------------------------------

def check_cuda():
    try:
        import torch
        return torch.cuda.is_available()
    except ImportError:
        return False

def load_model():
    global model, device, compute_type, model_name
    env_dev = os.getenv("WHISPER_DEVICE")
    env_comp = os.getenv("WHISPER_COMPUTE_TYPE")
    env_model = os.getenv("WHISPER_MODEL")
    if env_dev and env_comp and env_model:
        device, compute_type, model_name = env_dev, env_comp, env_model
    else:
        device = "cuda" if check_cuda() else "cpu"
        compute_type = "float16" if device == "cuda" else "int8"
        model_name = "large" if device == "cuda" else "tiny"
    t0 = time.time()
    model = WhisperModel(model_name, device=device, compute_type=compute_type)
    print(f"MODEL_LOADED device={device} compute_type={compute_type} model={model_name} load_time={time.time()-t0:.2f}s", flush=True)

def transcribe(path):
    if model is None:
        return {"error": "model not loaded"}

    ascii_only, lowercase = get_filter_cfg()

    t0 = time.time()
    segs, info = model.transcribe(path)
    parts = []
    seg_list = []

    for s in segs:
        raw = s.text or ""
        expanded = expand_digits_to_words(raw)  # ICAO digit words, punctuation→spaces
        cleaned  = letters_only(expanded, ascii_only=ascii_only, lowercase=True)
        if not cleaned:
            continue
        parts.append(cleaned)
        seg_list.append({"start": s.start, "end": s.end, "text": cleaned})

    return {
        "text": " ".join(parts),  # spaces always kept
        "language": (info.language or "").lower(),
        "language_probability": info.language_probability,
        "segments": seg_list,
        "device": device,
        "compute_type": compute_type,
        "transcription_time": time.time()-t0,
    }

def main():
    load_model()
    while True:
        line = sys.stdin.readline()
        if not line:
            break
        line = line.strip()
        if line.startswith("TRANSCRIBE:"):
            p = line.split(":", 1)[1]
            print("RESULT:" + json.dumps(transcribe(p)), flush=True)
        elif line == "QUIT":
            break

if __name__ == "__main__":
    main()
`

	// start persistent process
	tmp, err := os.CreateTemp("", "whisper_persist_*.py")
	if err != nil {
		return fmt.Errorf("create persistent script: %w", err)
	}
	if _, err := tmp.WriteString(persistent); err != nil {
		return fmt.Errorf("write persistent: %w", err)
	}
	_ = tmp.Close()

	w.cmd = exec.Command(w.pythonPath, tmp.Name())
	env := os.Environ()
	env = append(env, "WHISPER_DEVICE="+w.chosenDevice)
	env = append(env, "WHISPER_COMPUTE_TYPE="+w.chosenComputeType)
	env = append(env, "WHISPER_MODEL="+modelName)
	w.cmd.Env = env

	stdin, err := w.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	w.stdin = stdin
	stdout, err := w.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	w.stdout = stdout
	w.reader = bufio.NewReader(stdout)
	w.stderr, _ = w.cmd.StderrPipe()

	if err := w.cmd.Start(); err != nil {
		return fmt.Errorf("start python: %w", err)
	}
	// wait for model loaded line
	for {
		line, err := w.reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("wait model: %w", err)
		}
		ls := strings.TrimSpace(line)
		w.logger.Info("Whisper: " + ls)
		if strings.HasPrefix(ls, "MODEL_LOADED") {
			break
		}
	}
	return nil
}

func (w *whisperWrapper) Transcribe(path string) (*TranscriptionResult, error) {
	if w.cmd == nil || w.stdin == nil {
		return nil, fmt.Errorf("persistent process not started")
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf("audio file not found: %s", path)
	}
	if _, err := io.WriteString(w.stdin, "TRANSCRIBE:"+path+"\n"); err != nil {
		return nil, fmt.Errorf("send cmd: %w", err)
	}
	line, err := w.reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("read result: %w", err)
	}
	ls := strings.TrimSpace(line)
	if !strings.HasPrefix(ls, "RESULT:") {
		return nil, fmt.Errorf("unexpected: %s", ls)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(ls[7:]), &m); err != nil {
		return nil, fmt.Errorf("parse result: %w", err)
	}
	res := &TranscriptionResult{
		Text:                m["text"].(string),
		Language:            m["language"].(string),
		LanguageProbability: m["language_probability"].(float64),
		Device:              m["device"].(string),
		ComputeType:         m["compute_type"].(string),
		TranscriptionTime:   m["transcription_time"].(float64),
	}
	if segs, ok := m["segments"].([]interface{}); ok {
		for _, s := range segs {
			mm := s.(map[string]interface{})
			res.Segments = append(res.Segments, Segment{Start: mm["start"].(float64), End: mm["end"].(float64), Text: mm["text"].(string)})
		}
	}
	return res, nil
}

func (w *whisperWrapper) TranscribeSimple(path string) (string, error) {
	r, err := w.Transcribe(path)
	if err != nil {
		return "", err
	}
	return r.Text, nil
}

func (w *whisperWrapper) Settings() (string, string) { return w.chosenDevice, w.chosenComputeType }
