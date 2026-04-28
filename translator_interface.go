package main

import (
	"context"
)

// TranslatorClient 翻译客户端接口
type TranslatorClient interface {
	// Connect 连接到翻译服务
	Connect(ctx context.Context) error

	// StartSession 开始翻译会话
	StartSession(ctx context.Context, config SessionConfig) error

	// SendAudioChunk 发送音频数据
	SendAudioChunk(audioData []byte) error

	// HandleMessages 处理服务器消息
	HandleMessages(ctx context.Context, textCallback TextCallback, errorCallback ErrorCallback) error

	// Close 关闭连接
	Close() error
}

// SessionConfig 会话配置
type SessionConfig struct {
	SourceLanguage string
	TargetLanguage string
	InputDevice    string
	OutputDevice   string
	Mode           string // "s2s", "mic2vmic", "speaker2speaker"
}
