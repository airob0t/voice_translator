package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

type nonTimeoutNetErr struct{}

func (nonTimeoutNetErr) Error() string   { return "non-timeout" }
func (nonTimeoutNetErr) Timeout() bool   { return false }
func (nonTimeoutNetErr) Temporary() bool { return false }

func TestIsRetryableQwenReadError(t *testing.T) {
	if !isRetryableQwenReadError(timeoutErr{}) {
		t.Fatal("expected timeout errors to be retryable")
	}
	if !isRetryableQwenReadError(fmt.Errorf("wrapped: %w", timeoutErr{})) {
		t.Fatal("expected wrapped timeout errors to be retryable")
	}
	if isRetryableQwenReadError(nonTimeoutNetErr{}) {
		t.Fatal("expected non-timeout network errors to be non-retryable")
	}
	if isRetryableQwenReadError(errors.New("plain error")) {
		t.Fatal("expected plain errors to be non-retryable")
	}
}

func TestConnectQwenWithContext_CanceledContextFailsFast(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ConnectQwenWithContext(ctx, "ws://127.0.0.1:65535", "translate", "test-api-key", defaultQwenModel)
	if err == nil {
		t.Fatal("expected dial to fail when context is canceled")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("expected context canceled error, got %v", err)
	}
}
