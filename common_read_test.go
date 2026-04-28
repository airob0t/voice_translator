package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tuokentech/xingyi_client/protogen/products/understanding/ast"
)

func TestReceiveV4MessageWithCancel_StopsOnContextCancel(t *testing.T) {
	server, wsURL := startSilentWebSocketServer(t)
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket server: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(120*time.Millisecond, cancel)

	resp := new(ast.TranslateResponse)
	start := time.Now()
	err = receiveV4MessageWithCancel(ctx, conn, resp)
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("expected polling receive to stop quickly, took %s", elapsed)
	}
}

func TestReceiveV4MessageWithCancel_MapsClosedConnAfterCancelToContextCanceled(t *testing.T) {
	server, wsURL := startSilentWebSocketServer(t)
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket server: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(120*time.Millisecond, func() {
		cancel()
		_ = conn.Close()
	})

	resp := new(ast.TranslateResponse)
	err = receiveV4MessageWithCancel(ctx, conn, resp)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled when connection closes after cancel, got %v", err)
	}
}
