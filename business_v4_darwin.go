//go:build darwin
// +build darwin

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tuokentech/xingyi_client/protogen/common/event"
	"github.com/tuokentech/xingyi_client/protogen/common/rpcmeta"
	"github.com/tuokentech/xingyi_client/protogen/products/understanding/ast"
	"github.com/tuokentech/xingyi_client/protogen/products/understanding/base"

	"github.com/golang/glog"
	"github.com/google/uuid"
)

// translateV4 sends audio chunks to server and receives translated text.
func translateV4(conf Config, audio string, n int) {
	audioChunks, err := readAudioChunks(audio, 3200) // chunk size: 100ms
	if err != nil {
		glog.Exitf("Read audio chunks from file: %v", err)
	}

	conn, err := dial(conf, uuid.New().String())
	if err != nil {
		glog.Exitf("Dial server: %v", err)
	}
	defer normalClose(conn)

	sessionId := uuid.New().String()
	translateRequest := &ast.TranslateRequest{
		RequestMeta: &rpcmeta.RequestMeta{
			SessionID: sessionId,
		},
		Event: event.Type_StartSession,
		User: &base.User{
			Uid: "xingyi_client_client",
			Did: "xingyi_client_client",
		},
		SourceAudio: &base.Audio{
			Format:  "wav",
			Rate:    16000,
			Bits:    16,
			Channel: 1,
		},
		TargetAudio: &base.Audio{
			Format: "ogg_opus",
			Rate:   48000,
		},
		Request: &ast.ReqParams{
			Mode:           "s2s",
			SourceLanguage: "zh",
			TargetLanguage: "en",
		},
		Denoise: nil,
	}
	if err := shakeHands(conn, translateRequest, new(ast.TranslateResponse)); err != nil {
		glog.Exitf("Start session: %v", err)
	}
	safeInfof("Session (ID=%s) started.", sessionId)

	go func() {
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()

		for _, chunk := range audioChunks {
			safeInfof("Sending chunk: %d", len(chunk))
			if err := sendV4Request(conn, &ast.TranslateRequest{
				RequestMeta: &rpcmeta.RequestMeta{
					SessionID: sessionId,
				},
				Event: event.Type_TaskRequest,
				SourceAudio: &base.Audio{
					BinaryData: chunk,
				},
			}); err != nil {
				glog.Exitf("Send audio chunk: %v", err)
			}
			<-t.C
		}

		if err := sendV4Request(conn, &ast.TranslateRequest{
			RequestMeta: &rpcmeta.RequestMeta{
				SessionID: sessionId,
			},
			Event: event.Type_FinishSession,
		}); err != nil {
			glog.Exitf("Finish session: %v", err)
		}
		safeInfo("FinishSession request is sent.")
	}()

	var recvAudio bytes.Buffer
	var recvText strings.Builder
	for {
		safeInfof("Waiting for message...")
		resp := new(ast.TranslateResponse)
		if err := receiveV4Message(conn, resp); err != nil {
			safeErrorf("Receive message error: %v", err)
			break
		}

		if resp.GetEvent() == event.Type_SessionFailed {
			safeInfof("(session_id=%s) failed, status code:%d, error message:%s", resp.GetResponseMeta().GetSessionID(), resp.GetResponseMeta().GetStatusCode(), resp.GetResponseMeta().GetMessage())
			break
		} else if resp.GetEvent() == event.Type_SessionCanceled {
			safeInfof("(session_id=%s) canceled", resp.GetResponseMeta().GetSessionID())
			break
		} else if resp.GetEvent() == event.Type_SessionFinished {
			safeInfof("(session_id=%s) finished", resp.GetResponseMeta().GetSessionID())
			break
		}
		if resp.GetEvent() == event.Type_UsageResponse {
			safeInfof("Receive message (session_id=%s, event=%s), text:%s",
				resp.GetResponseMeta().GetSessionID(), resp.GetEvent(), resp.String())
		} else {
			safeInfof("Receive message (session_id=%s, event=%s), seq:%d, text:%s, audio data length:%d",
				resp.GetResponseMeta().GetSessionID(), resp.GetEvent(), resp.GetResponseMeta().GetSequence(), resp.GetText(), len(resp.GetData()))
			safeV(3).Infof("Receive message: %+v", resp)
			recvAudio.Write(resp.GetData())
			recvText.WriteString(resp.GetText())
		}
	}

	if recvAudio.Len() > 0 {
		path := filepath.Join(*outdir, fmt.Sprintf("v4_translate_audio_%05d.opus", n))
		if err := os.WriteFile(path, recvAudio.Bytes(), 0644); err != nil {
			glog.Exitf("Save audio file: %v", err)
		}
		safeInfof("Session finished, audio is saved as: %s", path)
		safeInfof("Session finished, text is: %s", recvText.String())
	} else {
		safeInfo("Session finished, no audio data is received.")
	}
}

const (
	// Input audio configuration (microphone)
	INPUT_SAMPLE_RATE   = 16000
	INPUT_CHANNELS      = 1
	INPUT_FRAMES_BUFFER = 2560 // 160ms at 16kHz (doubled buffer size to prevent overflow)

	// Output audio configuration (speaker)
	OUTPUT_SAMPLE_RATE   = 44100
	OUTPUT_CHANNELS      = 2
	OUTPUT_FRAMES_BUFFER = 3528 // 80ms at 44.1kHz
)

// streamSTSV4 handles speech-to-speech translation by streaming audio from the microphone
// to the server and playing back the translated audio.
func streamSTSV4(conf Config) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	safeInfo("Initializing mpv for PCM audio playback...")

	// Create mpv player for PCM streaming playback
	audioPlayer, err := NewPCMPlayer(16000, 1, 16)
	if err != nil {
		glog.Exitf("Create PCM player: %v", err)
	}
	defer audioPlayer.Close()

	// Create FFmpeg microphone recorder
	micRecorder, err := NewMicrophoneRecorder()
	if err != nil {
		glog.Exitf("Create microphone recorder: %v", err)
	}
	defer micRecorder.Close()

	// --- Setup WebSocket Connection ---
	conn, err := dial(conf, uuid.New().String())
	if err != nil {
		glog.Exitf("Dial server: %v", err)
		return
	}
	defer normalClose(conn)

	sessionId := uuid.New().String()
	translateRequest := &ast.TranslateRequest{
		RequestMeta: &rpcmeta.RequestMeta{
			SessionID: sessionId,
		},
		Event: event.Type_StartSession,
		User: &base.User{
			Uid: "sts_go_client",
			Did: "sts_go_client",
		},
		SourceAudio: &base.Audio{
			Format:  "pcm",
			Rate:    INPUT_SAMPLE_RATE,
			Bits:    16,
			Channel: INPUT_CHANNELS,
		},
		TargetAudio: &base.Audio{
			Format: "pcm",
			Rate:   16000,
			Bits:   16,
		},
		Request: &ast.ReqParams{
			Mode:           "s2s",
			SourceLanguage: "zh",
			TargetLanguage: "en",
		},
	}

	if err := shakeHands(conn, translateRequest, new(ast.TranslateResponse)); err != nil {
		glog.Exitf("Start session: %v", err)
		return
	}
	safeInfof("Session (ID=%s) started. Please start speaking.", sessionId)

	var wg sync.WaitGroup
	wg.Add(4) // Now we have 4 goroutines: recording, sending, receiving, and playing

	// Audio buffer channel to decouple reading from sending
	audioChan := make(chan []byte, 10) // Increased buffer to handle more audio chunks

	// Audio playback queue - small buffer to reduce latency while maintaining smoothness
	audioPlayQueue := make(chan []byte, 10) // 10 chunks = ~160ms buffer (assuming 16ms per chunk)
	var closePlayQueueOnce sync.Once
	closePlayQueue := func() {
		closePlayQueueOnce.Do(func() {
			close(audioPlayQueue)
		})
	}

	// Close WebSocket on cancellation to unblock receiveV4Message.
	go func() {
		<-ctx.Done()
		safeInfo("Context cancelled, closing WebSocket connection...")
		conn.Close()
	}()

	// WAV file for streaming audio backup
	wavFileName := "debug_complete_audio.wav"
	wavFile, err := os.Create(wavFileName)
	if err != nil {
		glog.Exitf("Create WAV file: %v", err)
	}
	defer wavFile.Close()

	pcmFile, err := os.Create("debug_pcm.pcm")
	if err != nil {
		glog.Exitf("Create PCM file: %v", err)
	}
	defer pcmFile.Close()

	// Write WAV header (will update at the end)
	if err := writeWAVHeader(wavFile, 16000, 1, 16, 0); err != nil {
		glog.Exitf("Write WAV header: %v", err)
	}
	safeInfof("Created WAV backup file: %s", wavFileName)

	var totalAudioBytes int
	var audioMutex sync.Mutex

	// --- Microphone Recording Goroutine ---
	go func() {
		defer wg.Done()
		safeInfo("Starting FFmpeg microphone recording...")

		// Use FFmpeg microphone recorder
		micRecorder.StartRecording(ctx, audioChan)
	}()

	// --- Audio Sending Goroutine ---
	go func() {
		defer wg.Done()
		safeInfo("Starting audio sender...")

		for {
			select {
			case <-ctx.Done():
				safeInfo("Audio sender stopped by context.")
				return
			case audioData, ok := <-audioChan:
				if !ok {
					safeInfo("Audio channel closed.")
					return
				}

				if err := sendV4Request(conn, &ast.TranslateRequest{
					RequestMeta: &rpcmeta.RequestMeta{SessionID: sessionId},
					Event:       event.Type_TaskRequest,
					SourceAudio: &base.Audio{BinaryData: audioData},
				}); err != nil {
					safeErrorf("Send audio chunk: %v", err)
					cancel()
					return
				} else {
					safeV(3).Infof("Sent %d bytes to translation service", len(audioData))
				}
			}
		}
	}()

	// --- Audio Playback Goroutine ---
	go func() {
		defer wg.Done()
		safeInfo("Starting audio playback goroutine...")

		playAudio := func(audioData []byte) {
			if err := audioPlayer.WriteAudio(audioData); err != nil {
				safeErrorf("Failed to play audio: %v", err)
				cancel()
				return
			}
			safeV(3).Infof("Successfully played %d bytes from queue", len(audioData))
		}

		for audioData := range audioPlayQueue {
			playAudio(audioData)
		}
		safeInfo("Audio playback queue closed.")
	}()

	// --- Server Receiving and FFmpeg Playback Goroutine ---
	go func() {
		defer wg.Done()
		safeInfo("Starting server message listener with FFmpeg playback...")

		for {
			select {
			case <-ctx.Done():
				safeInfo("Server listener stopped by context.")
				closePlayQueue()
				return
			default:
			}

			// Set a longer timeout for message receiving to avoid frequent timeouts
			// conn.SetReadDeadline(time.Now().Add(5 * time.Second))

			resp := new(ast.TranslateResponse)
			if err := receiveV4MessageWithCancel(ctx, conn, resp); err != nil {
				if err == context.Canceled {
					safeInfo("Server listener stopped by context.")
					closePlayQueue()
					return
				}
				if err == io.EOF || strings.Contains(err.Error(), "closed") {
					safeInfo("Connection closed by server.")
				} else {
					safeErrorf("Receive message error: %v", err)
				}
				closePlayQueue()
				cancel()
				return
			}

			switch resp.GetEvent() {
			case event.Type_SessionFailed, event.Type_SessionCanceled, event.Type_SessionFinished:
				safeInfof("Session ended (event: %s, session_id: %s, message: %s)",
					resp.GetEvent(), resp.GetResponseMeta().GetSessionID(), resp.GetResponseMeta().GetMessage())

				// Update WAV header with final size
				audioMutex.Lock()
				if totalAudioBytes > 0 {
					wavFile.Seek(0, 0)
					if err := writeWAVHeader(wavFile, 16000, 1, 16, totalAudioBytes); err == nil {
						safeInfof("Updated WAV header: %s (%d bytes)", wavFileName, totalAudioBytes)
					}
				}
				audioMutex.Unlock()

				// Close playback queue to let playback goroutine finish remaining audio
				closePlayQueue()
				safeInfo("Closed audio playback queue, waiting for remaining audio to play...")

				cancel()
				return
			case event.Type_UsageResponse:
				safeInfof("Receive usage (session_id=%s, event=%s), text:%s",
					resp.GetResponseMeta().GetSessionID(), resp.GetEvent(), resp.String())
			case event.Type_SourceSubtitleStart:
				safeInfof("Receive source text start (session_id=%s, event=%s)",
					resp.GetResponseMeta().GetSessionID(), resp.GetEvent())
			case event.Type_SourceSubtitleResponse:
				safeInfof("Receive source text response (session_id=%s, event=%s), text:%s",
					resp.GetResponseMeta().GetSessionID(), resp.GetEvent(), resp.GetText())
			case event.Type_SourceSubtitleEnd:
				safeInfof("Receive source text end (session_id=%s, event=%s)",
					resp.GetResponseMeta().GetSessionID(), resp.GetEvent())
			case event.Type_TranslationSubtitleStart:
				safeInfof("Receive translation text start (session_id=%s, event=%s)",
					resp.GetResponseMeta().GetSessionID(), resp.GetEvent())
			case event.Type_TranslationSubtitleResponse:
				safeInfof("Receive translation text response (session_id=%s, event=%s), text:%s",
					resp.GetResponseMeta().GetSessionID(), resp.GetEvent(), resp.GetText())
			case event.Type_TranslationSubtitleEnd:
				safeInfof("Receive translation text end (session_id=%s, event=%s)",
					resp.GetResponseMeta().GetSessionID(), resp.GetEvent())
			case event.Type_TTSSentenceStart:
				safeInfof("Receive TTS start (session_id=%s, event=%s)",
					resp.GetResponseMeta().GetSessionID(), resp.GetEvent())
			case event.Type_TTSSentenceEnd:
				safeInfof("Receive TTS end (session_id=%s, event=%s)",
					resp.GetResponseMeta().GetSessionID(), resp.GetEvent())
			case event.Type_TTSResponse:
				if len(resp.GetData()) > 0 {
					safeInfof("Received audio data: %d bytes", len(resp.GetData()))

					// Append PCM data to WAV file
					audioMutex.Lock()
					if _, err := wavFile.Write(resp.GetData()); err != nil {
						safeErrorf("Failed to write to WAV file: %v", err)
					} else {
						totalAudioBytes += len(resp.GetData())
						safeV(3).Infof("Appended %d bytes to WAV file (total: %d)", len(resp.GetData()), totalAudioBytes)
					}
					if _, err := pcmFile.Write(resp.GetData()); err != nil {
						safeErrorf("Failed to write to PCM file: %v", err)
					}
					audioMutex.Unlock()

					// Send audio to playback queue (non-blocking)
					select {
					case audioPlayQueue <- resp.GetData():
						safeV(3).Infof("Enqueued %d bytes for playback (queue len: %d)", len(resp.GetData()), len(audioPlayQueue))
					default:
						safeWarningf("Audio playback queue full, dropping %d bytes", len(resp.GetData()))
					}
				} else {
					safeInfof("Received empty audio data")
				}
			default:
				safeInfof("Received event: %s, text: %s, audio: %d bytes", resp.GetEvent(), resp.GetText(), len(resp.GetData()))
			}
		}
	}()

	// Wait for a signal to gracefully shut down
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sig:
		fmt.Println("\nCaught signal, shutting down...")

		// Update WAV header with final size before exit
		audioMutex.Lock()
		if totalAudioBytes > 0 {
			wavFile.Seek(0, 0)
			if err := writeWAVHeader(wavFile, 16000, 1, 16, totalAudioBytes); err == nil {
				safeInfof("Final WAV saved on exit: %s (%d bytes)", wavFileName, totalAudioBytes)
			}
		}
		audioMutex.Unlock()

		cancel()
	case <-ctx.Done():
	}

	// Send FinishSession event
	if err := sendV4Request(conn, &ast.TranslateRequest{
		RequestMeta: &rpcmeta.RequestMeta{SessionID: sessionId},
		Event:       event.Type_FinishSession,
	}); err != nil {
		safeErrorf("Finish session: %v", err)
	}
	safeInfo("FinishSession request sent.")

	// Wait for all goroutines to finish
	wg.Wait()
	safeInfo("All goroutines finished. Exiting.")
}

// physicalMicToVirtualMic records from physical microphone, translates, and writes to virtual microphone ring buffer
func physicalMicToVirtualMic(conf Config) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	safeInfo("Initializing physical mic to virtual mic translation...")

	// Initialize shared memory connection for virtual microphone (input)
	if err := initializeSharedMemoryConnection(); err != nil {
		glog.Exitf("Initialize shared memory for virtual mic: %v", err)
	}
	defer cleanupSharedMemoryConnection()

	// Create FFmpeg microphone recorder for physical microphone
	micRecorder, err := NewMicrophoneRecorder()
	if err != nil {
		glog.Exitf("Create microphone recorder: %v", err)
	}
	defer micRecorder.Close()

	// --- Setup WebSocket Connection ---
	conn, err := dial(conf, uuid.New().String())
	if err != nil {
		glog.Exitf("Dial server: %v", err)
	}
	defer normalClose(conn)

	sessionId := uuid.New().String()
	translateRequest := &ast.TranslateRequest{
		RequestMeta: &rpcmeta.RequestMeta{
			SessionID: sessionId,
		},
		Event: event.Type_StartSession,
		User: &base.User{
			Uid: "mic_to_virtual_mic_client",
			Did: "mic_to_virtual_mic_client",
		},
		SourceAudio: &base.Audio{
			Format:  "pcm",
			Rate:    INPUT_SAMPLE_RATE,
			Bits:    16,
			Channel: INPUT_CHANNELS,
		},
		TargetAudio: &base.Audio{
			Format:  "pcm",
			Rate:    INPUT_SAMPLE_RATE,
			Bits:    16,
			Channel: 1,
		},
		Request: &ast.ReqParams{
			Mode:           "s2s",
			SourceLanguage: "zh",
			TargetLanguage: "en",
		},
	}

	if err := shakeHands(conn, translateRequest, new(ast.TranslateResponse)); err != nil {
		glog.Exitf("Start session: %v", err)
	}
	safeInfof("Session (ID=%s) started. Recording from physical mic...", sessionId)

	var wg sync.WaitGroup
	wg.Add(4) // recording, sending, receiving, and rate-controlled writing

	// Audio buffer channel to decouple reading from sending
	audioChan := make(chan []byte, 20)

	// Audio write queue for rate-controlled ring buffer writing
	// Small buffer (2-3) to minimize latency while preventing drops
	audioWriteQueue := make(chan []byte, 3)

	// Close WebSocket on cancellation to unblock receiveV4Message.
	go func() {
		<-ctx.Done()
		safeInfo("Context cancelled, closing WebSocket connection...")
		conn.Close()
	}()

	// --- Microphone Recording Goroutine ---
	go func() {
		defer wg.Done()
		safeInfo("Starting physical microphone recording...")
		micRecorder.StartRecording(ctx, audioChan)
	}()

	// --- Rate-Controlled Ring Buffer Writing Goroutine ---
	go func() {
		defer wg.Done()
		safeInfo("Starting rate-controlled ring buffer writer...")
		const writeInterval = 80 * time.Millisecond
		writeCfg := newVirtualMicWriteConfig(&gSharedMemory.RingBuffer, INPUT_SAMPLE_RATE, INPUT_SAMPLE_RATE, writeInterval)
		safeInfof("Virtual mic writer configured: source=%d Hz, output=%d Hz, chunk=%d bytes",
			INPUT_SAMPLE_RATE, writeCfg.outputRate, writeCfg.chunkBytes)

		// Buffer for accumulating audio data
		var audioBuffer []byte

		// Use ticker to write at fixed intervals (80ms)
		ticker := time.NewTicker(writeInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case audioData, ok := <-audioWriteQueue:
				if !ok {
					// Flush remaining buffer before exit
					if len(audioBuffer) > 0 {
						frameCount := uint32(len(audioBuffer) / FrameSize)
						ringBufferWriteFrames(&gSharedMemory.RingBuffer, audioBuffer, frameCount)
					}
					safeInfo("Audio write queue closed.")
					return
				}
				if writeCfg.refresh(&gSharedMemory.RingBuffer, INPUT_SAMPLE_RATE, writeInterval) {
					safeWarningf("Virtual microphone sample rate changed, reconfiguring writer to %d Hz", writeCfg.outputRate)
					// Pending buffer is encoded for the previous sample rate, drop it to avoid speed/pitch artifacts.
					audioBuffer = nil
				}
				outputData := writeCfg.transform(audioData)
				if len(outputData) == 0 {
					safeV(3).Info("Resampler buffering, no output yet")
					continue
				}
				// Accumulate incoming audio data
				audioBuffer = append(audioBuffer, outputData...)
				safeV(3).Infof("Accumulated %d bytes, buffer now %d bytes", len(outputData), len(audioBuffer))

			case <-ticker.C:
				if writeCfg.refresh(&gSharedMemory.RingBuffer, INPUT_SAMPLE_RATE, writeInterval) {
					safeWarningf("Virtual microphone sample rate changed, reconfiguring writer to %d Hz", writeCfg.outputRate)
					// Pending buffer is encoded for the previous sample rate, drop it to avoid speed/pitch artifacts.
					audioBuffer = nil
				}
				// Write one chunk (80ms) if buffer has enough data
				if len(audioBuffer) >= writeCfg.chunkBytes {
					chunk := audioBuffer[:writeCfg.chunkBytes]
					frameCount := uint32(len(chunk) / FrameSize)
					ringBufferWriteFrames(&gSharedMemory.RingBuffer, chunk, frameCount)
					audioBuffer = audioBuffer[writeCfg.chunkBytes:]
					safeV(3).Infof("Wrote %d bytes, buffer remaining: %d bytes", len(chunk), len(audioBuffer))
				} else if len(audioBuffer) > 0 {
					// Write remaining small chunk
					frameCount := uint32(len(audioBuffer) / FrameSize)
					ringBufferWriteFrames(&gSharedMemory.RingBuffer, audioBuffer, frameCount)
					safeV(3).Infof("Wrote final %d bytes", len(audioBuffer))
					audioBuffer = nil
				}
			}
		}
	}()

	// --- Audio Sending Goroutine ---
	go func() {
		defer wg.Done()
		safeInfo("Starting audio sender to translation service...")

		for {
			select {
			case <-ctx.Done():
				safeInfo("Audio sender stopped by context.")
				return
			case audioData, ok := <-audioChan:
				if !ok {
					safeInfo("Audio channel closed.")
					return
				}

				if err := sendV4Request(conn, &ast.TranslateRequest{
					RequestMeta: &rpcmeta.RequestMeta{SessionID: sessionId},
					Event:       event.Type_TaskRequest,
					SourceAudio: &base.Audio{BinaryData: audioData},
				}); err != nil {
					safeErrorf("Send audio chunk: %v", err)
					cancel()
					return
				} else {
					safeV(3).Infof("Sent %d bytes to translation service", len(audioData))
				}
			}
		}
	}()

	// --- Server Receiving and Write to Virtual Mic Ring Buffer Goroutine ---
	go func() {
		defer wg.Done()
		safeInfo("Starting server message listener, writing to virtual mic ring buffer...")

		for {
			select {
			case <-ctx.Done():
				safeInfo("Server listener stopped by context.")
				return
			default:
			}

			// Set a longer timeout for message receiving
			// conn.SetReadDeadline(time.Now().Add(5 * time.Second))

			resp := new(ast.TranslateResponse)
			if err := receiveV4Message(conn, resp); err != nil {
				// Check if it's a timeout error
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				if err == io.EOF || strings.Contains(err.Error(), "closed") {
					safeInfo("Connection closed by server.")
				} else {
					safeErrorf("Receive message error: %v", err)
				}
				cancel()
				return
			}

			switch resp.GetEvent() {
			case event.Type_SessionFailed, event.Type_SessionCanceled, event.Type_SessionFinished:
				safeInfof("Session ended (event: %s, session_id: %s, message: %s)",
					resp.GetEvent(), resp.GetResponseMeta().GetSessionID(), resp.GetResponseMeta().GetMessage())
				cancel()
				return
			case event.Type_TTSResponse:
				if len(resp.GetData()) > 0 {
					safeInfof("Received translated audio: %d bytes, sending to rate-controlled writer", len(resp.GetData()))

					// Send to rate-controlled write queue instead of writing directly
					select {
					case audioWriteQueue <- resp.GetData():
						safeV(3).Infof("Enqueued %d bytes for rate-controlled writing", len(resp.GetData()))
					case <-ctx.Done():
						return
					default:
						safeWarningf("Audio write queue full, dropping %d bytes", len(resp.GetData()))
					}
				}
			default:
				if resp.GetText() != "" {
					safeInfof("Received event: %s, text: %s", resp.GetEvent(), resp.GetText())
				}
			}
		}
	}()

	// Wait for a signal to gracefully shut down
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sig:
		fmt.Println("\nCaught signal, shutting down physical mic to virtual mic...")
		cancel()
	case <-ctx.Done():
	}

	// Send FinishSession event
	if err := sendV4Request(conn, &ast.TranslateRequest{
		RequestMeta: &rpcmeta.RequestMeta{SessionID: sessionId},
		Event:       event.Type_FinishSession,
	}); err != nil {
		safeErrorf("Finish session: %v", err)
	}
	safeInfo("FinishSession request sent.")

	// Wait for all goroutines to finish
	wg.Wait()
	safeInfo("Physical mic to virtual mic translation finished.")
}

// virtualSpeakerToPhysicalSpeaker reads from virtual speaker ring buffer, translates, and plays to physical speaker via mpv
func virtualSpeakerToPhysicalSpeaker(conf Config) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	safeInfo("Initializing virtual speaker to physical speaker translation...")

	// Initialize shared memory connection for virtual speaker (output)
	if err := initializeOutputSharedMemoryConnection(); err != nil {
		glog.Exitf("Initialize shared memory for virtual speaker: %v", err)
	}
	defer cleanupOutputSharedMemoryConnection()

	// Create mpv player for physical speaker playback
	// Note: You may need to modify NewPCMPlayer to accept audio device parameter
	audioPlayer, err := NewPCMPlayer(16000, 1, 16)
	if err != nil {
		glog.Exitf("Create PCM player: %v", err)
	}
	defer audioPlayer.Close()

	// --- Setup WebSocket Connection ---
	conn, err := dial(conf, uuid.New().String())
	if err != nil {
		glog.Exitf("Dial server: %v", err)
	}
	defer normalClose(conn)

	sessionId := uuid.New().String()
	translateRequest := &ast.TranslateRequest{
		RequestMeta: &rpcmeta.RequestMeta{
			SessionID: sessionId,
		},
		Event: event.Type_StartSession,
		User: &base.User{
			Uid: "virtual_speaker_to_physical_client",
			Did: "virtual_speaker_to_physical_client",
		},
		SourceAudio: &base.Audio{
			Format:  "pcm",
			Rate:    16000,
			Bits:    16,
			Channel: 1,
		},
		TargetAudio: &base.Audio{
			Format:  "pcm",
			Rate:    16000,
			Bits:    16,
			Channel: 1,
		},
		Request: &ast.ReqParams{
			Mode:           "s2s",
			SourceLanguage: "en",
			TargetLanguage: "zh",
		},
	}

	if err := shakeHands(conn, translateRequest, new(ast.TranslateResponse)); err != nil {
		glog.Exitf("Start session: %v", err)
	}
	safeInfof("Session (ID=%s) started. Reading from virtual speaker...", sessionId)

	var wg sync.WaitGroup
	wg.Add(3) // reading from ring buffer, sending, receiving

	// Audio buffer channel for sending
	audioChan := make(chan []byte, 20)

	// Close WebSocket on cancellation to unblock receiveV4Message.
	go func() {
		<-ctx.Done()
		safeInfo("Context cancelled, closing WebSocket connection...")
		conn.Close()
	}()

	// --- Ring Buffer Reading Goroutine ---
	go func() {
		defer wg.Done()
		defer close(audioChan)
		safeInfo("Starting virtual speaker ring buffer reader...")

		const readInterval = 80 * time.Millisecond
		readCfg := newVirtualSpeakerReadConfig(&gSharedMemoryOutput.RingBuffer, INPUT_SAMPLE_RATE, INPUT_SAMPLE_RATE, readInterval)
		safeInfof("Virtual speaker reader configured: source=%d Hz, output=%d Hz, frames=%d",
			readCfg.sourceRate, readCfg.outputRate, readCfg.framesPerBuffer)
		buffer := make([]byte, int(readCfg.framesPerBuffer)*FrameSize)
		ticker := time.NewTicker(readInterval) // Check every 80ms
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				safeInfo("Ring buffer reader stopped by context.")
				return
			case <-ticker.C:
				if readCfg.refresh(&gSharedMemoryOutput.RingBuffer, INPUT_SAMPLE_RATE, readInterval) {
					safeWarningf("Virtual speaker sample rate changed, reconfiguring reader to %d Hz", readCfg.sourceRate)
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

					safeV(3).Infof("Read %d frames (%d bytes) from virtual speaker ring buffer", framesRead, bytesRead)

					if len(audioChan) == cap(audioChan) {
						safeV(2).Info("Audio send channel full, waiting for sender")
					}
					if !sendAudioWithBackpressure(ctx, audioChan, audioData) {
						return
					}
				}
			}
		}
	}()

	// --- Audio Sending Goroutine ---
	go func() {
		defer wg.Done()
		safeInfo("Starting audio sender to translation service...")

		for {
			select {
			case <-ctx.Done():
				safeInfo("Audio sender stopped by context.")
				return
			case audioData, ok := <-audioChan:
				if !ok {
					safeInfo("Audio channel closed.")
					return
				}

				if err := sendV4Request(conn, &ast.TranslateRequest{
					RequestMeta: &rpcmeta.RequestMeta{SessionID: sessionId},
					Event:       event.Type_TaskRequest,
					SourceAudio: &base.Audio{BinaryData: audioData},
				}); err != nil {
					safeErrorf("Send audio chunk: %v", err)
					cancel()
					return
				} else {
					safeV(3).Infof("Sent %d bytes to translation service", len(audioData))
				}
			}
		}
	}()

	// --- Server Receiving and Play to Physical Speaker Goroutine ---
	go func() {
		defer wg.Done()
		safeInfo("Starting server message listener, playing to physical speaker...")

		for {
			select {
			case <-ctx.Done():
				safeInfo("Server listener stopped by context.")
				return
			default:
			}

			// Set a longer timeout for message receiving
			// conn.SetReadDeadline(time.Now().Add(5 * time.Second))

			resp := new(ast.TranslateResponse)
			if err := receiveV4Message(conn, resp); err != nil {
				// Check if it's a timeout error
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				if err == io.EOF || strings.Contains(err.Error(), "closed") {
					safeInfo("Connection closed by server.")
				} else {
					safeErrorf("Receive message error: %v", err)
				}
				cancel()
				return
			}

			switch resp.GetEvent() {
			case event.Type_SessionFailed, event.Type_SessionCanceled, event.Type_SessionFinished:
				safeInfof("Session ended (event: %s, session_id: %s, message: %s)",
					resp.GetEvent(), resp.GetResponseMeta().GetSessionID(), resp.GetResponseMeta().GetMessage())
				cancel()
				return
			case event.Type_TTSResponse:
				if len(resp.GetData()) > 0 {
					safeInfof("Received translated audio: %d bytes, playing to physical speaker", len(resp.GetData()))

					// Play translated PCM data to physical speaker via mpv
					if err := audioPlayer.WriteAudio(resp.GetData()); err != nil {
						safeErrorf("Failed to play audio to physical speaker: %v", err)
					} else {
						safeInfof("Successfully played %d bytes to physical speaker", len(resp.GetData()))
					}
				}
			default:
				if resp.GetText() != "" {
					safeInfof("Received event: %s, text: %s", resp.GetEvent(), resp.GetText())
				}
			}
		}
	}()

	// Wait for a signal to gracefully shut down
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sig:
		fmt.Println("\nCaught signal, shutting down virtual speaker to physical speaker...")
		cancel()
	case <-ctx.Done():
	}

	// Send FinishSession event
	if err := sendV4Request(conn, &ast.TranslateRequest{
		RequestMeta: &rpcmeta.RequestMeta{SessionID: sessionId},
		Event:       event.Type_FinishSession,
	}); err != nil {
		safeErrorf("Finish session: %v", err)
	}
	safeInfo("FinishSession request sent.")

	// Wait for all goroutines to finish
	wg.Wait()
	safeInfo("Virtual speaker to physical speaker translation finished.")
}

// bidirectionalTranslation runs both translation modes simultaneously
func bidirectionalTranslation(conf Config) {
	safeInfo("Starting bidirectional translation (mic2vmic + vspeaker2pspeaker)...")

	var wg sync.WaitGroup
	wg.Add(2)

	// Mode 1: Physical Mic -> Virtual Mic
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				safeErrorf("Mode 1 panicked: %v", r)
			}
		}()
		physicalMicToVirtualMic(conf)
	}()

	// Mode 2: Virtual Speaker -> Physical Speaker
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				safeErrorf("Mode 2 panicked: %v", r)
			}
		}()
		virtualSpeakerToPhysicalSpeaker(conf)
	}()

	// Wait for signal
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	fmt.Println("\nReceived signal, both modes will shut down...")
	safeInfo("Waiting for both modes to finish...")
	wg.Wait()
	safeInfo("Bidirectional translation finished.")
}

// streamSTSV4WithDevices handles speech-to-speech translation with specific devices
func streamSTSV4WithDevices(conf Config, micDevice, speakerDevice string, ctx context.Context) {
	// Keep default behavior as zh->en
	streamSTSV4WithDevicesAndLanguages(conf, micDevice, speakerDevice, "zh", "en", ctx, nil, nil)
}

// streamSTSV4WithDevicesAndLanguages handles speech-to-speech translation with specific devices and languages
func streamSTSV4WithDevicesAndLanguages(conf Config, micDevice, speakerDevice, sourceLang, targetLang string, ctx context.Context, textCallback TextCallback, errorCallback ErrorCallback) {
	safeInfo("Initializing mpv for PCM audio playback...")

	// Create mpv player for PCM streaming playback with specific device
	audioPlayer, err := NewPCMPlayerWithDevice(16000, 1, 16, speakerDevice)
	if err != nil {
		safeErrorf("Create PCM player: %v", err)
		return
	}
	defer audioPlayer.Close()

	// Create FFmpeg microphone recorder with specific device
	micRecorder, err := NewMicrophoneRecorderWithDevice(micDevice)
	if err != nil {
		safeErrorf("Create microphone recorder: %v", err)
		return
	}
	defer micRecorder.Close()

	// --- Setup WebSocket Connection ---
	conn, err := dial(conf, uuid.New().String())
	if err != nil {
		safeErrorf("Dial server: %v", err)
		if errorCallback != nil {
			errorCallback(fmt.Errorf("连接服务器失败: %v", err))
		}
		return
	}
	defer normalClose(conn)

	sessionId := uuid.New().String()
	translateRequest := &ast.TranslateRequest{
		RequestMeta: &rpcmeta.RequestMeta{
			SessionID: sessionId,
		},
		Event: event.Type_StartSession,
		User: &base.User{
			Uid: "sts_go_client",
			Did: "sts_go_client",
		},
		SourceAudio: &base.Audio{
			Format:  "pcm",
			Rate:    INPUT_SAMPLE_RATE,
			Bits:    16,
			Channel: INPUT_CHANNELS,
		},
		TargetAudio: &base.Audio{
			Format: "pcm",
			Rate:   16000,
			Bits:   16,
		},
		Request: &ast.ReqParams{
			Mode:           "s2s",
			SourceLanguage: sourceLang,
			TargetLanguage: targetLang,
		},
	}

	if err := shakeHands(conn, translateRequest, new(ast.TranslateResponse)); err != nil {
		safeErrorf("Start session: %v", err)
		return
	}
	safeInfof("Session (ID=%s) started. Please start speaking.", sessionId)

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	var wg sync.WaitGroup
	wg.Add(2)

	// Audio buffer channel to decouple reading from sending
	audioChan := make(chan []byte, 20)

	// --- Context Cancellation Monitor ---
	// Close WebSocket when context is cancelled to unblock receiveV4Message
	go func() {
		<-runCtx.Done()
		safeInfo("Context cancelled, closing WebSocket connection...")
		conn.Close()
	}()

	// --- Microphone Recording Goroutine ---
	go func() {
		defer wg.Done()
		safeInfo("Starting FFmpeg microphone recording...")
		micRecorder.StartRecording(runCtx, audioChan)
	}()

	// --- Audio Sending and Receiving Goroutine ---
	go func() {
		defer wg.Done()

		// Start sender goroutine
		var senderWg sync.WaitGroup
		senderWg.Add(1)
		go func() {
			defer senderWg.Done()
			runV4AudioSender(runCtx, runCancel, audioChan, func(audioData []byte) error {
				return sendV4Request(conn, &ast.TranslateRequest{
					RequestMeta: &rpcmeta.RequestMeta{SessionID: sessionId},
					Event:       event.Type_TaskRequest,
					SourceAudio: &base.Audio{BinaryData: audioData},
				})
			}, errorCallback)
		}()

		// Receive messages
		for {
			select {
			case <-runCtx.Done():
				senderWg.Wait()
				return
			default:
			}

			resp := new(ast.TranslateResponse)
			if err := receiveV4MessageWithCancel(runCtx, conn, resp); err != nil {
				if err == context.Canceled {
					senderWg.Wait()
					return
				}
				select {
				case <-runCtx.Done():
					senderWg.Wait()
					return
				default:
				}
				if errorCallback != nil {
					errorCallback(err)
				}
				if err == io.EOF || strings.Contains(err.Error(), "closed") {
					safeInfo("Connection closed by server.")
				} else {
					safeErrorf("Receive message error: %v", err)
				}
				runCancel()
				senderWg.Wait()
				return
			}

			if isSessionTerminalEvent(resp.GetEvent()) {
				safeInfof("Session ended (event: %s)", resp.GetEvent())
				if errorCallback != nil {
					switch resp.GetEvent() {
					case event.Type_SessionFailed:
						errorCallback(fmt.Errorf("会话失败: %s", resp.GetResponseMeta().GetMessage()))
					case event.Type_SessionCanceled:
						errorCallback(fmt.Errorf("会话被取消: %s", resp.GetResponseMeta().GetMessage()))
					}
				}
				runCancel()
				senderWg.Wait()
				return
			}

			switch resp.GetEvent() {
			case event.Type_SourceSubtitleResponse:
				if textCallback != nil && resp.GetText() != "" {
					textCallback(resp.GetText(), "")
				}
			case event.Type_TranslationSubtitleResponse:
				if textCallback != nil && resp.GetText() != "" {
					textCallback("", resp.GetText())
				}
			case event.Type_TTSResponse:
				if len(resp.GetData()) > 0 {
					if err := audioPlayer.WriteAudio(resp.GetData()); err != nil {
						safeErrorf("Failed to play audio: %v", err)
						if errorCallback != nil {
							errorCallback(fmt.Errorf("播放音频失败: %v", err))
						}
						runCancel()
						senderWg.Wait()
						return
					}
				}
			}
		}
	}()

	wg.Wait()

	// Send FinishSession event
	if err := sendV4Request(conn, &ast.TranslateRequest{
		RequestMeta: &rpcmeta.RequestMeta{SessionID: sessionId},
		Event:       event.Type_FinishSession,
	}); err != nil {
		if runCtx.Err() != nil {
			safeV(2).Infof("Skip FinishSession error after run context canceled: %v", err)
		} else {
			safeWarningf("Finish session: %v", err)
			if errorCallback != nil {
				errorCallback(fmt.Errorf("结束会话失败: %v", err))
			}
		}
	}
	safeInfo("streamSTSV4WithDevices finished.")
}

// physicalMicToVirtualMicWithDevices records from physical microphone with specific device
func physicalMicToVirtualMicWithDevices(conf Config, micDevice string, ctx context.Context) {
	physicalMicToVirtualMicWithDevicesAndCallback(conf, micDevice, ctx, nil, nil)
}

// physicalMicToVirtualMicWithDevicesAndCallback records from physical microphone with callback support
func physicalMicToVirtualMicWithDevicesAndCallback(conf Config, micDevice string, ctx context.Context, textCallback TextCallback, errorCallback ErrorCallback) {
	safeInfo("Initializing physical mic to virtual mic translation...")

	// Initialize shared memory connection for virtual microphone (input)
	if err := initializeSharedMemoryConnection(); err != nil {
		safeErrorf("Initialize shared memory for virtual mic: %v", err)
		return
	}
	defer cleanupSharedMemoryConnection()

	// Create FFmpeg microphone recorder for physical microphone
	micRecorder, err := NewMicrophoneRecorderWithDevice(micDevice)
	if err != nil {
		safeErrorf("Create microphone recorder: %v", err)
		return
	}
	defer micRecorder.Close()

	// --- Setup WebSocket Connection ---
	conn, err := dial(conf, uuid.New().String())
	if err != nil {
		safeErrorf("Dial server: %v", err)
		if errorCallback != nil {
			errorCallback(fmt.Errorf("连接服务器失败: %v", err))
		}
		return
	}
	defer normalClose(conn)

	sessionId := uuid.New().String()
	translateRequest := &ast.TranslateRequest{
		RequestMeta: &rpcmeta.RequestMeta{
			SessionID: sessionId,
		},
		Event: event.Type_StartSession,
		User: &base.User{
			Uid: "mic_to_virtual_mic_client",
			Did: "mic_to_virtual_mic_client",
		},
		SourceAudio: &base.Audio{
			Format:  "pcm",
			Rate:    INPUT_SAMPLE_RATE,
			Bits:    16,
			Channel: INPUT_CHANNELS,
		},
		TargetAudio: &base.Audio{
			Format:  "pcm",
			Rate:    INPUT_SAMPLE_RATE,
			Bits:    16,
			Channel: 1,
		},
		Request: &ast.ReqParams{
			Mode:           "s2s",
			SourceLanguage: "zh",
			TargetLanguage: "en",
		},
	}

	if err := shakeHands(conn, translateRequest, new(ast.TranslateResponse)); err != nil {
		safeErrorf("Start session: %v", err)
		return
	}
	safeInfof("Session (ID=%s) started. Recording from physical mic...", sessionId)

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	var wg sync.WaitGroup
	wg.Add(3) // recording, sending+receiving, and rate-controlled writing

	// Audio buffer channel for microphone input
	audioChan := make(chan []byte, 20)

	// Audio write queue for rate-controlled ring buffer writing
	// Small buffer (2-3) to minimize latency while preventing drops
	audioWriteQueue := make(chan []byte, 3)

	// --- Context Cancellation Monitor ---
	// Close WebSocket when context is cancelled to unblock receiveV4Message
	go func() {
		<-runCtx.Done()
		safeInfo("Context cancelled, closing WebSocket connection...")
		conn.Close()
	}()

	// --- Microphone Recording Goroutine ---
	go func() {
		defer wg.Done()
		micRecorder.StartRecording(runCtx, audioChan)
	}()

	// --- Rate-Controlled Ring Buffer Writing Goroutine ---
	go func() {
		defer wg.Done()
		safeInfo("Starting rate-controlled ring buffer writer...")
		const writeInterval = 80 * time.Millisecond
		writeCfg := newVirtualMicWriteConfig(&gSharedMemory.RingBuffer, INPUT_SAMPLE_RATE, INPUT_SAMPLE_RATE, writeInterval)
		safeInfof("Virtual mic writer configured: source=%d Hz, output=%d Hz, chunk=%d bytes",
			INPUT_SAMPLE_RATE, writeCfg.outputRate, writeCfg.chunkBytes)

		// Buffer for accumulating audio data
		var audioBuffer []byte

		flushBuffer := func(reason string) {
			if len(audioBuffer) == 0 {
				return
			}
			frameCount := uint32(len(audioBuffer) / FrameSize)
			if frameCount > 0 {
				bytesToWrite := int(frameCount) * int(FrameSize)
				ringBufferWriteFrames(&gSharedMemory.RingBuffer, audioBuffer[:bytesToWrite], frameCount)
				safeV(3).Infof("Flushed %d bytes from audio buffer (%s)", bytesToWrite, reason)
			}
			audioBuffer = nil
		}

		drainQueuedAudio := func() {
			for {
				select {
				case audioData, ok := <-audioWriteQueue:
					if !ok {
						return
					}
					audioBuffer = append(audioBuffer, audioData...)
				default:
					return
				}
			}
		}

		// Use ticker to write at fixed intervals (80ms)
		ticker := time.NewTicker(writeInterval)
		defer ticker.Stop()

		for {
			select {
			case <-runCtx.Done():
				drainQueuedAudio()
				flushBuffer("context canceled")
				return
			case audioData, ok := <-audioWriteQueue:
				if !ok {
					flushBuffer("queue closed")
					safeInfo("Audio write queue closed.")
					return
				}
				if writeCfg.refresh(&gSharedMemory.RingBuffer, INPUT_SAMPLE_RATE, writeInterval) {
					safeWarningf("Virtual microphone sample rate changed, reconfiguring writer to %d Hz", writeCfg.outputRate)
					// Pending buffer is encoded for the previous sample rate, drop it to avoid speed/pitch artifacts.
					audioBuffer = nil
				}
				outputData := writeCfg.transform(audioData)
				if len(outputData) == 0 {
					safeV(3).Info("Resampler buffering, no output yet")
					continue
				}
				// Accumulate incoming audio data
				audioBuffer = append(audioBuffer, outputData...)
				safeV(3).Infof("Accumulated %d bytes, buffer now %d bytes", len(outputData), len(audioBuffer))

			case <-ticker.C:
				if writeCfg.refresh(&gSharedMemory.RingBuffer, INPUT_SAMPLE_RATE, writeInterval) {
					safeWarningf("Virtual microphone sample rate changed, reconfiguring writer to %d Hz", writeCfg.outputRate)
					// Pending buffer is encoded for the previous sample rate, drop it to avoid speed/pitch artifacts.
					audioBuffer = nil
				}
				// Write one chunk (80ms) if buffer has enough data
				if len(audioBuffer) >= writeCfg.chunkBytes {
					chunk := audioBuffer[:writeCfg.chunkBytes]
					frameCount := uint32(len(chunk) / FrameSize)
					ringBufferWriteFrames(&gSharedMemory.RingBuffer, chunk, frameCount)
					audioBuffer = audioBuffer[writeCfg.chunkBytes:]
					safeV(3).Infof("Wrote %d bytes, buffer remaining: %d bytes", len(chunk), len(audioBuffer))
				} else if len(audioBuffer) > 0 {
					// Write remaining small chunk
					frameCount := uint32(len(audioBuffer) / FrameSize)
					ringBufferWriteFrames(&gSharedMemory.RingBuffer, audioBuffer, frameCount)
					safeV(3).Infof("Wrote final %d bytes", len(audioBuffer))
					audioBuffer = nil
				}
			}
		}
	}()

	// --- Audio Sending and Receiving Goroutine ---
	go func() {
		defer wg.Done()

		// Sender
		var senderWg sync.WaitGroup
		senderWg.Add(1)
		go func() {
			defer senderWg.Done()
			runV4AudioSender(runCtx, runCancel, audioChan, func(audioData []byte) error {
				return sendV4Request(conn, &ast.TranslateRequest{
					RequestMeta: &rpcmeta.RequestMeta{SessionID: sessionId},
					Event:       event.Type_TaskRequest,
					SourceAudio: &base.Audio{BinaryData: audioData},
				})
			}, errorCallback)
		}()

		// Receiver
		for {
			select {
			case <-runCtx.Done():
				senderWg.Wait()
				return
			default:
			}

			resp := new(ast.TranslateResponse)
			if err := receiveV4MessageWithCancel(runCtx, conn, resp); err != nil {
				if err == context.Canceled {
					senderWg.Wait()
					return
				}
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				if err == io.EOF || strings.Contains(err.Error(), "closed") {
					safeErrorf("WebSocket连接已关闭: %v", err)
					if errorCallback != nil {
						errorCallback(fmt.Errorf("WebSocket连接已关闭: %v", err))
					}
				} else {
					safeErrorf("接收消息错误: %v", err)
					if errorCallback != nil {
						errorCallback(fmt.Errorf("接收消息错误: %v", err))
					}
				}
				runCancel()
				senderWg.Wait()
				return
			}

			if isSessionTerminalEvent(resp.GetEvent()) {
				runCancel()
				senderWg.Wait()
				return
			}

			switch resp.GetEvent() {
			case event.Type_SourceSubtitleResponse:
				// Handle source text if callback is provided
				if textCallback != nil && resp.GetText() != "" {
					safeInfof("Source text: %s", resp.GetText())
					textCallback(resp.GetText(), "")
				}
			case event.Type_TranslationSubtitleResponse:
				// Handle translation text if callback is provided
				if textCallback != nil && resp.GetText() != "" {
					safeInfof("Translation text: %s", resp.GetText())
					textCallback("", resp.GetText())
				}
			case event.Type_TTSResponse:
				if len(resp.GetData()) > 0 {
					// Send to rate-controlled write queue instead of writing directly
					select {
					case audioWriteQueue <- resp.GetData():
						safeV(3).Infof("Enqueued %d bytes for rate-controlled writing", len(resp.GetData()))
					case <-runCtx.Done():
						senderWg.Wait()
						return
					default:
						safeWarningf("Audio write queue full, dropping %d bytes", len(resp.GetData()))
					}
				}
			}
		}
	}()

	wg.Wait()

	if err := sendV4Request(conn, &ast.TranslateRequest{
		RequestMeta: &rpcmeta.RequestMeta{SessionID: sessionId},
		Event:       event.Type_FinishSession,
	}); err != nil {
		if runCtx.Err() != nil {
			safeV(2).Infof("Skip FinishSession error after run context canceled: %v", err)
		} else {
			safeWarningf("Finish session: %v", err)
			if errorCallback != nil {
				errorCallback(fmt.Errorf("结束会话失败: %v", err))
			}
		}
	}
	safeInfo("physicalMicToVirtualMicWithDevices finished.")
}

// virtualSpeakerToPhysicalSpeakerWithDevices reads from virtual speaker with specific device
func virtualSpeakerToPhysicalSpeakerWithDevices(conf Config, speakerDevice string, ctx context.Context) {
	virtualSpeakerToPhysicalSpeakerWithDevicesAndCallback(conf, speakerDevice, ctx, nil, nil)
}

// virtualSpeakerToPhysicalSpeakerWithDevicesAndCallback reads from virtual speaker with callback support
func virtualSpeakerToPhysicalSpeakerWithDevicesAndCallback(conf Config, speakerDevice string, ctx context.Context, textCallback TextCallback, errorCallback ErrorCallback) {
	safeInfo("Initializing virtual speaker to physical speaker translation...")

	// Initialize shared memory connection for virtual speaker (output)
	if err := initializeOutputSharedMemoryConnection(); err != nil {
		safeErrorf("Initialize shared memory for virtual speaker: %v", err)
		return
	}
	defer cleanupOutputSharedMemoryConnection()

	// Create mpv player for physical speaker playback
	audioPlayer, err := NewPCMPlayerWithDevice(16000, 1, 16, speakerDevice)
	if err != nil {
		safeErrorf("Create PCM player: %v", err)
		return
	}
	defer audioPlayer.Close()

	// --- Setup WebSocket Connection ---
	conn, err := dial(conf, uuid.New().String())
	if err != nil {
		safeErrorf("Dial server: %v", err)
		return
	}
	defer normalClose(conn)

	sessionId := uuid.New().String()
	translateRequest := &ast.TranslateRequest{
		RequestMeta: &rpcmeta.RequestMeta{
			SessionID: sessionId,
		},
		Event: event.Type_StartSession,
		User: &base.User{
			Uid: "virtual_speaker_to_physical_client",
			Did: "virtual_speaker_to_physical_client",
		},
		SourceAudio: &base.Audio{
			Format:  "pcm",
			Rate:    16000,
			Bits:    16,
			Channel: 1,
		},
		TargetAudio: &base.Audio{
			Format:  "pcm",
			Rate:    16000,
			Bits:    16,
			Channel: 1,
		},
		Request: &ast.ReqParams{
			Mode:           "s2s",
			SourceLanguage: "en",
			TargetLanguage: "zh",
		},
	}

	if err := shakeHands(conn, translateRequest, new(ast.TranslateResponse)); err != nil {
		safeErrorf("Start session: %v", err)
		return
	}
	safeInfof("Session (ID=%s) started. Reading from virtual speaker...", sessionId)

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	var wg sync.WaitGroup
	wg.Add(2)

	// Audio buffer channel
	audioChan := make(chan []byte, 20)

	// --- Context Cancellation Monitor ---
	// Close WebSocket when context is cancelled to unblock receiveV4Message
	go func() {
		<-runCtx.Done()
		safeInfo("Context cancelled, closing WebSocket connection...")
		conn.Close()
	}()

	// --- Ring Buffer Reading Goroutine ---
	go func() {
		defer wg.Done()
		defer close(audioChan)

		const readInterval = 80 * time.Millisecond
		readCfg := newVirtualSpeakerReadConfig(&gSharedMemoryOutput.RingBuffer, INPUT_SAMPLE_RATE, INPUT_SAMPLE_RATE, readInterval)
		safeInfof("Virtual speaker reader configured: source=%d Hz, output=%d Hz, frames=%d",
			readCfg.sourceRate, readCfg.outputRate, readCfg.framesPerBuffer)
		buffer := make([]byte, int(readCfg.framesPerBuffer)*FrameSize)
		ticker := time.NewTicker(readInterval) // Check every 80ms
		defer ticker.Stop()

		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if readCfg.refresh(&gSharedMemoryOutput.RingBuffer, INPUT_SAMPLE_RATE, readInterval) {
					safeWarningf("Virtual speaker sample rate changed, reconfiguring reader to %d Hz", readCfg.sourceRate)
					buffer = make([]byte, int(readCfg.framesPerBuffer)*FrameSize)
				}
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

	// --- Audio Sending and Receiving Goroutine ---
	go func() {
		defer wg.Done()

		// Sender
		var senderWg sync.WaitGroup
		senderWg.Add(1)
		go func() {
			defer senderWg.Done()
			runV4AudioSender(runCtx, runCancel, audioChan, func(audioData []byte) error {
				return sendV4Request(conn, &ast.TranslateRequest{
					RequestMeta: &rpcmeta.RequestMeta{SessionID: sessionId},
					Event:       event.Type_TaskRequest,
					SourceAudio: &base.Audio{BinaryData: audioData},
				})
			}, errorCallback)
		}()

		// Receiver
		for {
			select {
			case <-runCtx.Done():
				senderWg.Wait()
				return
			default:
			}

			resp := new(ast.TranslateResponse)
			if err := receiveV4MessageWithCancel(runCtx, conn, resp); err != nil {
				if err == context.Canceled {
					senderWg.Wait()
					return
				}
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				if err == io.EOF || strings.Contains(err.Error(), "closed") {
					safeErrorf("WebSocket连接已关闭: %v", err)
					if errorCallback != nil {
						errorCallback(fmt.Errorf("WebSocket连接已关闭: %v", err))
					}
				} else {
					safeErrorf("接收消息错误: %v", err)
					if errorCallback != nil {
						errorCallback(fmt.Errorf("接收消息错误: %v", err))
					}
				}
				runCancel()
				senderWg.Wait()
				return
			}

			if isSessionTerminalEvent(resp.GetEvent()) {
				runCancel()
				senderWg.Wait()
				return
			}

			switch resp.GetEvent() {
			case event.Type_SourceSubtitleResponse:
				// Handle source text if callback is provided
				if textCallback != nil && resp.GetText() != "" {
					safeInfof("Source text: %s", resp.GetText())
					textCallback(resp.GetText(), "")
				}
			case event.Type_TranslationSubtitleResponse:
				// Handle translation text if callback is provided
				if textCallback != nil && resp.GetText() != "" {
					safeInfof("Translation text: %s", resp.GetText())
					textCallback("", resp.GetText())
				}
			case event.Type_TTSResponse:
				if len(resp.GetData()) > 0 {
					if err := audioPlayer.WriteAudio(resp.GetData()); err != nil {
						safeErrorf("Failed to play audio: %v", err)
					}
				}
			}
		}
	}()

	wg.Wait()

	if err := sendV4Request(conn, &ast.TranslateRequest{
		RequestMeta: &rpcmeta.RequestMeta{SessionID: sessionId},
		Event:       event.Type_FinishSession,
	}); err != nil {
		if runCtx.Err() != nil {
			safeV(2).Infof("Skip FinishSession error after run context canceled: %v", err)
		} else {
			safeWarningf("Finish session: %v", err)
			if errorCallback != nil {
				errorCallback(fmt.Errorf("结束会话失败: %v", err))
			}
		}
	}
	safeInfo("virtualSpeakerToPhysicalSpeakerWithDevices finished.")
}
