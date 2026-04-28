package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// FFmpegDecoder provides streaming Opus audio decoding using FFmpeg
type FFmpegDecoder struct {
	sampleRate   int
	channels     int
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	stdout       io.ReadCloser
	stderr       io.ReadCloser
	outputBuffer chan []float32
	errorChan    chan error
	done         chan struct{}
	mutex        sync.Mutex
	closed       bool
}

func decodeFloat32WithCarry(chunk []byte, carry []byte) ([]float32, []byte, error) {
	merged := make([]byte, 0, len(carry)+len(chunk))
	merged = append(merged, carry...)
	merged = append(merged, chunk...)

	completeBytes := (len(merged) / 4) * 4
	if completeBytes == 0 {
		return nil, merged, nil
	}

	samples := make([]float32, completeBytes/4)
	if err := binary.Read(bytes.NewReader(merged[:completeBytes]), binary.LittleEndian, samples); err != nil {
		return nil, nil, err
	}

	nextCarry := append([]byte(nil), merged[completeBytes:]...)
	return samples, nextCarry, nil
}

// NewFFmpegDecoder creates a new FFmpeg-based Opus decoder
func NewFFmpegDecoder(sampleRate, channels int) (*FFmpegDecoder, error) {
	decoder := &FFmpegDecoder{
		sampleRate:   sampleRate,
		channels:     channels,
		outputBuffer: make(chan []float32, 50), // Buffer up to 50 frames to handle bursts
		errorChan:    make(chan error, 1),
		done:         make(chan struct{}),
	}

	// FFmpeg command to decode Opus from stdin to PCM 32-bit float little-endian to stdout
	// -f ogg: input format is Ogg container (for Opus)
	// -i pipe:0: input from stdin
	// -f f32le: output format is 32-bit float little-endian PCM
	// -ar: output sample rate
	// -ac: output channels
	// -: output to stdout
	decoder.cmd = exec.Command("ffmpeg",
		"-f", "ogg",
		"-i", "pipe:0",
		"-f", "f32le",
		"-ar", fmt.Sprintf("%d", sampleRate),
		"-ac", fmt.Sprintf("%d", channels),
		"-loglevel", "error", // Only show errors
		"-")

	var err error
	decoder.stdin, err = decoder.cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	decoder.stdout, err = decoder.cmd.StdoutPipe()
	if err != nil {
		decoder.stdin.Close()
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	decoder.stderr, err = decoder.cmd.StderrPipe()
	if err != nil {
		decoder.stdin.Close()
		decoder.stdout.Close()
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := decoder.cmd.Start(); err != nil {
		decoder.stdin.Close()
		decoder.stdout.Close()
		decoder.stderr.Close()
		return nil, fmt.Errorf("failed to start FFmpeg: %w", err)
	}

	// Start goroutines to handle output and error streams
	go decoder.readOutput()
	go decoder.readErrors()

	return decoder, nil
}

// DecodeOpus decodes Opus data and returns PCM samples count (not bytes)
// Returns the number of samples decoded (per channel)
func (d *FFmpegDecoder) DecodeOpus(opusData []byte, pcmOut []float32) (int, error) {
	d.mutex.Lock()
	if d.closed {
		d.mutex.Unlock()
		return 0, fmt.Errorf("decoder is closed")
	}
	d.mutex.Unlock()

	// Write Opus data to FFmpeg stdin
	_, err := d.stdin.Write(opusData)
	if err != nil {
		return 0, fmt.Errorf("failed to write to FFmpeg stdin: %w", err)
	}

	// Try to read decoded samples with timeout, check multiple times
	for attempts := 0; attempts < 3; attempts++ {
		select {
		case samples := <-d.outputBuffer:
			samplesCount := len(samples)
			if samplesCount > len(pcmOut) {
				samplesCount = len(pcmOut)
			}
			copy(pcmOut, samples[:samplesCount])
			return samplesCount / d.channels, nil // Return samples per channel
		case err := <-d.errorChan:
			return 0, err
		case <-time.After(50 * time.Millisecond):
			// Short timeout, try again if we haven't exhausted attempts
			if attempts < 2 {
				continue
			}
		}
	}
	// No data available after retries
	return 0, nil
}

// readOutput continuously reads PCM data from FFmpeg stdout
func (d *FFmpegDecoder) readOutput() {
	defer close(d.outputBuffer)

	reader := bufio.NewReader(d.stdout)
	buffer := make([]byte, 4096) // Read in chunks
	carry := make([]byte, 0, 3)

	for {
		select {
		case <-d.done:
			return
		default:
			n, err := reader.Read(buffer)
			if err != nil {
				if err != io.EOF {
					select {
					case d.errorChan <- fmt.Errorf("failed to read FFmpeg output: %w", err):
					default:
					}
				}
				return
			}

			if n <= 0 {
				continue
			}

			samples, nextCarry, convErr := decodeFloat32WithCarry(buffer[:n], carry)
			if convErr != nil {
				safeErrorf("Failed to convert bytes to samples: %v", convErr)
				carry = carry[:0]
				continue
			}
			carry = nextCarry
			if len(samples) == 0 {
				continue
			}

			select {
			case d.outputBuffer <- samples:
				// Successfully buffered
			case <-d.done:
				return
			default:
				// Buffer is full, try to make space by reading one frame
				select {
				case <-d.outputBuffer:
					// Removed one old frame, try again
					select {
					case d.outputBuffer <- samples:
					default:
						safeV(3).Info("Output buffer still full after making space, dropping audio frame")
					}
				default:
					safeV(3).Info("Output buffer full, dropping audio frame")
				}
			}
		}
	}
}

// readErrors continuously reads and logs errors from FFmpeg stderr
func (d *FFmpegDecoder) readErrors() {
	scanner := bufio.NewScanner(d.stderr)
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			safeV(2).Infof("FFmpeg: %s", line)
		}
	}
}

// Close closes the decoder and cleans up resources
func (d *FFmpegDecoder) Close() error {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	if d.closed {
		return nil
	}
	d.closed = true

	close(d.done)

	// Close pipes
	if d.stdin != nil {
		d.stdin.Close()
	}
	if d.stdout != nil {
		d.stdout.Close()
	}
	if d.stderr != nil {
		d.stderr.Close()
	}

	// Wait for process to finish
	if d.cmd != nil && d.cmd.Process != nil {
		// Give it a moment to finish gracefully
		done := make(chan error, 1)
		go func() {
			done <- d.cmd.Wait()
		}()

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			// Force kill if it doesn't finish
			d.cmd.Process.Kill()
			<-done
		}
	}

	return nil
}
