//go:build ignore

package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"

	"github.com/gordonklaus/portaudio"
)

// player manages the audio data and playback state.
type player struct {
	data []float32
	pos  int
	done chan struct{} // Channel to signal playback completion
}

// processAudio is the callback function that PortAudio calls to get audio samples.
func (p *player) processAudio(out []float32) {
	for i := range out {
		if p.pos < len(p.data) {
			out[i] = p.data[p.pos]
			p.pos++
		} else {
			// Fill the rest of the buffer with silence if we've reached the end.
			out[i] = 0
		}
	}

	// If we have finished playing all the data, signal the main goroutine.
	if p.pos >= len(p.data) {
		select {
		case <-p.done:
			// Channel already closed, do nothing.
		default:
			// Close the channel to signal completion.
			close(p.done)
		}
	}
}

func main() {
	// Audio parameters from user request
	const (
		sampleRate = 44100
		channels   = 2
		fileName   = "v4_translate_audio_00000.pcm"
	)

	fmt.Printf("Attempting to play %s...\n", fileName)

	// Read the entire PCM file
	pcmData, err := os.ReadFile(fileName)
	if err != nil {
		fmt.Printf("Error reading PCM file: %v\n", err)
		return
	}

	// Convert raw byte data to []float32
	numSamples := len(pcmData) / 4 // Each float32 sample is 4 bytes
	audioBuffer := make([]float32, numSamples)
	reader := bytes.NewReader(pcmData)
	if err := binary.Read(reader, binary.LittleEndian, &audioBuffer); err != nil {
		fmt.Printf("Error converting byte data to float32: %v\n", err)
		return
	}

	p := &player{
		data: audioBuffer,
		done: make(chan struct{}), // Initialize the done channel
	}

	// Initialize PortAudio
	if err := portaudio.Initialize(); err != nil {
		fmt.Printf("Error initializing PortAudio: %v\n", err)
		return
	}
	defer portaudio.Terminate()

	hostAPI, err := portaudio.DefaultHostApi()
	if err != nil {
		fmt.Printf("Error getting default host API: %v\n", err)
		return
	}

	parameters := portaudio.StreamParameters{
		Input:           portaudio.StreamDeviceParameters{Device: nil, Channels: 0},
		Output:          portaudio.StreamDeviceParameters{Device: hostAPI.DefaultOutputDevice, Channels: channels, Latency: hostAPI.DefaultOutputDevice.DefaultHighOutputLatency},
		SampleRate:      sampleRate,
		FramesPerBuffer: 0, // Let PortAudio choose the buffer size
	}

	stream, err := portaudio.OpenStream(parameters, p.processAudio)
	if err != nil {
		fmt.Printf("Error opening PortAudio stream: %v\n", err)
		return
	}
	defer stream.Close()

	// Start the audio stream
	if err := stream.Start(); err != nil {
		fmt.Printf("Error starting stream: %v\n", err)
		return
	}

	fmt.Println("Playing audio...")

	// Wait for the stream to finish by waiting on the done channel.
	// This blocks until the channel is closed by the callback.
	<-p.done

	// Stop the stream
	if err := stream.Stop(); err != nil {
		fmt.Printf("Error stopping stream: %v\n", err)
	}

	fmt.Println("Playback finished.")
}
