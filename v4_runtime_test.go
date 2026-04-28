package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestRunV4AudioSender_CancelsOnSendError(t *testing.T) {
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	audioChan := make(chan []byte, 1)
	audioChan <- []byte{1, 2, 3}
	close(audioChan)

	var callbackErr error
	runV4AudioSender(runCtx, runCancel, audioChan, func(_ []byte) error {
		return errors.New("send failed")
	}, func(err error) {
		callbackErr = err
	})

	select {
	case <-runCtx.Done():
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected run context to be canceled on send error")
	}

	if callbackErr == nil {
		t.Fatal("expected error callback on send failure")
	}
}

func TestRunV4AudioSender_DoesNotReportSendErrorAfterCancel(t *testing.T) {
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	audioChan := make(chan []byte, 1)
	audioChan <- []byte{1, 2, 3}
	close(audioChan)

	callbackCalled := false
	runV4AudioSender(runCtx, runCancel, audioChan, func(_ []byte) error {
		runCancel()
		return errors.New("connection closed")
	}, func(error) {
		callbackCalled = true
	})

	if !errors.Is(runCtx.Err(), context.Canceled) {
		t.Fatalf("expected canceled context, got %v", runCtx.Err())
	}
	if callbackCalled {
		t.Fatal("did not expect error callback after context cancellation")
	}
}

func TestRunV4AudioSender_DoesNotReportContextCanceledSendError(t *testing.T) {
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	audioChan := make(chan []byte, 1)
	audioChan <- []byte{1, 2, 3}
	close(audioChan)

	callbackCalled := false
	runV4AudioSender(runCtx, runCancel, audioChan, func(_ []byte) error {
		return context.Canceled
	}, func(error) {
		callbackCalled = true
	})

	if callbackCalled {
		t.Fatal("did not expect error callback on context canceled send error")
	}
}

func TestRunV4AudioSender_DoesNotReportNormalClosureSendError(t *testing.T) {
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	audioChan := make(chan []byte, 1)
	audioChan <- []byte{1, 2, 3}
	close(audioChan)

	callbackCalled := false
	runV4AudioSender(runCtx, runCancel, audioChan, func(_ []byte) error {
		return &websocket.CloseError{Code: websocket.CloseNormalClosure}
	}, func(error) {
		callbackCalled = true
	})

	if callbackCalled {
		t.Fatal("did not expect error callback on normal websocket closure")
	}
}
