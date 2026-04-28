package main

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type blockingWriteCloser struct {
	once   sync.Once
	closed chan struct{}
}

func newBlockingWriteCloser() *blockingWriteCloser {
	return &blockingWriteCloser{
		closed: make(chan struct{}),
	}
}

func (b *blockingWriteCloser) Write(_ []byte) (int, error) {
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *blockingWriteCloser) Close() error {
	b.once.Do(func() {
		close(b.closed)
	})
	return nil
}

func TestRawAudioFormatForBitDepth_24Bit(t *testing.T) {
	format, err := rawAudioFormatForBitDepth(24)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if format != "s24le" {
		t.Fatalf("expected s24le, got %q", format)
	}
}

func TestRawAudioFormatForBitDepth_Invalid(t *testing.T) {
	if _, err := rawAudioFormatForBitDepth(20); err == nil {
		t.Fatal("expected error for unsupported bit depth")
	}
}

func TestPlayerWriteAudio_TimeoutForcesClose(t *testing.T) {
	oldTimeout := playerWriteTimeout
	playerWriteTimeout = 20 * time.Millisecond
	defer func() {
		playerWriteTimeout = oldTimeout
	}()

	stdin := newBlockingWriteCloser()
	player := &Player{
		stdin: stdin,
	}

	err := player.WriteAudio([]byte{1, 2, 3, 4})
	if err == nil {
		t.Fatal("expected timeout error from WriteAudio")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if !player.closed {
		t.Fatal("expected player to be closed after write timeout")
	}

	if err := player.WriteAudio([]byte{1}); err == nil {
		t.Fatal("expected closed player write to fail")
	}
}
