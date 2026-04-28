//go:build windows
// +build windows

package main

import (
	"context"
	"fmt"
	"sync"
)

// qwenVirtualSpeakerToPhysicalSpeaker 使用Qwen模型实现虚拟扬声器到物理扬声器的翻译
func qwenVirtualSpeakerToPhysicalSpeaker(conf Config, speakerDevice, sourceLang, targetLang, voice string, ctx context.Context, textCallback TextCallback, errorCallback ErrorCallback) {
	safeInfo("Initializing Qwen CABLE-B Output to physical speaker translation...")
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

	// Create FFmpeg recorder for CABLE-B Output (virtual microphone)
	cableBRecorder, err := NewMicrophoneRecorderWithDevice("audio=CABLE-B Output (VB-Audio Cable B)")
	if err != nil {
		safeErrorf("Create CABLE-B Output recorder: %v", err)
		if errorCallback != nil {
			errorCallback(fmt.Errorf("创建CABLE-B录音器失败: %w", err))
		}
		return
	}
	defer cableBRecorder.Close()
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

	// --- CABLE-B Recording Goroutine ---
	go func() {
		defer wg.Done()
		defer runCancel()
		safeInfo("Starting CABLE-B Output recording...")
		cableBRecorder.StartRecording(runCtx, audioChan)
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
	safeInfo("Qwen CABLE-B Output to physical speaker translation finished.")
}
