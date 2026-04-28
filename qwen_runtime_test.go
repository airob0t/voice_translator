package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"
)

func TestRunQwenAudioSender_CancelsOnSendError(t *testing.T) {
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	audioChan := make(chan []byte, 1)
	audioChan <- []byte{1, 2, 3}
	close(audioChan)

	var callbackErr error
	runQwenAudioSender(runCtx, runCancel, audioChan, func(_ []byte) error {
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

func TestRunQwenAudioSender_DoesNotReportSendErrorAfterCancel(t *testing.T) {
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	audioChan := make(chan []byte, 1)
	audioChan <- []byte{1, 2, 3}
	close(audioChan)

	callbackCalled := false
	runQwenAudioSender(runCtx, runCancel, audioChan, func(_ []byte) error {
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

func TestRunQwenMessageReceiver_CancelsOnDisconnect(t *testing.T) {
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	runQwenMessageReceiver(runCtx, runCancel, func(ctx context.Context) error {
		return io.EOF
	})

	select {
	case <-runCtx.Done():
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected run context to be canceled on receiver disconnect")
	}
}

func TestSendAudioWithBackpressure_WaitsForCapacity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	audioChan := make(chan []byte, 1)
	audioChan <- []byte{0x00} // fill channel first

	type sendResult struct {
		ok bool
	}
	resultCh := make(chan sendResult, 1)
	go func() {
		ok := sendAudioWithBackpressure(ctx, audioChan, []byte{0x01, 0x02})
		resultCh <- sendResult{ok: ok}
	}()

	select {
	case <-resultCh:
		t.Fatal("expected sender to wait while channel is full")
	case <-time.After(40 * time.Millisecond):
	}

	<-audioChan // free one slot

	select {
	case result := <-resultCh:
		if !result.ok {
			t.Fatal("expected send to succeed after capacity is available")
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("expected sender to proceed after channel has capacity")
	}

	got := <-audioChan
	if len(got) != 2 || got[0] != 0x01 || got[1] != 0x02 {
		t.Fatalf("unexpected payload sent: %v", got)
	}
}

func TestSendAudioWithBackpressure_StopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	audioChan := make(chan []byte, 1)
	audioChan <- []byte{0x00} // keep channel full

	resultCh := make(chan bool, 1)
	go func() {
		resultCh <- sendAudioWithBackpressure(ctx, audioChan, []byte{0x01})
	}()

	cancel()

	select {
	case ok := <-resultCh:
		if ok {
			t.Fatal("expected send to fail when context is canceled")
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("expected sender to stop after context cancellation")
	}
}

func TestRunQwenAudioSender_DoesNotSendWhenContextAlreadyCanceled(t *testing.T) {
	for i := 0; i < 200; i++ {
		runCtx, runCancel := context.WithCancel(context.Background())
		audioChan := make(chan []byte, 1)
		audioChan <- []byte{1, 2, 3}
		runCancel()

		sendCalled := false
		runQwenAudioSender(runCtx, runCancel, audioChan, func(_ []byte) error {
			sendCalled = true
			return nil
		}, nil)
		if sendCalled {
			t.Fatalf("expected no send after cancellation on iteration %d", i)
		}
	}
}

func TestRunQwenAudioSender_PreservesCanceledCauseInCallback(t *testing.T) {
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	audioChan := make(chan []byte, 1)
	audioChan <- []byte{1, 2, 3}
	close(audioChan)

	var callbackErr error
	runQwenAudioSender(runCtx, runCancel, audioChan, func(_ []byte) error {
		return context.Canceled
	}, func(err error) {
		callbackErr = err
	})

	if callbackErr == nil {
		t.Fatal("expected error callback on send cancellation")
	}
	if !errors.Is(callbackErr, context.Canceled) {
		t.Fatalf("expected callback error to wrap context.Canceled, got %v", callbackErr)
	}
}

type qwenRuntimeTimeoutErr struct{}

func (qwenRuntimeTimeoutErr) Error() string   { return "timeout" }
func (qwenRuntimeTimeoutErr) Timeout() bool   { return true }
func (qwenRuntimeTimeoutErr) Temporary() bool { return true }

func TestIsQwenReceiveTimeoutError_DetectsWrappedNetTimeout(t *testing.T) {
	err := fmt.Errorf("outer: %w", qwenRuntimeTimeoutErr{})
	if !isQwenReceiveTimeoutError(err) {
		t.Fatalf("expected wrapped net timeout to be classified as timeout, got %v", err)
	}
}

func TestIsQwenReceiveContextCanceled_DetectsWrappedCanceled(t *testing.T) {
	err := fmt.Errorf("outer: %w", context.Canceled)
	if !isQwenReceiveContextCanceled(err) {
		t.Fatalf("expected wrapped context cancellation to be recognized, got %v", err)
	}
}

func TestIsQwenReceiveClosedError_DetectsWrappedEOF(t *testing.T) {
	err := fmt.Errorf("outer: %w", io.EOF)
	if !isQwenReceiveClosedError(err) {
		t.Fatalf("expected wrapped EOF to be recognized as closed connection, got %v", err)
	}
}

func TestIsQwenReceiveClosedError_DetectsClosedMessage(t *testing.T) {
	err := errors.New("websocket: close 1000 (normal): closed")
	if !isQwenReceiveClosedError(err) {
		t.Fatalf("expected closed-text error to be recognized, got %v", err)
	}
}

func TestRunQwenMessageReceiver_CancelsWhenReceiverReturnsNil(t *testing.T) {
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	runQwenMessageReceiver(runCtx, runCancel, func(context.Context) error {
		return nil
	})

	select {
	case <-runCtx.Done():
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected run context to be canceled when receiver exits without error")
	}
}
