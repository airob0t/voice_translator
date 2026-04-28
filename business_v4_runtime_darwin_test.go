//go:build darwin
// +build darwin

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/tuokentech/xingyi_client/protogen/common/event"
)

func TestRunV4AudioSenderInvokesErrorHandler(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	audioChan := make(chan []byte, 1)
	audioChan <- []byte{0x01, 0x02}
	close(audioChan)

	called := false
	runV4AudioSender(ctx, cancel, audioChan, func(_ []byte) error {
		return errors.New("send failed")
	}, func(err error) {
		called = true
	})

	if !called {
		t.Fatal("expected error handler to be invoked on send failure")
	}
	if ctx.Err() == nil {
		t.Fatal("expected context to be canceled by error handler")
	}
}

func TestIsSessionTerminalEvent(t *testing.T) {
	if !isSessionTerminalEvent(event.Type_SessionFinished) {
		t.Fatal("session finished should be terminal")
	}
	if !isSessionTerminalEvent(event.Type_SessionFailed) {
		t.Fatal("session failed should be terminal")
	}
	if !isSessionTerminalEvent(event.Type_SessionCanceled) {
		t.Fatal("session canceled should be terminal")
	}
	if isSessionTerminalEvent(event.Type_TTSResponse) {
		t.Fatal("TTS response should not be terminal")
	}
}
