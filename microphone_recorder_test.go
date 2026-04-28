package main

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

type blockingReadCloser struct {
	once   sync.Once
	closed chan struct{}
}

type alwaysFailReadCloser struct{}

func (a *alwaysFailReadCloser) Read(_ []byte) (int, error) {
	return 0, errors.New("boom")
}

func (a *alwaysFailReadCloser) Close() error {
	return nil
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{
		closed: make(chan struct{}),
	}
}

func (b *blockingReadCloser) Read(_ []byte) (int, error) {
	<-b.closed
	return 0, io.EOF
}

func (b *blockingReadCloser) Close() error {
	b.once.Do(func() {
		close(b.closed)
	})
	return nil
}

func TestStartRecording_StopsPromptlyOnContextCancel(t *testing.T) {
	recorder := &MicrophoneRecorder{
		stdout: newBlockingReadCloser(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	audioChan := make(chan []byte, 1)
	done := make(chan struct{})

	go func() {
		recorder.StartRecording(ctx, audioChan)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(700 * time.Millisecond):
		t.Fatal("StartRecording did not return after context cancellation")
	}
}

func TestStartRecording_StopsOnPersistentReadError(t *testing.T) {
	recorder := &MicrophoneRecorder{
		stdout: &alwaysFailReadCloser{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	audioChan := make(chan []byte, 1)
	done := make(chan struct{})

	go func() {
		recorder.StartRecording(ctx, audioChan)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("StartRecording should stop on persistent read errors instead of spinning")
	}
}
