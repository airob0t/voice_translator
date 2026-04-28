package main

import (
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

// Player provides audio playback using mpv
type Player struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr io.ReadCloser
	mutex  sync.Mutex
	closed bool
}

var playerWriteTimeout = 2 * time.Second

func rawAudioFormatForBitDepth(bitDepth int) (string, error) {
	switch bitDepth {
	case 16:
		return "s16le", nil
	case 24:
		return "s24le", nil
	default:
		return "", fmt.Errorf("unsupported PCM bit depth: %d", bitDepth)
	}
}

// NewPCMPlayer creates a new audio player for raw PCM data
// sampleRate: e.g., 16000, 44100
// channels: 1 for mono, 2 for stereo
// bitDepth: 16 or 24
func NewPCMPlayer(sampleRate, channels, bitDepth int) (*Player, error) {
	return NewPCMPlayerWithDevice(sampleRate, channels, bitDepth, "")
}

// NewPCMPlayerWithDevice creates a new audio player for raw PCM data with a specific device
func NewPCMPlayerWithDevice(sampleRate, channels, bitDepth int, audioDevice string) (*Player, error) {
	player := &Player{}
	rawFormat, err := rawAudioFormatForBitDepth(bitDepth)
	if err != nil {
		return nil, err
	}

	args := []string{
		"--no-video",
		"--demuxer=rawaudio",
		fmt.Sprintf("--demuxer-rawaudio-rate=%d", sampleRate),
		fmt.Sprintf("--demuxer-rawaudio-channels=%d", channels),
		fmt.Sprintf("--demuxer-rawaudio-format=%s", rawFormat),
		"--audio-buffer=0.1",           // 降低音频缓冲到100ms
		"--demuxer-readahead-secs=0.1", // 降低预读到100ms
		"--cache=yes",                  // 启用缓存但设置很小
		"--cache-secs=0.1",             // 缓存时长100ms
		"--hr-seek=no",                 // 禁用高精度seek以减少延迟
	}

	// Add audio device if specified
	if audioDevice != "" {
		args = append([]string{"--audio-device=" + audioDevice}, args...)
	}

	args = append(args, "-")

	// Use mpv.com on Windows, mpv on other platforms
	var mpvCmd string
	if runtime.GOOS == "windows" {
		mpvCmd = ".\\mpv.com"
	} else {
		mpvCmd = "mpv"
	}

	player.cmd = exec.Command(mpvCmd, args...)

	player.stdin, err = player.cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	player.stderr, err = player.cmd.StderrPipe()
	if err != nil {
		player.stdin.Close()
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := player.cmd.Start(); err != nil {
		player.stdin.Close()
		player.stderr.Close()
		return nil, fmt.Errorf("failed to start mpv: %w", err)
	}

	// Start goroutine to read and log errors
	go player.readErrors()

	safeInfof("PCM audio player started (rate=%d, channels=%d, bits=%d)", sampleRate, channels, bitDepth)
	safeInfof("mpv command: %v", player.cmd.Args)
	return player, nil
}

// WriteAudio writes audio data to mpv for playback
// The audio data should be in the format specified when creating the player
func (p *Player) WriteAudio(data []byte) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if p.closed {
		return fmt.Errorf("player is closed")
	}

	if len(data) == 0 {
		return nil
	}

	type writeResult struct {
		n   int
		err error
	}
	done := make(chan writeResult, 1)
	go func(stdin io.WriteCloser, payload []byte) {
		n, err := stdin.Write(payload)
		done <- writeResult{n: n, err: err}
	}(p.stdin, data)

	var n int
	select {
	case res := <-done:
		if res.err != nil {
			return fmt.Errorf("failed to write to mpv stdin: %w", res.err)
		}
		n = res.n
	case <-time.After(playerWriteTimeout):
		safeWarningf("Write to mpv stdin timed out after %s, forcing player close", playerWriteTimeout)
		_ = p.closeLocked()
		return fmt.Errorf("write to mpv stdin timed out after %s", playerWriteTimeout)
	}

	if n != len(data) {
		return fmt.Errorf("incomplete write: wrote %d bytes, expected %d", n, len(data))
	}

	safeV(3).Infof("Wrote %d bytes to mpv", n)
	return nil
}

// readErrors continuously reads and logs errors from mpv stderr
func (p *Player) readErrors() {
	buffer := make([]byte, 1024)
	for {
		n, err := p.stderr.Read(buffer)
		if err != nil {
			if err != io.EOF {
				safeWarningf("Error reading mpv stderr: %v", err)
			}
			return
		}
		if n > 0 {
			// Log mpv output at Info level so we can see what's happening
			safeInfof("mpv stderr: %s", string(buffer[:n]))
		}
	}
}

// Close stops the audio player and cleans up resources
func (p *Player) Close() error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	return p.closeLocked()
}

func (p *Player) closeLocked() error {
	if p.closed {
		return nil
	}
	p.closed = true

	safeInfo("Closing FFmpeg audio player...")

	// Close stdin to signal end of input
	if p.stdin != nil {
		p.stdin.Close()
	}

	// Close stderr
	if p.stderr != nil {
		p.stderr.Close()
	}

	// Wait for process to finish
	if p.cmd != nil && p.cmd.Process != nil {
		p.cmd.Process.Kill()
		p.cmd.Wait()
	}

	safeInfo("FFmpeg audio player stopped")
	return nil
}
