package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

const websocketWriteTimeout = 3 * time.Second
const (
	dialRetryMaxAttempts = 3
	dialRetryBaseDelay   = 200 * time.Millisecond
)

var websocketDialContext = websocket.DefaultDialer.DialContext
var dialRetrySleep = time.Sleep

type wsMessageWriter interface {
	SetWriteDeadline(t time.Time) error
	WriteMessage(messageType int, data []byte) error
}

func writeWSMessageWithTimeout(conn wsMessageWriter, messageType int, data []byte, timeout time.Duration) error {
	if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	defer conn.SetWriteDeadline(time.Time{})

	if err := conn.WriteMessage(messageType, data); err != nil {
		return err
	}
	return nil
}

func buildHTTPHeader(conf Config, connID string) http.Header {
	h := http.Header{
		"X-Api-App-Key":     []string{conf.DoubaoAppID},
		"X-Api-Access-Key":  []string{conf.DoubaoAccessKey},
		"X-Api-Resource-Id": []string{conf.DoubaoResourceID},
		"X-Api-Connect-Id":  []string{connID},
	}
	return h
}

func dial(conf Config, connID string) (*websocket.Conn, error) {
	addr := fmt.Sprintf("%s/%s", conf.Host, conf.Endpoint)
	// safeInfof("Dial server: %s", addr)
	header := buildHTTPHeader(conf, connID)

	for attempt := 1; attempt <= dialRetryMaxAttempts; attempt++ {
		conn, r, connErr := websocketDialContext(context.Background(), addr, header)
		if r != nil {
			logID := r.Header.Get("X-Tt-Logid")
			safeInfof("Dial server with LogID: %s", logID)
		}
		if connErr == nil {
			return conn, nil
		}

		wrappedErr, statusCode, body := wrapDialError(connErr, r)
		if attempt < dialRetryMaxAttempts && shouldRetryDial(statusCode, body, connErr) {
			safeWarningf("Dial transient failure (attempt %d/%d), retrying: %v", attempt, dialRetryMaxAttempts, wrappedErr)
			dialRetrySleep(time.Duration(attempt) * dialRetryBaseDelay)
			continue
		}
		return nil, wrappedErr
	}

	return nil, fmt.Errorf("dial failed after retries")
}

func wrapDialError(connErr error, response *http.Response) (error, int, string) {
	if response == nil {
		return connErr, 0, ""
	}

	bodyText := "empty response body"
	if response.Body != nil {
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			bodyText = fmt.Sprintf("parse response body failed: %v", err)
		} else {
			bodyText = string(body)
		}
	}

	return fmt.Errorf("[code=%s] [body=%s] %w", response.Status, bodyText, connErr), response.StatusCode, bodyText
}

func shouldRetryDial(statusCode int, body string, connErr error) bool {
	if statusCode == http.StatusServiceUnavailable {
		normalizedBody := strings.ToLower(strings.TrimSpace(body))
		if normalizedBody == "" || normalizedBody == "empty response body" {
			return true
		}
		if strings.Contains(normalizedBody, "connection guard temporarily unavailable") {
			return true
		}
	}

	var netErr net.Error
	return errors.As(connErr, &netErr) && netErr.Timeout()
}

func normalClose(conn *websocket.Conn) {
	defer conn.Close()
	normalClosure := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
	if err := conn.WriteControl(websocket.CloseMessage, normalClosure, time.Now().Add(time.Second)); err != nil {
		safeErrorf("Write websocket NormalClosure: %v", err)
	}
}

func readAudioChunks(path string, chunkSize int) ([][]byte, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	max := len(content)

	var chunks [][]byte
	for i := 0; i < max; i += chunkSize {
		if i+chunkSize < max {
			chunks = append(chunks, content[i:i+chunkSize])
		} else {
			chunks = append(chunks, content[i:])
		}
	}
	return chunks, nil
}

func sendV4Request(conn *websocket.Conn, req proto.Message) error {
	frame, err := proto.Marshal(req)
	if err != nil {
		return fmt.Errorf("proto marshal failed:%w", err)
	}

	if err := writeWSMessageWithTimeout(conn, websocket.BinaryMessage, frame, websocketWriteTimeout); err != nil {
		return fmt.Errorf("send V4 request: %w", err)
	}

	safeV(3).Info("V4 request is sent.")
	return nil
}

func receiveV4Message(conn *websocket.Conn, resp proto.Message) error {
	mt, frame, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read message: %w", err)
	}
	if mt != websocket.BinaryMessage && mt != websocket.TextMessage {
		return fmt.Errorf("unexpected Websocket message type: %d", mt)
	}

	if err := proto.Unmarshal(frame, resp); err != nil {
		safeWarningf("Unable to unmarshal response message: %v", frame)
		return fmt.Errorf("unmarshal response message: %w", err)
	}
	return nil
}

func receiveV4MessageWithCancel(ctx context.Context, conn *websocket.Conn, resp proto.Message) error {
	readDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.SetReadDeadline(time.Now())
		case <-readDone:
		}
	}()
	defer close(readDone)
	defer conn.SetReadDeadline(time.Time{})

	err := receiveV4Message(conn, resp)
	if err == nil {
		return nil
	}

	if ctx.Err() != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return ctx.Err()
		}
		if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
			return ctx.Err()
		}
		var closeErr *websocket.CloseError
		if errors.As(err, &closeErr) {
			return ctx.Err()
		}
		if strings.Contains(strings.ToLower(err.Error()), "closed") {
			return ctx.Err()
		}
	}

	return err
}

func runV4AudioSender(ctx context.Context, runCancel context.CancelFunc, audioChan <-chan []byte, send func([]byte) error, errorCallback ErrorCallback) {
	for {
		select {
		case <-ctx.Done():
			return
		case audioData, ok := <-audioChan:
			if !ok {
				return
			}

			if err := send(audioData); err != nil {
				// Treat cancellation/normal-close write failures as expected teardown noise.
				if shouldSuppressV4SendError(ctx, err) {
					safeV(2).Infof("Audio sender exiting after context cancellation: %v", err)
					return
				}
				if errorCallback != nil {
					errorCallback(fmt.Errorf("发送音频失败: %v", err))
				}
				if runCancel != nil {
					runCancel()
				}
				return
			}
		}
	}
}

func shouldSuppressV4SendError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
		return true
	}
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) {
		switch closeErr.Code {
		case websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived:
			return true
		}
	}
	return false
}

func shakeHands(conn *websocket.Conn, req, resp proto.Message) error {
	if err := sendV4Request(conn, req); err != nil {
		return fmt.Errorf("start connection: %w", err)
	}

	if err := receiveV4Message(conn, resp); err != nil {
		return fmt.Errorf("receive ConnectionStarted response: %w", err)
	}

	//if event.Type(msg.Event) != event.Type_ConnectionStarted {
	//	return fmt.Errorf("unexpected response event (%s) for StartConnection request", event.Type(msg.Event))
	//}
	safeInfof("Shake-hands done: %s", resp)
	return nil
}

// writeWAVHeader writes a standard WAV file header
// sampleRate: e.g., 16000, 44100
// channels: 1 for mono, 2 for stereo
// bitsPerSample: typically 16 or 24
// dataSize: size of PCM data in bytes (0 if not yet known)
func writeWAVHeader(w io.WriteSeeker, sampleRate, channels, bitsPerSample, dataSize int) error {
	// WAV file format:
	// RIFF header (12 bytes)
	// fmt chunk (24 bytes)
	// data chunk header (8 bytes)
	// Total header: 44 bytes

	byteRate := sampleRate * channels * bitsPerSample / 8
	blockAlign := channels * bitsPerSample / 8
	chunkSize := 36 + dataSize // Total size - 8 bytes

	// RIFF header
	if _, err := w.Write([]byte("RIFF")); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(chunkSize)); err != nil {
		return err
	}
	if _, err := w.Write([]byte("WAVE")); err != nil {
		return err
	}

	// fmt subchunk
	if _, err := w.Write([]byte("fmt ")); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(16)); err != nil { // Subchunk1Size (16 for PCM)
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, uint16(1)); err != nil { // AudioFormat (1 for PCM)
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, uint16(channels)); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(sampleRate)); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(byteRate)); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, uint16(blockAlign)); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, uint16(bitsPerSample)); err != nil {
		return err
	}

	// data subchunk
	if _, err := w.Write([]byte("data")); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(dataSize)); err != nil {
		return err
	}

	return nil
}
