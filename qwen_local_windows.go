//go:build windows
// +build windows

package main

import (
	"context"
	"fmt"
	"sync"
)

// qwenStreamSTSWithDevicesAndLanguages 使用Qwen模型实现本机翻译（物理麦克风到物理扬声器）
func qwenStreamSTSWithDevicesAndLanguages(conf Config, micDevice, speakerDevice, sourceLang, targetLang, voice string, ctx context.Context, textCallback TextCallback, errorCallback ErrorCallback) {
	safeInfof("Initializing Qwen stream STS translation (%s->%s)...", sourceLang, targetLang)
	if ctx.Err() != nil {
		return
	}

	// Create mpv player for physical speaker playback (Qwen outputs 24kHz audio)
	audioPlayer, err := NewPCMPlayerWithDevice(qwenOutputSampleRate, 1, 16, speakerDevice)
	if err != nil {
		safeErrorf("Create PCM player: %v", err)
		if errorCallback != nil {
			errorCallback(fmt.Errorf("创建音频播放器失败: %w", err))
		}
		return
	}
	defer audioPlayer.Close()
	if ctx.Err() != nil {
		return
	}

	// Connect to Qwen server
	conn, err := ConnectQwenWithContext(ctx, conf.Host, conf.Endpoint, conf.QwenAPIKey, conf.QwenModel)
	if err != nil {
		safeErrorf("Connect to Qwen server: %v", err)
		if errorCallback != nil {
			errorCallback(fmt.Errorf("连接Qwen服务器失败: %w", err))
		}
		return
	}
	defer closeQwenConn(conn)
	if ctx.Err() != nil {
		return
	}

	if voice == "" {
		voice = "Cherry"
	}

	// Send session configuration
	sessionConfig := QwenSessionConfig{
		Model:          "qwen3-livetranslate",
		SourceLanguage: sourceLang,
		TargetLanguage: targetLang,
		Voice:          voice,
		AudioEnabled:   true,
	}

	if err := SendQwenSessionConfig(conn, sessionConfig); err != nil {
		safeErrorf("Send Qwen session config: %v", err)
		if errorCallback != nil {
			errorCallback(fmt.Errorf("配置Qwen会话失败: %w", err))
		}
		return
	}

	safeInfo("Qwen session configured successfully")

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	var wg sync.WaitGroup
	wg.Add(3) // recording, sending, receiving

	// Audio buffer channel for sending
	audioChan := make(chan []byte, 20)

	// --- Audio Recording Goroutine ---
	go func() {
		defer wg.Done()
		defer runCancel()
		safeInfo("Starting audio recording from physical microphone...")

		recorder, err := NewMicrophoneRecorderWithDevice(micDevice)
		if err != nil {
			safeErrorf("Failed to create microphone recorder: %v", err)
			if errorCallback != nil {
				errorCallback(fmt.Errorf("创建麦克风录音器失败: %w", err))
			}
			runCancel()
			return
		}
		defer recorder.Close()

		recorder.StartRecording(runCtx, audioChan)
	}()

	// --- Audio Sending Goroutine ---
	go func() {
		defer wg.Done()
		runQwenAudioSender(runCtx, runCancel, audioChan, func(audioData []byte) error {
			return SendQwenAudioChunk(conn, audioData)
		}, errorCallback)
	}()

	// --- Server Receiving and Play to Physical Speaker Goroutine ---
	go func() {
		defer wg.Done()
		safeInfo("Starting Qwen message listener, playing to physical speaker...")

		audioCallback := func(audioData []byte) {
			safeInfof("Received translated audio: %d bytes, playing to physical speaker", len(audioData))

			// Play translated PCM data to physical speaker via mpv
			if err := audioPlayer.WriteAudio(audioData); err != nil {
				safeErrorf("Failed to play audio to physical speaker: %v", err)
			} else {
				safeV(3).Infof("Successfully played %d bytes to physical speaker", len(audioData))
			}
		}

		runQwenMessageReceiver(runCtx, runCancel, func(innerCtx context.Context) error {
			return HandleQwenMessages(innerCtx, conn, textCallback, audioCallback, errorCallback)
		})
	}()

	// Wait for context cancellation
	<-runCtx.Done()

	// 发送会话结束消息，清理服务器端资源（防止继续扣费）
	safeInfo("Stopping Qwen session, sending cleanup messages...")
	if err := SendQwenSessionClose(conn); err != nil {
		safeWarningf("Failed to send session close: %v", err)
	}

	// Wait for all goroutines to finish
	wg.Wait()
	safeInfof("Qwen stream STS translation (%s->%s) finished.", sourceLang, targetLang)
}
