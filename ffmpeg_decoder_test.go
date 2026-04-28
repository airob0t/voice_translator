package main

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestDecodeFloat32WithCarry_PreservesUnalignedBytes(t *testing.T) {
	raw := make([]byte, 8)
	binary.LittleEndian.PutUint32(raw[0:4], math.Float32bits(0.5))
	binary.LittleEndian.PutUint32(raw[4:8], math.Float32bits(-0.25))

	samples1, carry, err := decodeFloat32WithCarry(raw[:5], nil)
	if err != nil {
		t.Fatalf("unexpected error on first chunk: %v", err)
	}
	if len(samples1) != 1 {
		t.Fatalf("expected 1 sample from first chunk, got %d", len(samples1))
	}
	if samples1[0] != float32(0.5) {
		t.Fatalf("expected sample 0.5, got %v", samples1[0])
	}
	if len(carry) != 1 {
		t.Fatalf("expected carry length 1, got %d", len(carry))
	}

	samples2, carry2, err := decodeFloat32WithCarry(raw[5:], carry)
	if err != nil {
		t.Fatalf("unexpected error on second chunk: %v", err)
	}
	if len(samples2) != 1 {
		t.Fatalf("expected 1 sample from second chunk, got %d", len(samples2))
	}
	if samples2[0] != float32(-0.25) {
		t.Fatalf("expected sample -0.25, got %v", samples2[0])
	}
	if len(carry2) != 0 {
		t.Fatalf("expected empty carry after second chunk, got %d", len(carry2))
	}
}
