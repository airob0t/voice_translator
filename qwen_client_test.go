package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestBuildQwenSessionUpdatePayload_UsesOfficialRealtimeFields(t *testing.T) {
	cfg := QwenSessionConfig{
		SourceLanguage: "en",
		TargetLanguage: "zh",
		Voice:          "Cherry",
		AudioEnabled:   true,
	}

	payload := buildQwenSessionUpdatePayload(cfg)
	session, ok := payload["session"].(map[string]interface{})
	if !ok {
		t.Fatalf("session payload missing or wrong type: %#v", payload["session"])
	}

	translation, ok := session["translation"].(map[string]interface{})
	if !ok {
		t.Fatalf("translation payload missing or wrong type: %#v", session["translation"])
	}

	if got := translation["language"]; got != "zh" {
		t.Fatalf("expected target language zh, got %#v", got)
	}

	inputAudioTranscription, ok := session["input_audio_transcription"].(map[string]interface{})
	if !ok {
		t.Fatalf("input_audio_transcription payload missing or wrong type: %#v", session["input_audio_transcription"])
	}
	if got := inputAudioTranscription["language"]; got != "en" {
		t.Fatalf("expected source transcription language en, got %#v", got)
	}

	if got, ok := session["output_audio_format"].(string); !ok || got != "pcm16" {
		t.Fatalf("expected output_audio_format pcm16, got %#v", session["output_audio_format"])
	}
}

func TestBuildQwenConnectURL_AppendsModelQuery(t *testing.T) {
	got := buildQwenConnectURL("wss://example.com", "api/ws/v1/realtime", defaultQwenModel)
	want := "wss://example.com/api/ws/v1/realtime?model=" + defaultQwenModel
	if got != want {
		t.Fatalf("unexpected ws url, want %q got %q", want, got)
	}
}

func TestRedactSensitiveURL_RemovesTokenValue(t *testing.T) {
	redacted := redactSensitiveURL("wss://example.com/api/ws?token=secret-token&x=1")
	if redacted == "wss://example.com/api/ws?token=secret-token&x=1" {
		t.Fatalf("expected token to be redacted, got %q", redacted)
	}
	if redacted == "" {
		t.Fatal("expected non-empty redacted URL")
	}
}

func TestHandleQwenMessages_StopsPromptlyOnContextCancel(t *testing.T) {
	server, wsURL := startSilentWebSocketServer(t)
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket server: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- HandleQwenMessages(ctx, conn, nil, nil, nil)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case gotErr := <-done:
		if gotErr != nil && !errors.Is(gotErr, context.Canceled) {
			t.Fatalf("expected nil or context.Canceled, got %v", gotErr)
		}
	case <-time.After(700 * time.Millisecond):
		t.Fatal("HandleQwenMessages did not return after context cancel")
	}
}

func TestQwenConnWriteLockCleanup_RemovesStoredLock(t *testing.T) {
	conn := &websocket.Conn{}

	if err := withQwenConnWriteLock(conn, func() error { return nil }); err != nil {
		t.Fatalf("withQwenConnWriteLock failed: %v", err)
	}
	if _, ok := qwenConnWriteLocks.Load(conn); !ok {
		t.Fatal("expected lock to be stored for connection")
	}

	cleanupQwenConnWriteLock(conn)

	if _, ok := qwenConnWriteLocks.Load(conn); ok {
		t.Fatal("expected lock entry to be removed")
	}
}

func TestHandleQwenMessages_DoesNotCleanupWriteLockOnCancel(t *testing.T) {
	server, wsURL := startSilentWebSocketServer(t)
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket server: %v", err)
	}
	defer conn.Close()
	defer cleanupQwenConnWriteLock(conn)

	if err := withQwenConnWriteLock(conn, func() error { return nil }); err != nil {
		t.Fatalf("withQwenConnWriteLock failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- HandleQwenMessages(ctx, conn, nil, nil, nil)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case gotErr := <-done:
		if gotErr != nil && !errors.Is(gotErr, context.Canceled) {
			t.Fatalf("expected nil or context.Canceled, got %v", gotErr)
		}
	case <-time.After(700 * time.Millisecond):
		t.Fatal("HandleQwenMessages did not return after context cancel")
	}

	if _, ok := qwenConnWriteLocks.Load(conn); !ok {
		t.Fatal("expected write lock entry to remain until connection close")
	}
}
