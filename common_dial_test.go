package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type trackingReadCloser struct {
	closed bool
}

func (t *trackingReadCloser) Read(p []byte) (int, error) {
	payload := []byte("unauthorized")
	n := copy(p, payload)
	return n, io.EOF
}

func (t *trackingReadCloser) Close() error {
	t.closed = true
	return nil
}

func TestDialClosesResponseBodyOnError(t *testing.T) {
	originalDial := websocketDialContext
	defer func() {
		websocketDialContext = originalDial
	}()

	body := &trackingReadCloser{}
	websocketDialContext = func(_ context.Context, _ string, _ http.Header) (*websocket.Conn, *http.Response, error) {
		return nil, &http.Response{
			Status: "401 Unauthorized",
			Body:   body,
			Header: http.Header{},
		}, errors.New("handshake failed")
	}

	conf := Config{
		Host:     "ws://example.com",
		Endpoint: "translate",
	}

	if _, err := dial(conf, "conn-id"); err == nil {
		t.Fatal("expected dial to fail")
	}
	if !body.closed {
		t.Fatal("expected response body to be closed on dial error")
	}
}

func TestDialRetriesConnectionGuardUnavailable(t *testing.T) {
	originalDial := websocketDialContext
	defer func() {
		websocketDialContext = originalDial
	}()
	originalSleep := dialRetrySleep
	defer func() {
		dialRetrySleep = originalSleep
	}()
	dialRetrySleep = func(time.Duration) {}

	attempts := 0
	websocketDialContext = func(_ context.Context, _ string, _ http.Header) (*websocket.Conn, *http.Response, error) {
		attempts++
		if attempts == 1 {
			return nil, &http.Response{
				Status:     "503 Service Unavailable",
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(strings.NewReader(`{"error":"Connection guard temporarily unavailable"}`)),
				Header:     http.Header{},
			}, errors.New("websocket: bad handshake")
		}
		return &websocket.Conn{}, nil, nil
	}

	conf := Config{
		Host:     "ws://example.com",
		Endpoint: "translate",
	}

	conn, err := dial(conf, "conn-id")
	if err != nil {
		t.Fatalf("expected dial to recover after transient guard error, got %v", err)
	}
	if conn == nil {
		t.Fatal("expected non-nil connection")
	}
	if attempts != 2 {
		t.Fatalf("expected 2 dial attempts, got %d", attempts)
	}
}

func TestDialDoesNotRetryUnauthorized(t *testing.T) {
	originalDial := websocketDialContext
	defer func() {
		websocketDialContext = originalDial
	}()

	attempts := 0
	websocketDialContext = func(_ context.Context, _ string, _ http.Header) (*websocket.Conn, *http.Response, error) {
		attempts++
		return nil, &http.Response{
			Status:     "401 Unauthorized",
			StatusCode: http.StatusUnauthorized,
			Body:       io.NopCloser(strings.NewReader(`{"error":"Unauthorized"}`)),
			Header:     http.Header{},
		}, errors.New("websocket: bad handshake")
	}

	conf := Config{
		Host:     "ws://example.com",
		Endpoint: "translate",
	}

	if _, err := dial(conf, "conn-id"); err == nil {
		t.Fatal("expected dial to fail")
	}
	if attempts != 1 {
		t.Fatalf("expected unauthorized to avoid retries, got %d attempts", attempts)
	}
}
