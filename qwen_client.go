package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// QwenClient Qwen翻译客户端，处理JSON格式的websocket通信
type QwenClient struct {
	conn        *websocket.Conn
	writeMu     sync.Mutex
	sourceQueue chan []byte
	targetQueue chan []byte
	ctx         context.Context
	cancel      context.CancelFunc
}

// QwenSessionConfig Qwen会话配置
type QwenSessionConfig struct {
	Model          string
	SourceLanguage string
	TargetLanguage string
	Voice          string
	AudioEnabled   bool
}

// QwenServerEvent Qwen服务器事件
type QwenServerEvent struct {
	EventID    string                 `json:"event_id"`
	Type       string                 `json:"type"`
	Delta      string                 `json:"delta,omitempty"`
	Transcript string                 `json:"transcript,omitempty"`
	Text       string                 `json:"text,omitempty"`
	Response   map[string]interface{} `json:"response,omitempty"`
}

var qwenConnWriteLocks sync.Map // map[*websocket.Conn]*sync.Mutex

const qwenWriteTimeout = 3 * time.Second

type qwenWriteConn interface {
	SetWriteDeadline(time.Time) error
	WriteMessage(int, []byte) error
	WriteControl(int, []byte, time.Time) error
}

func isRetryableQwenReadError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if !errors.As(err, &netErr) {
		return false
	}
	return netErr.Timeout()
}

func buildQwenConnectURL(host, endpoint, model string) string {
	base := strings.TrimRight(host, "/")
	ep := strings.TrimLeft(endpoint, "/")
	rawURL := base
	if ep != "" {
		rawURL = base + "/" + ep
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if model != "" {
		q := u.Query()
		q.Set("model", model)
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func redactSensitiveURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	if q.Has("token") {
		q.Set("token", "REDACTED")
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func buildQwenSessionUpdatePayload(config QwenSessionConfig) map[string]interface{} {
	translation := map[string]interface{}{
		"language": config.TargetLanguage,
	}

	session := map[string]interface{}{
		"input_audio_format": "pcm16",
		"translation":        translation,
	}

	if config.SourceLanguage != "" {
		session["input_audio_transcription"] = map[string]interface{}{
			"language": config.SourceLanguage,
		}
	}

	if config.AudioEnabled {
		session["modalities"] = []string{"text", "audio"}
		session["output_audio_format"] = "pcm16"
		if config.Voice != "" {
			session["voice"] = config.Voice
		}
	} else {
		session["modalities"] = []string{"text"}
	}

	return map[string]interface{}{
		"event_id": fmt.Sprintf("event_%d", time.Now().UnixMilli()),
		"type":     "session.update",
		"session":  session,
	}
}

func withQwenConnWriteLock(conn *websocket.Conn, fn func() error) error {
	if conn == nil {
		return errors.New("nil websocket connection")
	}

	lockAny, _ := qwenConnWriteLocks.LoadOrStore(conn, &sync.Mutex{})
	lock := lockAny.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	return fn()
}

func cleanupQwenConnWriteLock(conn *websocket.Conn) {
	if conn == nil {
		return
	}
	qwenConnWriteLocks.Delete(conn)
}

func releaseQwenConnWriteLock(conn *websocket.Conn) {
	cleanupQwenConnWriteLock(conn)
}

func closeQwenConn(conn *websocket.Conn) error {
	if conn == nil {
		return nil
	}
	cleanupQwenConnWriteLock(conn)
	return conn.Close()
}

func writeQwenMessageWithDeadline(conn qwenWriteConn, messageType int, data []byte) error {
	if conn == nil {
		return errors.New("nil websocket connection")
	}
	deadline := time.Now().Add(qwenWriteTimeout)
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	return conn.WriteMessage(messageType, data)
}

func writeQwenControlWithDeadline(conn qwenWriteConn, messageType int, data []byte) error {
	if conn == nil {
		return errors.New("nil websocket connection")
	}
	deadline := time.Now().Add(qwenWriteTimeout)
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	return conn.WriteControl(messageType, data, deadline)
}

func readQwenMessageWithCancel(ctx context.Context, conn *websocket.Conn) ([]byte, error) {
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

	_, message, err := conn.ReadMessage()
	if err == nil {
		return message, nil
	}

	if ctx.Err() != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return nil, ctx.Err()
		}
	}

	return nil, err
}

// ConnectQwenWithContext 连接到Qwen翻译服务，并支持通过 context 取消拨号。
func ConnectQwenWithContext(ctx context.Context, host, endpoint, apiKey, model string) (*websocket.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("missing qwen api key")
	}

	wsURL := buildQwenConnectURL(host, endpoint, model)

	header := http.Header{}
	header.Set("Authorization", "Bearer "+apiKey)

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, resp, err := dialer.DialContext(ctx, wsURL, header)
	if err != nil {
		if resp != nil {
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr == nil && len(body) > 0 {
				return nil, fmt.Errorf("dial qwen server failed: %w (status=%s, body=%s)", err, resp.Status, string(body))
			}
		}
		return nil, fmt.Errorf("dial qwen server failed: %w", err)
	}
	if resp != nil {
		resp.Body.Close()
	}

	safeInfof("Connected to Qwen server: %s", redactSensitiveURL(wsURL))
	return conn, nil
}

// ConnectQwen 连接到Qwen翻译服务
func ConnectQwen(host, endpoint, apiKey, model string) (*websocket.Conn, error) {
	return ConnectQwenWithContext(context.Background(), host, endpoint, apiKey, model)
}

// SendQwenSessionConfig 发送Qwen会话配置
func SendQwenSessionConfig(conn *websocket.Conn, config QwenSessionConfig) error {
	payload := buildQwenSessionUpdatePayload(config)

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal session config: %w", err)
	}

	if err := withQwenConnWriteLock(conn, func() error {
		return writeWSMessageWithTimeout(conn, websocket.TextMessage, data, websocketWriteTimeout)
	}); err != nil {
		return fmt.Errorf("send session config: %w", err)
	}

	safeV(3).Infof("Sent Qwen session config: %s", string(data))
	return nil
}

// SendQwenAudioChunk 发送音频数据到Qwen
func SendQwenAudioChunk(conn *websocket.Conn, audioData []byte) error {
	event := map[string]interface{}{
		"event_id": fmt.Sprintf("event_%d", time.Now().UnixMilli()),
		"type":     "input_audio_buffer.append",
		"audio":    base64.StdEncoding.EncodeToString(audioData),
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal audio event: %w", err)
	}

	if err := withQwenConnWriteLock(conn, func() error {
		return writeWSMessageWithTimeout(conn, websocket.TextMessage, data, websocketWriteTimeout)
	}); err != nil {
		return fmt.Errorf("send audio chunk: %w", err)
	}

	safeV(3).Infof("Sent audio chunk: %d bytes", len(audioData))
	return nil
}

// SendQwenSessionClose 发送会话结束消息，清理服务器端资源
func SendQwenSessionClose(conn *websocket.Conn) error {
	// 1. 发送 input_audio_buffer.clear 清理待处理的音频
	clearEvent := map[string]interface{}{
		"event_id": fmt.Sprintf("event_%d", time.Now().UnixMilli()),
		"type":     "input_audio_buffer.clear",
	}

	data, err := json.Marshal(clearEvent)
	if err != nil {
		return fmt.Errorf("marshal clear event: %w", err)
	}

	if err := withQwenConnWriteLock(conn, func() error {
		if err := writeWSMessageWithTimeout(conn, websocket.TextMessage, data, websocketWriteTimeout); err != nil {
			safeWarningf("Failed to send input_audio_buffer.clear: %v", err)
		} else {
			safeInfo("Sent input_audio_buffer.clear to Qwen server")
		}

		// 2. 发送正常的 WebSocket 关闭帧
		closeMessage := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session ended")
		if err := conn.WriteControl(websocket.CloseMessage, closeMessage, time.Now().Add(time.Second)); err != nil {
			safeWarningf("Failed to send WebSocket close message: %v", err)
			return err
		}
		safeInfo("Sent WebSocket close message to Qwen server")
		return nil
	}); err != nil {
		return fmt.Errorf("send session close: %w", err)
	}

	return nil
}

// HandleQwenMessages 处理Qwen服务器消息
func HandleQwenMessages(ctx context.Context, conn *websocket.Conn, textCallback TextCallback, audioCallback func([]byte), errorCallback ErrorCallback) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		message, err := readQwenMessageWithCancel(ctx, conn)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			// WebSocket 正常关闭（用户主动停止）不视为错误
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return nil
			}
			if errorCallback != nil {
				errorCallback(fmt.Errorf("read message: %w", err))
			}
			return err
		}

		var event QwenServerEvent
		if err := json.Unmarshal(message, &event); err != nil {
			safeWarningf("Failed to parse Qwen message: %v", err)
			continue
		}

		safeV(3).Infof("Received Qwen event: %s", event.Type)

		switch event.Type {
		case "session.created":
			safeInfo("Qwen session created")

		case "session.updated":
			safeInfo("Qwen session updated")

		case "response.audio.delta":
			if event.Delta != "" {
				audioData, decodeErr := base64.StdEncoding.DecodeString(event.Delta)
				if decodeErr != nil {
					safeWarningf("Failed to decode audio data: %v", decodeErr)
					continue
				}
				if audioCallback != nil {
					audioCallback(audioData)
				}
			}

		case "response.audio_transcript.done":
			if event.Transcript != "" {
				safeInfof("Translation text: %s", event.Transcript)
				if textCallback != nil {
					textCallback("", event.Transcript)
				}
			}

		case "response.text.done":
			if event.Text != "" {
				safeInfof("Translation text: %s", event.Text)
				if textCallback != nil {
					textCallback("", event.Text)
				}
			}

		case "response.done":
			safeV(3).Info("Response completed")

		case "error":
			// 如果 context 已取消（用户主动停止），忽略服务器返回的错误
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			errorMsg := "Unknown error"
			if event.Response != nil {
				if msg, ok := event.Response["message"].(string); ok {
					errorMsg = msg
				}
			}
			safeErrorf("Qwen server error: %s", errorMsg)
			if errorCallback != nil {
				errorCallback(fmt.Errorf("server error: %s", errorMsg))
			}
			return fmt.Errorf("qwen server error: %s", errorMsg)
		}
	}
}
