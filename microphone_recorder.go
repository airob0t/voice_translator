package main

import (
	"context"
	"io"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

// detectAudioDevice attempts to find a working audio input device
func detectAudioDevice() string {
	var ffmpegCmd string
	var inputFormat string
	var devices []string

	if runtime.GOOS == "windows" {
		ffmpegCmd = ".\\ffmpeg.exe"
		inputFormat = "dshow"
		devices = []string{"audio=麦克风", "audio=Microphone"}
	} else {
		ffmpegCmd = "ffmpeg"
		inputFormat = "avfoundation"
		devices = []string{":default", ":0", ":1", ":2"}
	}

	for _, device := range devices {
		safeV(2).Infof("Testing audio device: %s", device)
		// Quick test to see if device works
		cmd := exec.Command(ffmpegCmd,
			"-f", inputFormat,
			"-i", device,
			"-t", "0.1", // Very short test
			"-f", "null",
			"-loglevel", "error",
			"-")

		if err := cmd.Run(); err == nil {
			safeInfof("Found working audio device: %s", device)
			return device
		}
	}

	safeWarning("No working audio device found, using default")
	if runtime.GOOS == "windows" {
		return "audio=麦克风"
	}
	return ":default"
}

// MicrophoneRecorder provides microphone input using FFmpeg
type MicrophoneRecorder struct {
	cmd         *exec.Cmd
	stdout      io.ReadCloser
	mutex       sync.Mutex
	closed      bool
	audioDevice string
}

// NewMicrophoneRecorder creates a new FFmpeg-based microphone recorder
func NewMicrophoneRecorder() (*MicrophoneRecorder, error) {
	return NewMicrophoneRecorderWithDevice("")
}

// NewMicrophoneRecorderWithDevice creates a new FFmpeg-based microphone recorder with a specific device
func NewMicrophoneRecorderWithDevice(audioDevice string) (*MicrophoneRecorder, error) {
	recorder := &MicrophoneRecorder{}
	recorder.audioDevice = audioDevice

	var ffmpegCmd string
	var inputFormat string

	if runtime.GOOS == "windows" {
		ffmpegCmd = ".\\ffmpeg.exe"
		inputFormat = "dshow"
		if audioDevice == "" {
			audioDevice = "audio=麦克风"
		}
	} else {
		ffmpegCmd = "ffmpeg"
		inputFormat = "avfoundation"
		if audioDevice == "" {
			audioDevice = ":1"
		}
	}
	safeInfof("Using audio device: %s", audioDevice)

	// FFmpeg command to capture microphone input
	// Windows: -f dshow: use DirectShow for audio capture
	// macOS: -f avfoundation: use AVFoundation for audio capture
	// -f s16le: 16-bit little-endian PCM, translator only support 16bit
	// -ar 16000: sample rate 16kHz
	// -ac 1: mono (1 channel)
	// -: output to stdout
	recorder.cmd = exec.Command(ffmpegCmd,
		"-f", inputFormat,
		"-i", audioDevice,
		"-f", "s16le", // 输出参数
		"-ar", "16000",
		"-ac", "1",
		"-loglevel", "warning",
		"-")

	var err error
	recorder.stdout, err = recorder.cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	if err := recorder.cmd.Start(); err != nil {
		recorder.stdout.Close()
		return nil, err
	}

	safeInfof("FFmpeg microphone recorder started, audio device: %v", recorder.audioDevice)
	safeInfof("FFmpeg command: %v", recorder.cmd.Args)
	return recorder, nil
}

// StartRecording starts reading microphone data and sends it to the channel
func (r *MicrophoneRecorder) StartRecording(ctx context.Context, audioChan chan<- []byte) {
	defer close(audioChan)

	cancelMonitorDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if err := r.Close(); err != nil {
				safeV(2).Infof("Close microphone recorder on context cancel: %v", err)
			}
		case <-cancelMonitorDone:
		}
	}()
	defer close(cancelMonitorDone)

	buffer := make([]byte, 1600) // 50ms at 16kHz mono 16-bit = 1600 bytes
	const maxReadErrorsBeforeStop = 5
	const readErrorBackoff = 20 * time.Millisecond
	consecutiveReadErrors := 0

	for {
		select {
		case <-ctx.Done():
			safeInfo("Microphone recording stopped by context")
			return
		default:
		}

		// Read audio data (blocking read)
		n, err := r.stdout.Read(buffer)
		if err != nil {
			if err == io.EOF {
				safeInfof("Microphone recording ended, audio device: %v", r.audioDevice)
				return
			}
			if ctx.Err() != nil {
				return
			}
			consecutiveReadErrors++
			safeWarningf("Microphone read error (%d/%d): %v", consecutiveReadErrors, maxReadErrorsBeforeStop, err)
			if consecutiveReadErrors >= maxReadErrorsBeforeStop {
				safeErrorf("Stopping microphone recorder after repeated read errors: %v", err)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(readErrorBackoff):
			}
			continue
		}
		consecutiveReadErrors = 0

		if n > 0 {
			// Send audio data to channel
			audioData := make([]byte, n)
			copy(audioData, buffer[:n])

			select {
			case audioChan <- audioData:
				safeV(3).Infof("Captured %d bytes from microphone", n)
			case <-ctx.Done():
				return
			default:
				safeV(2).Info("Audio buffer full, dropping microphone data")
			}
		}
	}
}

// Close stops the microphone recorder
func (r *MicrophoneRecorder) Close() error {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if r.closed {
		return nil
	}
	r.closed = true

	if r.stdout != nil {
		r.stdout.Close()
	}

	if r.cmd != nil && r.cmd.Process != nil {
		r.cmd.Process.Kill()
		r.cmd.Wait()
	}

	safeInfo("FFmpeg microphone recorder stopped")
	return nil
}
