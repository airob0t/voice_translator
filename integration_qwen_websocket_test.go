//go:build integration

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestIntegrationQwenWebSocketFlow(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	authHeaderCh := make(chan string, 1)
	modelQueryCh := make(chan string, 1)
	eventCh := make(chan map[string]interface{}, 2)
	serverErrCh := make(chan error, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ws/er/translate" {
			serverErrCh <- fmt.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}

		authHeaderCh <- r.Header.Get("Authorization")
		modelQueryCh <- r.URL.Query().Get("model")

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrCh <- fmt.Errorf("upgrade failed: %w", err)
			return
		}
		defer conn.Close()

		for i := 0; i < 2; i++ {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				serverErrCh <- fmt.Errorf("read message %d failed: %w", i+1, err)
				return
			}

			var evt map[string]interface{}
			if err := json.Unmarshal(raw, &evt); err != nil {
				serverErrCh <- fmt.Errorf("unmarshal message %d failed: %w", i+1, err)
				return
			}
			eventCh <- evt

			if i == 0 {
				if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.updated"}`)); err != nil {
					serverErrCh <- fmt.Errorf("write ack failed: %w", err)
					return
				}
			}
		}
	}))
	defer srv.Close()

	wsHost := "ws" + strings.TrimPrefix(srv.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, err := ConnectQwenWithContext(ctx, wsHost, "api/ws/er/translate", "integration-token", defaultQwenModel)
	if err != nil {
		t.Fatalf("ConnectQwenWithContext failed: %v", err)
	}
	defer closeQwenConn(conn)

	config := QwenSessionConfig{
		SourceLanguage: "en",
		TargetLanguage: "zh",
		Voice:          "Cherry",
		AudioEnabled:   true,
	}
	if err := SendQwenSessionConfig(conn, config); err != nil {
		t.Fatalf("SendQwenSessionConfig failed: %v", err)
	}

	audioPayload := []byte{0x01, 0x02, 0x03, 0x04}
	if err := SendQwenAudioChunk(conn, audioPayload); err != nil {
		t.Fatalf("SendQwenAudioChunk failed: %v", err)
	}

	ackCtx, ackCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer ackCancel()
	ackRaw, err := readQwenMessageWithCancel(ackCtx, conn)
	if err != nil {
		t.Fatalf("readQwenMessageWithCancel failed: %v", err)
	}

	var ack map[string]interface{}
	if err := json.Unmarshal(ackRaw, &ack); err != nil {
		t.Fatalf("unmarshal ack failed: %v", err)
	}
	if got := ack["type"]; got != "session.updated" {
		t.Fatalf("unexpected ack type: %#v", got)
	}

	authHeader := waitStringValue(t, authHeaderCh, "auth header")
	if authHeader != "Bearer integration-token" {
		t.Fatalf("unexpected Authorization header: %q", authHeader)
	}
	modelQuery := waitStringValue(t, modelQueryCh, "model query")
	if modelQuery != defaultQwenModel {
		t.Fatalf("unexpected model query: %q", modelQuery)
	}

	sessionEvt := waitMapValue(t, eventCh, "session event")
	if got := sessionEvt["type"]; got != "session.update" {
		t.Fatalf("expected first event type=session.update, got %#v", got)
	}

	audioEvt := waitMapValue(t, eventCh, "audio event")
	if got := audioEvt["type"]; got != "input_audio_buffer.append" {
		t.Fatalf("expected second event type=input_audio_buffer.append, got %#v", got)
	}

	audioEncoded, ok := audioEvt["audio"].(string)
	if !ok {
		t.Fatalf("expected audio field as base64 string, got %#v", audioEvt["audio"])
	}
	decoded, err := base64.StdEncoding.DecodeString(audioEncoded)
	if err != nil {
		t.Fatalf("decode base64 audio failed: %v", err)
	}
	if string(decoded) != string(audioPayload) {
		t.Fatalf("unexpected audio payload, got %v want %v", decoded, audioPayload)
	}

	select {
	case serverErr := <-serverErrCh:
		t.Fatalf("websocket server assertion failed: %v", serverErr)
	default:
	}
}

func waitMapValue(t *testing.T, ch <-chan map[string]interface{}, label string) map[string]interface{} {
	t.Helper()

	select {
	case value := <-ch:
		return value
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for %s", label)
		return nil
	}
}

func waitStringValue(t *testing.T, ch <-chan string, label string) string {
	t.Helper()

	select {
	case value := <-ch:
		return value
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for %s", label)
		return ""
	}
}
