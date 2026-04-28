package main

import (
	"errors"
	"testing"
	"time"
)

type mockWSMessageWriter struct {
	deadlines []time.Time
	mt        int
	data      []byte
	writeErr  error
}

func (m *mockWSMessageWriter) SetWriteDeadline(t time.Time) error {
	m.deadlines = append(m.deadlines, t)
	return nil
}

func (m *mockWSMessageWriter) WriteMessage(messageType int, data []byte) error {
	m.mt = messageType
	m.data = append([]byte(nil), data...)
	return m.writeErr
}

func TestWriteWSMessageWithTimeout_SetsAndResetsDeadline(t *testing.T) {
	mock := &mockWSMessageWriter{}
	payload := []byte{1, 2, 3}

	if err := writeWSMessageWithTimeout(mock, 2, payload, 500*time.Millisecond); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.deadlines) != 2 {
		t.Fatalf("expected 2 deadline calls, got %d", len(mock.deadlines))
	}
	if mock.deadlines[0].IsZero() {
		t.Fatal("expected first deadline to be non-zero")
	}
	if !mock.deadlines[1].IsZero() {
		t.Fatal("expected second deadline reset to zero time")
	}
	if mock.mt != 2 {
		t.Fatalf("expected message type 2, got %d", mock.mt)
	}
	if len(mock.data) != len(payload) {
		t.Fatalf("expected payload length %d, got %d", len(payload), len(mock.data))
	}
}

func TestWriteWSMessageWithTimeout_ResetsDeadlineOnError(t *testing.T) {
	mock := &mockWSMessageWriter{writeErr: errors.New("write failed")}

	err := writeWSMessageWithTimeout(mock, 1, []byte{9}, 500*time.Millisecond)
	if err == nil {
		t.Fatal("expected error")
	}
	if len(mock.deadlines) != 2 {
		t.Fatalf("expected 2 deadline calls, got %d", len(mock.deadlines))
	}
	if !mock.deadlines[1].IsZero() {
		t.Fatal("expected second deadline reset to zero time")
	}
}
