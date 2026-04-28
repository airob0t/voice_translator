package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
)

func sendAudioWithBackpressure(ctx context.Context, audioChan chan<- []byte, audioData []byte) bool {
	select {
	case audioChan <- audioData:
		return true
	case <-ctx.Done():
		return false
	}
}

func runQwenAudioSender(ctx context.Context, runCancel context.CancelFunc, audioChan <-chan []byte, send func([]byte) error, errorCallback ErrorCallback) {
	safeInfo("Starting audio sender to Qwen translation service...")

	for {
		if ctx.Err() != nil {
			safeInfo("Audio sender stopped by context.")
			return
		}

		select {
		case <-ctx.Done():
			safeInfo("Audio sender stopped by context.")
			return
		case audioData, ok := <-audioChan:
			if !ok {
				safeInfo("Audio channel closed.")
				return
			}
			if ctx.Err() != nil {
				safeInfo("Audio sender stopped by context.")
				return
			}

			if err := send(audioData); err != nil {
				if ctx.Err() != nil {
					safeV(2).Infof("Qwen audio sender exiting after context cancellation: %v", err)
					return
				}
				safeErrorf("Send audio chunk to Qwen: %v", err)
				if errorCallback != nil {
					errorCallback(fmt.Errorf("发送音频失败: %w", err))
				}
				if runCancel != nil {
					runCancel()
				}
				return
			}
			safeV(3).Infof("Sent %d bytes to Qwen translation service", len(audioData))
		}
	}
}

func runQwenMessageReceiver(ctx context.Context, runCancel context.CancelFunc, receive func(context.Context) error) {
	err := receive(ctx)
	if err == nil {
		// Receiver exited without error (e.g. normal close). Cancel run context so
		// sender/recording goroutines can stop and the session can converge.
		runCancel()
		return
	}

	if isQwenReceiveTimeoutError(err) {
		runCancel()
		return
	}
	if isQwenReceiveClosedError(err) {
		safeInfo("Connection closed by Qwen server.")
	} else if !isQwenReceiveContextCanceled(err) {
		safeErrorf("Qwen message handler error: %v", err)
	}
	runCancel()
}

func isQwenReceiveTimeoutError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func isQwenReceiveContextCanceled(err error) bool {
	return errors.Is(err, context.Canceled)
}

func isQwenReceiveClosedError(err error) bool {
	return errors.Is(err, io.EOF) || strings.Contains(err.Error(), "closed")
}
