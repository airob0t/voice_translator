//go:build darwin
// +build darwin

package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// qwenPhysicalMicToVirtualMic 使用Qwen模型实现物理麦克风到虚拟麦克风的翻译
func qwenPhysicalMicToVirtualMic(conf Config, micDevice, sourceLang, targetLang, voice string, ctx context.Context, textCallback TextCallback, errorCallback ErrorCallback) {
	safeInfo("Initializing Qwen physical mic to virtual mic translation...")
	if ctx.Err() != nil {
		return
	}

	// Initialize shared memory connection for virtual microphone (output to virtual mic)
	if err := initializeSharedMemoryConnection(); err != nil {
		safeErrorf("Initialize shared memory for virtual microphone: %v", err)
		if errorCallback != nil {
			errorCallback(fmt.Errorf("初始化虚拟麦克风失败: %w", err))
		}
		return
	}
	defer cleanupSharedMemoryConnection()
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
	wg.Add(4) // recording, sending, receiving, paced writing

	// Audio buffer channel for sending
	audioChan := make(chan []byte, 20)
	audioWriteQueue := make(chan []byte, 20)

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

	// --- Rate-Controlled Ring Buffer Writing Goroutine ---
	go func() {
		defer wg.Done()
		safeInfo("Starting Qwen paced virtual microphone writer...")

		const writeInterval = 80 * time.Millisecond
		writeCfg := newVirtualMicWriteConfig(&gSharedMemory.RingBuffer, qwenOutputSampleRate, int(SampleRate), writeInterval)
		safeInfof("Configured virtual mic output rate: %d Hz (Qwen output: %d Hz, write chunk=%d bytes)", writeCfg.outputRate, qwenOutputSampleRate, writeCfg.chunkBytes)

		var audioBuffer []byte
		ticker := time.NewTicker(writeInterval)
		defer ticker.Stop()

		for {
			select {
			case <-runCtx.Done():
				return
			case audioData, ok := <-audioWriteQueue:
				if !ok {
					if len(audioBuffer) > 0 {
						frameCount := uint32(len(audioBuffer) / FrameSize)
						ringBufferWriteFrames(&gSharedMemory.RingBuffer, audioBuffer, frameCount)
					}
					return
				}

				if writeCfg.refresh(&gSharedMemory.RingBuffer, int(SampleRate), writeInterval) {
					safeWarningf("Virtual microphone sample rate changed, reconfiguring writer to %d Hz", writeCfg.outputRate)
					// Pending buffer is encoded for the previous sample rate, drop it to avoid speed/pitch artifacts.
					audioBuffer = nil
				}

				outputData := writeCfg.transform(audioData)
				if len(outputData) == 0 {
					safeV(3).Info("Resampler buffering, no output yet")
					continue
				}
				audioBuffer = append(audioBuffer, outputData...)

			case <-ticker.C:
				if writeCfg.refresh(&gSharedMemory.RingBuffer, int(SampleRate), writeInterval) {
					safeWarningf("Virtual microphone sample rate changed, reconfiguring writer to %d Hz", writeCfg.outputRate)
					// Pending buffer is encoded for the previous sample rate, drop it to avoid speed/pitch artifacts.
					audioBuffer = nil
				}
				if len(audioBuffer) >= writeCfg.chunkBytes {
					chunk := audioBuffer[:writeCfg.chunkBytes]
					frameCount := uint32(len(chunk) / FrameSize)
					ringBufferWriteFrames(&gSharedMemory.RingBuffer, chunk, frameCount)
					audioBuffer = audioBuffer[writeCfg.chunkBytes:]
				} else if len(audioBuffer) > 0 {
					frameCount := uint32(len(audioBuffer) / FrameSize)
					ringBufferWriteFrames(&gSharedMemory.RingBuffer, audioBuffer, frameCount)
					audioBuffer = nil
				}
			}
		}
	}()

	// --- Server Receiving and Write to Virtual Microphone Goroutine ---
	go func() {
		defer wg.Done()
		defer close(audioWriteQueue)
		safeInfo("Starting Qwen message listener, writing to virtual microphone...")

		audioCallback := func(audioData []byte) {
			if len(audioWriteQueue) == cap(audioWriteQueue) {
				safeV(2).Info("Qwen audio write queue full, waiting for writer")
			}
			if !sendAudioWithBackpressure(runCtx, audioWriteQueue, audioData) {
				return
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
	safeInfo("Qwen physical mic to virtual mic translation finished.")
}
