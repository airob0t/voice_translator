//go:build windows
// +build windows

package main

import (
	"context"
	"fmt"
	"sync"
)

// qwenPhysicalMicToVirtualMic 使用Qwen模型实现物理麦克风到虚拟麦克风的翻译
func qwenPhysicalMicToVirtualMic(conf Config, micDevice, sourceLang, targetLang, voice string, ctx context.Context, textCallback TextCallback, errorCallback ErrorCallback) {
	safeInfo("Initializing Qwen physical mic to CABLE-A Input translation...")
	if ctx.Err() != nil {
		return
	}

	// 创建重采样器：Qwen输出24kHz，虚拟设备需要16kHz
	resampler := NewResampler(qwenOutputSampleRate, qwenInputSampleRate)
	safeInfof("Created resampler: %d Hz -> %d Hz", qwenOutputSampleRate, qwenInputSampleRate)

	// Create mpv player for CABLE-A Input (virtual speaker device) - 使用16kHz以匹配虚拟设备
	audioPlayer, err := NewPCMPlayerWithDevice(qwenInputSampleRate, 1, 16, VirtualMicRouteDevice)
	if err != nil {
		safeErrorf("Create PCM player for CABLE-A Input: %v", err)
		if errorCallback != nil {
			errorCallback(fmt.Errorf("创建CABLE-A播放器失败: %w", err))
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

	// --- Server Receiving and Write to CABLE-A Input Goroutine ---
	go func() {
		defer wg.Done()
		safeInfo("Starting Qwen message listener, writing to CABLE-A Input...")

		audioCallback := func(audioData []byte) {
			safeV(2).Infof("Received translated audio: %d bytes (24kHz), resampling to 16kHz", len(audioData))

			// 重采样：24kHz -> 16kHz
			resampledData := resampler.Resample16(audioData)
			if resampledData == nil || len(resampledData) == 0 {
				safeV(3).Info("Resampler buffering, no output yet")
				return
			}

			safeV(2).Infof("Resampled to %d bytes (16kHz), writing to CABLE-A Input", len(resampledData))

			// Write resampled PCM data to CABLE-A Input via mpv
			if err := audioPlayer.WriteAudio(resampledData); err != nil {
				safeErrorf("Failed to write audio to CABLE-A Input: %v", err)
			} else {
				safeV(3).Infof("Successfully wrote %d bytes to CABLE-A Input", len(resampledData))
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
	safeInfo("Qwen physical mic to CABLE-A Input translation finished.")
}
