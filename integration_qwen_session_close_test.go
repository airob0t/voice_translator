//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestIntegrationQwenSessionClose_SendsClearThenNormalClose(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	type closeObservation struct {
		clearEventType string
		closeCode      int
		closeText      string
	}

	resultCh := make(chan closeObservation, 1)
	errCh := make(chan error, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errCh <- fmt.Errorf("upgrade failed: %w", err)
			return
		}
		defer conn.Close()

		var obs closeObservation

		_, clearRaw, err := conn.ReadMessage()
		if err != nil {
			errCh <- fmt.Errorf("read clear event failed: %w", err)
			return
		}

		var clearEvent map[string]interface{}
		if err := json.Unmarshal(clearRaw, &clearEvent); err != nil {
			errCh <- fmt.Errorf("unmarshal clear event failed: %w", err)
			return
		}

		evtType, _ := clearEvent["type"].(string)
		obs.clearEventType = evtType

		_, _, err = conn.ReadMessage()
		if err == nil {
			errCh <- errors.New("expected close frame, but read succeeded")
			return
		}

		var closeErr *websocket.CloseError
		if !errors.As(err, &closeErr) {
			errCh <- fmt.Errorf("expected CloseError, got %T (%v)", err, err)
			return
		}

		obs.closeCode = closeErr.Code
		obs.closeText = closeErr.Text
		resultCh <- obs
	}))
	defer server.Close()

	wsHost := "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, err := ConnectQwenWithContext(ctx, wsHost, "api/ws/er/translate", "close-token", defaultQwenModel)
	if err != nil {
		t.Fatalf("ConnectQwenWithContext failed: %v", err)
	}
	defer closeQwenConn(conn)

	if err := SendQwenSessionClose(conn); err != nil {
		t.Fatalf("SendQwenSessionClose failed: %v", err)
	}

	select {
	case serverErr := <-errCh:
		t.Fatalf("websocket server assertion failed: %v", serverErr)
	case obs := <-resultCh:
		if obs.clearEventType != "input_audio_buffer.clear" {
			t.Fatalf("unexpected clear event type: %q", obs.clearEventType)
		}
		if obs.closeCode != websocket.CloseNormalClosure {
			t.Fatalf("unexpected close code: %d", obs.closeCode)
		}
		if obs.closeText != "session ended" {
			t.Fatalf("unexpected close text: %q", obs.closeText)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for close observation")
	}
}
