//go:build darwin
// +build darwin

package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// qwenVirtualSpeakerToPhysicalSpeaker 使用Qwen模型实现虚拟扬声器到物理扬声器的翻译
func qwenVirtualSpeakerToPhysicalSpeaker(conf Config, speakerDevice, sourceLang, targetLang, voice string, ctx context.Context, textCallback TextCallback, errorCallback ErrorCallback) {
	safeInfo("Initializing Qwen virtual speaker to physical speaker translation...")
	if ctx.Err() != nil {
		return
	}

	// Initialize shared memory connection for virtual speaker (output)
	if err := initializeOutputSharedMemoryConnection(); err != nil {
		safeErrorf("Initialize shared memory for virtual speaker: %v", err)
		if errorCallback != nil {
			errorCallback(fmt.Errorf("初始化虚拟扬声器失败: %w", err))
		}
		return
	}
	defer cleanupOutputSharedMemoryConnection()
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
	wg.Add(3) // reading from ring buffer, sending, receiving

	// Audio buffer channel for sending
	audioChan := make(chan []byte, 20)

	// --- Ring Buffer Reading Goroutine ---
	go func() {
		defer wg.Done()
		defer close(audioChan)
		defer runCancel()
		safeInfo("Starting virtual speaker ring buffer reader...")

		const readInterval = 80 * time.Millisecond
		readCfg := newVirtualSpeakerReadConfig(&gSharedMemoryOutput.RingBuffer, qwenInputSampleRate, qwenInputSampleRate, readInterval)
		safeInfof("Qwen virtual speaker reader configured: source=%d Hz, output=%d Hz, frames=%d",
			readCfg.sourceRate, readCfg.outputRate, readCfg.framesPerBuffer)
		buffer := make([]byte, int(readCfg.framesPerBuffer)*FrameSize)
		ticker := time.NewTicker(readInterval) // Check every 80ms
		defer ticker.Stop()

		for {
			select {
			case <-runCtx.Done():
				safeInfo("Ring buffer reader stopped by context.")
				return
			case <-ticker.C:
				if readCfg.refresh(&gSharedMemoryOutput.RingBuffer, qwenInputSampleRate, readInterval) {
					safeWarningf("Qwen virtual speaker sample rate changed, reconfiguring reader to %d Hz", readCfg.sourceRate)
					buffer = make([]byte, int(readCfg.framesPerBuffer)*FrameSize)
				}
				// Try to read from ring buffer
				framesRead := ringBufferReadFrames(&gSharedMemoryOutput.RingBuffer, buffer, readCfg.framesPerBuffer)

				if framesRead > 0 {
					bytesRead := int(framesRead * FrameSize)
					rawAudioData := make([]byte, bytesRead)
					copy(rawAudioData, buffer[:bytesRead])
					audioData := readCfg.transform(rawAudioData)
					if len(audioData) == 0 {
						safeV(3).Info("Resampler buffering, no output yet")
						continue
					}

					safeV(3).Infof("Read %d frames (%d bytes) from virtual speaker ring buffer, sent %d bytes to Qwen",
						framesRead, bytesRead, len(audioData))

					if len(audioChan) == cap(audioChan) {
						safeV(2).Info("Audio send channel full, waiting for sender")
					}
					if !sendAudioWithBackpressure(runCtx, audioChan, audioData) {
						return
					}
				}
			}
		}
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
	safeInfo("Qwen virtual speaker to physical speaker translation finished.")
}
