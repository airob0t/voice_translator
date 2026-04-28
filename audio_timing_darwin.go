//go:build darwin
// +build darwin

package main

import "time"

const defaultVirtualAudioSampleRate = 16000

// resolveRingBufferSampleRate returns the runtime sample rate exposed by the driver.
// If the driver value is unavailable, it falls back to the provided rate.
func resolveRingBufferSampleRate(rb *LockFreeRingBuffer, fallback int) int {
	if fallback <= 0 {
		fallback = defaultVirtualAudioSampleRate
	}
	if rb == nil {
		return fallback
	}

	driverRate := int(atomicLoadUint32(&rb.SampleRate))
	if driverRate <= 0 {
		return fallback
	}
	return driverRate
}

// pcmChunkBytesForDuration converts a target duration to PCM byte size.
func pcmChunkBytesForDuration(sampleRate, frameSize int, duration time.Duration) int {
	if sampleRate <= 0 {
		sampleRate = defaultVirtualAudioSampleRate
	}
	if frameSize <= 0 {
		frameSize = FrameSize
	}
	if duration <= 0 {
		duration = 80 * time.Millisecond
	}

	frames := int((int64(sampleRate)*int64(duration) + int64(time.Second)/2) / int64(time.Second))
	if frames <= 0 {
		frames = 1
	}
	return frames * frameSize
}

type virtualMicWriteConfig struct {
	sourceRate int
	outputRate int
	chunkBytes int
	resampler  *Resampler
}

func newVirtualMicWriteConfig(rb *LockFreeRingBuffer, sourceRate, fallback int, duration time.Duration) virtualMicWriteConfig {
	if sourceRate <= 0 {
		sourceRate = defaultVirtualAudioSampleRate
	}
	cfg := virtualMicWriteConfig{
		sourceRate: sourceRate,
	}
	cfg.apply(resolveRingBufferSampleRate(rb, fallback), duration)
	return cfg
}

func (c *virtualMicWriteConfig) refresh(rb *LockFreeRingBuffer, fallback int, duration time.Duration) bool {
	nextRate := resolveRingBufferSampleRate(rb, fallback)
	if nextRate == c.outputRate {
		return false
	}
	c.apply(nextRate, duration)
	return true
}

func (c *virtualMicWriteConfig) apply(outputRate int, duration time.Duration) {
	c.outputRate = outputRate
	c.chunkBytes = pcmChunkBytesForDuration(outputRate, FrameSize, duration)
	if c.sourceRate != outputRate {
		c.resampler = NewResampler(c.sourceRate, outputRate)
	} else {
		c.resampler = nil
	}
}

func (c *virtualMicWriteConfig) transform(input []byte) []byte {
	if len(input) == 0 {
		return nil
	}
	if c.resampler == nil {
		return input
	}
	return c.resampler.Resample16(input)
}

type virtualSpeakerReadConfig struct {
	sourceRate      int
	outputRate      int
	framesPerBuffer uint32
	resampler       *Resampler
}

func newVirtualSpeakerReadConfig(rb *LockFreeRingBuffer, outputRate, fallback int, duration time.Duration) virtualSpeakerReadConfig {
	if outputRate <= 0 {
		outputRate = defaultVirtualAudioSampleRate
	}
	cfg := virtualSpeakerReadConfig{
		outputRate: outputRate,
	}
	cfg.apply(resolveRingBufferSampleRate(rb, fallback), duration)
	return cfg
}

func (c *virtualSpeakerReadConfig) refresh(rb *LockFreeRingBuffer, fallback int, duration time.Duration) bool {
	nextRate := resolveRingBufferSampleRate(rb, fallback)
	if nextRate == c.sourceRate {
		return false
	}
	c.apply(nextRate, duration)
	return true
}

func (c *virtualSpeakerReadConfig) apply(sourceRate int, duration time.Duration) {
	if sourceRate <= 0 {
		sourceRate = defaultVirtualAudioSampleRate
	}
	c.sourceRate = sourceRate
	chunkBytes := pcmChunkBytesForDuration(sourceRate, FrameSize, duration)
	c.framesPerBuffer = uint32(chunkBytes / FrameSize)
	if c.framesPerBuffer == 0 {
		c.framesPerBuffer = 1
	}
	if c.sourceRate != c.outputRate {
		c.resampler = NewResampler(c.sourceRate, c.outputRate)
	} else {
		c.resampler = nil
	}
}

func (c *virtualSpeakerReadConfig) transform(input []byte) []byte {
	if len(input) == 0 {
		return nil
	}
	if c.resampler == nil {
		return input
	}
	return c.resampler.Resample16(input)
}
