//go:build darwin
// +build darwin

package main

import (
	"strings"
	"testing"
)

func validSharedMemoryForTest() *SharedMemoryData {
	return &SharedMemoryData{
		RingBuffer: LockFreeRingBuffer{
			Magic:         TranslatorAudioMagic,
			Version:       TranslatorAudioVersion,
			BufferSize:    RingBufferFrames,
			SampleRate:    uint32(SampleRate),
			Channels:      Channels,
			BitsPerSample: uint32(SampleSize * 8),
		},
	}
}

func TestValidateSharedMemory_ValidLayout(t *testing.T) {
	shm := validSharedMemoryForTest()
	if err := ValidateSharedMemory(shm); err != nil {
		t.Fatalf("expected valid shared memory, got error: %v", err)
	}
}

func TestValidateSharedMemory_RejectsInvalidBufferSize(t *testing.T) {
	shm := validSharedMemoryForTest()
	shm.RingBuffer.BufferSize = RingBufferFrames + 1

	err := ValidateSharedMemory(shm)
	if err == nil {
		t.Fatal("expected error for invalid buffer size")
	}
	if !strings.Contains(err.Error(), "buffer size") {
		t.Fatalf("expected buffer size error, got: %v", err)
	}
}

func TestValidateSharedMemory_RejectsChannelMismatch(t *testing.T) {
	shm := validSharedMemoryForTest()
	shm.RingBuffer.Channels = 2

	err := ValidateSharedMemory(shm)
	if err == nil {
		t.Fatal("expected error for channel mismatch")
	}
	if !strings.Contains(err.Error(), "channels") {
		t.Fatalf("expected channels error, got: %v", err)
	}
}

func TestValidateSharedMemory_RejectsBitDepthMismatch(t *testing.T) {
	shm := validSharedMemoryForTest()
	shm.RingBuffer.BitsPerSample = 24

	err := ValidateSharedMemory(shm)
	if err == nil {
		t.Fatal("expected error for bit depth mismatch")
	}
	if !strings.Contains(err.Error(), "bits per sample") {
		t.Fatalf("expected bits per sample error, got: %v", err)
	}
}
