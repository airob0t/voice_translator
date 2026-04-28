//go:build darwin
// +build darwin

package main

import (
	"testing"
	"time"
)

func TestResolveRingBufferSampleRateUsesDriverValue(t *testing.T) {
	rb := &LockFreeRingBuffer{SampleRate: 48000}
	got := resolveRingBufferSampleRate(rb, 16000)
	if got != 48000 {
		t.Fatalf("expected 48000, got %d", got)
	}
}

func TestResolveRingBufferSampleRateFallsBackWhenInvalid(t *testing.T) {
	tests := []struct {
		name     string
		rb       *LockFreeRingBuffer
		fallback int
		want     int
	}{
		{
			name:     "nil ring buffer",
			rb:       nil,
			fallback: 16000,
			want:     16000,
		},
		{
			name:     "zero rate in ring buffer",
			rb:       &LockFreeRingBuffer{SampleRate: 0},
			fallback: 16000,
			want:     16000,
		},
		{
			name:     "invalid fallback",
			rb:       &LockFreeRingBuffer{SampleRate: 0},
			fallback: 0,
			want:     16000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveRingBufferSampleRate(tt.rb, tt.fallback)
			if got != tt.want {
				t.Fatalf("expected %d, got %d", tt.want, got)
			}
		})
	}
}

func TestPCMChunkBytesForDuration(t *testing.T) {
	t.Run("16k 80ms mono s16", func(t *testing.T) {
		got := pcmChunkBytesForDuration(16000, FrameSize, 80*time.Millisecond)
		if got != 2560 {
			t.Fatalf("expected 2560 bytes, got %d", got)
		}
	})

	t.Run("48k 80ms mono s16", func(t *testing.T) {
		got := pcmChunkBytesForDuration(48000, FrameSize, 80*time.Millisecond)
		if got != 7680 {
			t.Fatalf("expected 7680 bytes, got %d", got)
		}
	})
}

func TestVirtualMicWriteConfigRefreshOnSampleRateChange(t *testing.T) {
	rb := &LockFreeRingBuffer{SampleRate: 16000}
	cfg := newVirtualMicWriteConfig(rb, qwenOutputSampleRate, 16000, 80*time.Millisecond)
	if cfg.outputRate != 16000 {
		t.Fatalf("expected initial output rate 16000, got %d", cfg.outputRate)
	}
	if cfg.chunkBytes != 2560 {
		t.Fatalf("expected initial chunk bytes 2560, got %d", cfg.chunkBytes)
	}

	rb.SampleRate = 48000
	changed := cfg.refresh(rb, 16000, 80*time.Millisecond)
	if !changed {
		t.Fatal("expected config refresh to detect sample rate change")
	}
	if cfg.outputRate != 48000 {
		t.Fatalf("expected output rate 48000 after refresh, got %d", cfg.outputRate)
	}
	if cfg.chunkBytes != 7680 {
		t.Fatalf("expected chunk bytes 7680 after refresh, got %d", cfg.chunkBytes)
	}
	if cfg.resampler == nil {
		t.Fatal("expected resampler to be configured for 24k->48k")
	}
}

func TestVirtualMicWriteConfigNoResamplerWhenRatesMatch(t *testing.T) {
	rb := &LockFreeRingBuffer{SampleRate: 24000}
	cfg := newVirtualMicWriteConfig(rb, qwenOutputSampleRate, 16000, 80*time.Millisecond)
	if cfg.outputRate != 24000 {
		t.Fatalf("expected output rate 24000, got %d", cfg.outputRate)
	}
	if cfg.resampler != nil {
		t.Fatal("expected no resampler when source and output rates match")
	}
}

func TestVirtualSpeakerReadConfigRefreshOnSampleRateChange(t *testing.T) {
	rb := &LockFreeRingBuffer{SampleRate: 16000}
	cfg := newVirtualSpeakerReadConfig(rb, 16000, 16000, 80*time.Millisecond)
	if cfg.sourceRate != 16000 {
		t.Fatalf("expected initial source rate 16000, got %d", cfg.sourceRate)
	}
	if cfg.framesPerBuffer != 1280 {
		t.Fatalf("expected initial framesPerBuffer 1280, got %d", cfg.framesPerBuffer)
	}
	if cfg.resampler != nil {
		t.Fatal("expected no resampler when source and output rates match")
	}

	rb.SampleRate = 48000
	changed := cfg.refresh(rb, 16000, 80*time.Millisecond)
	if !changed {
		t.Fatal("expected config refresh to detect sample rate change")
	}
	if cfg.sourceRate != 48000 {
		t.Fatalf("expected source rate 48000 after refresh, got %d", cfg.sourceRate)
	}
	if cfg.framesPerBuffer != 3840 {
		t.Fatalf("expected framesPerBuffer 3840 after refresh, got %d", cfg.framesPerBuffer)
	}
	if cfg.resampler == nil {
		t.Fatal("expected resampler to be configured for 48k->16k")
	}
}
