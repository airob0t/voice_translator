package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type fakeQwenWriteConn struct {
	setWriteDeadlineCalls int
	writeMessageCalls     int
	writeControlCalls     int

	lastWriteDeadline   time.Time
	lastControlDeadline time.Time

	setWriteDeadlineErr error
	writeMessageErr     error
	writeControlErr     error
}

func (f *fakeQwenWriteConn) SetWriteDeadline(t time.Time) error {
	f.setWriteDeadlineCalls++
	f.lastWriteDeadline = t
	return f.setWriteDeadlineErr
}

func (f *fakeQwenWriteConn) WriteMessage(_ int, _ []byte) error {
	f.writeMessageCalls++
	return f.writeMessageErr
}

func (f *fakeQwenWriteConn) WriteControl(_ int, _ []byte, deadline time.Time) error {
	f.writeControlCalls++
	f.lastControlDeadline = deadline
	return f.writeControlErr
}

func TestWriteQwenMessageWithDeadlineSetsDeadline(t *testing.T) {
	conn := &fakeQwenWriteConn{}

	if err := writeQwenMessageWithDeadline(conn, websocket.TextMessage, []byte(`{"type":"test"}`)); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if conn.setWriteDeadlineCalls != 1 {
		t.Fatalf("expected SetWriteDeadline to be called once, got %d", conn.setWriteDeadlineCalls)
	}
	if conn.writeMessageCalls != 1 {
		t.Fatalf("expected WriteMessage to be called once, got %d", conn.writeMessageCalls)
	}
	if conn.lastWriteDeadline.IsZero() {
		t.Fatal("expected write deadline to be set")
	}
	if !conn.lastWriteDeadline.After(time.Now()) {
		t.Fatalf("expected deadline in future, got %v", conn.lastWriteDeadline)
	}
}

func TestWriteQwenControlWithDeadlineSetsSharedDeadline(t *testing.T) {
	conn := &fakeQwenWriteConn{}

	if err := writeQwenControlWithDeadline(conn, websocket.CloseMessage, []byte("bye")); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if conn.setWriteDeadlineCalls != 1 {
		t.Fatalf("expected SetWriteDeadline to be called once, got %d", conn.setWriteDeadlineCalls)
	}
	if conn.writeControlCalls != 1 {
		t.Fatalf("expected WriteControl to be called once, got %d", conn.writeControlCalls)
	}
	if conn.lastWriteDeadline.IsZero() || conn.lastControlDeadline.IsZero() {
		t.Fatal("expected deadlines to be set")
	}
	if !conn.lastControlDeadline.Equal(conn.lastWriteDeadline) {
		t.Fatalf("expected control deadline to match write deadline, got write=%v control=%v", conn.lastWriteDeadline, conn.lastControlDeadline)
	}
}

func TestWriteQwenMessageWithDeadlinePropagatesDeadlineError(t *testing.T) {
	conn := &fakeQwenWriteConn{setWriteDeadlineErr: errors.New("deadline failed")}

	err := writeQwenMessageWithDeadline(conn, websocket.TextMessage, []byte("x"))
	if err == nil {
		t.Fatal("expected error when SetWriteDeadline fails")
	}
}

func TestReleaseQwenConnWriteLockRemovesEntry(t *testing.T) {
	conn := &websocket.Conn{}
	defer qwenConnWriteLocks.Delete(conn)

	if err := withQwenConnWriteLock(conn, func() error { return nil }); err != nil {
		t.Fatalf("expected no error when creating lock entry, got %v", err)
	}
	if _, ok := qwenConnWriteLocks.Load(conn); !ok {
		t.Fatal("expected lock entry to exist")
	}

	releaseQwenConnWriteLock(conn)

	if _, ok := qwenConnWriteLocks.Load(conn); ok {
		t.Fatal("expected lock entry to be removed")
	}
}

func TestSendQwenSessionCloseKeepsLockUntilConnectionClose(t *testing.T) {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(_ *http.Request) bool { return true },
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to dial test websocket server: %v", err)
	}
	defer conn.Close()
	defer qwenConnWriteLocks.Delete(conn)

	if err := withQwenConnWriteLock(conn, func() error { return nil }); err != nil {
		t.Fatalf("expected no error when creating lock entry, got %v", err)
	}
	if _, ok := qwenConnWriteLocks.Load(conn); !ok {
		t.Fatal("expected lock entry to exist before session close")
	}

	if err := SendQwenSessionClose(conn); err != nil {
		t.Fatalf("expected session close to succeed, got %v", err)
	}
	if _, ok := qwenConnWriteLocks.Load(conn); !ok {
		t.Fatal("expected lock entry to remain until connection close")
	}

	_ = closeQwenConn(conn)
	if _, ok := qwenConnWriteLocks.Load(conn); ok {
		t.Fatal("expected lock entry to be removed after connection close")
	}
}
