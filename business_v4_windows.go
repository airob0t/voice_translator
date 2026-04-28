//go:build windows
// +build windows

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
	}
	safeInfof("Session (ID=%s) started. Please start speaking.", sessionId)

	var wg sync.WaitGroup
	wg.Add(4) // Now we have 4 goroutines: recording, sending, receiving, and playing

	// Audio buffer channel to decouple reading from sending
	audioChan := make(chan []byte, 20) // Increased buffer to handle more audio chunks

	// Audio playback queue - balanced buffer to prevent drops while keeping latency reasonable
	audioPlayQueue := make(chan []byte, 50) // ~800ms buffer to handle bursts
	var closePlayQueueOnce sync.Once
	closePlayQueue := func() {
		closePlayQueueOnce.Do(func() {
			close(audioPlayQueue)
		})
	}

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

	// Audio drop statistics
	var droppedPackets int64
	var droppedBytes int64
	var dropMutex sync.Mutex

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
			// Play audio immediately from queue
			if err := audioPlayer.WriteAudio(audioData); err != nil {
				safeErrorf("Failed to play audio: %v", err)
				cancel()
				return
			} else {
				safeV(3).Infof("Successfully played %d bytes from queue", len(audioData))
			}
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
						dropMutex.Lock()
						droppedPackets++
						droppedBytes += int64(len(resp.GetData()))
						safeErrorf("!!! AUDIO DROP !!! Queue full, dropped packet #%d (%d bytes, total dropped: %d bytes)", droppedPackets, len(resp.GetData()), droppedBytes)
						dropMutex.Unlock()
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

	// Wait for all goroutines to finish
	wg.Wait()

	// Send FinishSession event after sender goroutine exits to avoid concurrent websocket writes.
	if err := sendV4Request(conn, &ast.TranslateRequest{
		RequestMeta: &rpcmeta.RequestMeta{SessionID: sessionId},
		Event:       event.Type_FinishSession,
	}); err != nil {
		safeErrorf("Finish session: %v", err)
	}
	safeInfo("FinishSession request sent.")

	// Report audio drop statistics
	dropMutex.Lock()
	if droppedPackets > 0 {
		safeErrorf("=== AUDIO DROP SUMMARY === Dropped %d packets (%d bytes total) during session", droppedPackets, droppedBytes)
	} else {
		safeInfof("=== SESSION COMPLETE === No audio packets were dropped")
	}
	dropMutex.Unlock()

	safeInfo("All goroutines finished. Exiting.")
}

// physicalMicToVirtualMic records from physical microphone, translates, and plays to CABLE-A Input (virtual speaker device)
func physicalMicToVirtualMic(conf Config) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	safeInfo("Initializing physical mic to CABLE-A Input translation...")

	// Create mpv player for CABLE-A Input (virtual speaker device)
	audioPlayer, err := NewPCMPlayerWithDevice(16000, 1, 16, VirtualMicRouteDevice)
	if err != nil {
		glog.Exitf("Create PCM player for CABLE-A Input: %v", err)
	}
	defer audioPlayer.Close()

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
			Uid: "mic_to_cable_a_client",
			Did: "mic_to_cable_a_client",
		},
		SourceAudio: &base.Audio{
			Format:  "pcm",
			Rate:    INPUT_SAMPLE_RATE,
			Bits:    16,
			Channel: INPUT_CHANNELS,
		},
		TargetAudio: &base.Audio{
			Format:  "pcm",
			Rate:    16000,
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
	wg.Add(3) // recording, sending, receiving+playing

	// Audio buffer channel to decouple reading from sending
	audioChan := make(chan []byte, 20)

	// Audio playback queue - balanced buffer to prevent drops while keeping latency reasonable
	audioPlayQueue := make(chan []byte, 50) // ~800ms buffer to handle bursts

	// Audio drop statistics
	var droppedPackets int64
	var droppedBytes int64
	var dropMutex sync.Mutex

	// --- Microphone Recording Goroutine ---
	go func() {
		defer wg.Done()
		safeInfo("Starting physical microphone recording...")
		micRecorder.StartRecording(ctx, audioChan)
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

	// --- Server Receiving and Playback Goroutine ---
	go func() {
		defer wg.Done()
		safeInfo("Starting server message listener, playing to CABLE-A Input...")

		// Start playback sub-goroutine
		var playWg sync.WaitGroup
		playWg.Add(1)
		go func() {
			defer playWg.Done()
			for audioData := range audioPlayQueue {
				if err := audioPlayer.WriteAudio(audioData); err != nil {
					safeErrorf("Failed to play audio to CABLE-A Input: %v", err)
					cancel()
					return
				} else {
					safeV(3).Infof("Played %d bytes to CABLE-A Input", len(audioData))
				}
			}
		}()

		for {
			select {
			case <-ctx.Done():
				safeInfo("Server listener stopped by context.")
				close(audioPlayQueue)
				playWg.Wait()
				return
			default:
			}

			resp := new(ast.TranslateResponse)
			if err := receiveV4MessageWithCancel(ctx, conn, resp); err != nil {
				if err == context.Canceled {
					close(audioPlayQueue)
					playWg.Wait()
					return
				}
				// Check if it's a timeout error
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				if err == io.EOF || strings.Contains(err.Error(), "closed") {
					safeInfo("Connection closed by server.")
				} else {
					safeErrorf("Receive message error: %v", err)
				}
				close(audioPlayQueue)
				playWg.Wait()
				cancel()
				return
			}

			switch resp.GetEvent() {
			case event.Type_SessionFailed, event.Type_SessionCanceled, event.Type_SessionFinished:
				safeInfof("Session ended (event: %s, session_id: %s, message: %s)",
					resp.GetEvent(), resp.GetResponseMeta().GetSessionID(), resp.GetResponseMeta().GetMessage())
				close(audioPlayQueue)
				playWg.Wait()
				cancel()
				return
			case event.Type_TTSResponse:
				if len(resp.GetData()) > 0 {
					safeInfof("Received translated audio: %d bytes, sending to CABLE-A Input", len(resp.GetData()))

					// Send to playback queue
					select {
					case audioPlayQueue <- resp.GetData():
						safeV(3).Infof("Enqueued %d bytes for CABLE-A Input playback", len(resp.GetData()))
					case <-ctx.Done():
						close(audioPlayQueue)
						playWg.Wait()
						return
					default:
						dropMutex.Lock()
						droppedPackets++
						droppedBytes += int64(len(resp.GetData()))
						safeErrorf("!!! AUDIO DROP !!! Queue full, dropped packet #%d (%d bytes, total: %d bytes)", droppedPackets, len(resp.GetData()), droppedBytes)
						dropMutex.Unlock()
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
		fmt.Println("\nCaught signal, shutting down physical mic to CABLE-A Input...")
		cancel()
	case <-ctx.Done():
	}

	// Wait for all goroutines to finish
	wg.Wait()

	// Send FinishSession event after sender goroutine exits to avoid concurrent websocket writes.
	if err := sendV4Request(conn, &ast.TranslateRequest{
		RequestMeta: &rpcmeta.RequestMeta{SessionID: sessionId},
		Event:       event.Type_FinishSession,
	}); err != nil {
		safeErrorf("Finish session: %v", err)
	}
	safeInfo("FinishSession request sent.")

	// Report audio drop statistics
	dropMutex.Lock()
	if droppedPackets > 0 {
		safeErrorf("=== AUDIO DROP SUMMARY === Dropped %d packets (%d bytes total) during session", droppedPackets, droppedBytes)
	} else {
		safeInfof("=== SESSION COMPLETE === No audio packets were dropped")
	}
	dropMutex.Unlock()

	safeInfo("Physical mic to CABLE-A Input translation finished.")
}

// virtualSpeakerToPhysicalSpeaker reads from CABLE-B Output (virtual microphone), translates, and plays to physical speaker
func virtualSpeakerToPhysicalSpeaker(conf Config) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	safeInfo("Initializing CABLE-B Output to physical speaker translation...")

	// Create mpv player for physical speaker playback
	audioPlayer, err := NewPCMPlayer(16000, 1, 16)
	if err != nil {
		glog.Exitf("Create PCM player for physical speaker: %v", err)
	}
	defer audioPlayer.Close()

	// Create FFmpeg recorder for CABLE-B Output (virtual microphone)
	cableBRecorder, err := NewMicrophoneRecorderWithDevice("audio=CABLE-B Output (VB-Audio Cable B)")
	if err != nil {
		glog.Exitf("Create CABLE-B Output recorder: %v", err)
	}
	defer cableBRecorder.Close()

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
			Uid: "cable_b_to_physical_speaker_client",
			Did: "cable_b_to_physical_speaker_client",
		},
		SourceAudio: &base.Audio{
			Format:  "pcm",
			Rate:    INPUT_SAMPLE_RATE,
			Bits:    16,
			Channel: INPUT_CHANNELS,
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
	safeInfof("Session (ID=%s) started. Reading from CABLE-B Output...", sessionId)

	var wg sync.WaitGroup
	wg.Add(3) // recording, sending, receiving+playing

	// Audio buffer channel for sending
	audioChan := make(chan []byte, 20)

	// Audio playback queue - balanced buffer to prevent drops while keeping latency reasonable
	audioPlayQueue := make(chan []byte, 50) // ~800ms buffer to handle bursts

	// Audio drop statistics
	var droppedPackets int64
	var droppedBytes int64
	var dropMutex sync.Mutex

	// --- CABLE-B Recording Goroutine ---
	go func() {
		defer wg.Done()
		safeInfo("Starting CABLE-B Output recording...")
		cableBRecorder.StartRecording(ctx, audioChan)
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

		// Start playback sub-goroutine
		var playWg sync.WaitGroup
		playWg.Add(1)
		go func() {
			defer playWg.Done()
			for audioData := range audioPlayQueue {
				if err := audioPlayer.WriteAudio(audioData); err != nil {
					safeErrorf("Failed to play audio to physical speaker: %v", err)
					cancel()
					return
				} else {
					safeV(3).Infof("Played %d bytes to physical speaker", len(audioData))
				}
			}
		}()

		for {
			select {
			case <-ctx.Done():
				safeInfo("Server listener stopped by context.")
				close(audioPlayQueue)
				playWg.Wait()
				return
			default:
			}

			resp := new(ast.TranslateResponse)
			if err := receiveV4MessageWithCancel(ctx, conn, resp); err != nil {
				if err == context.Canceled {
					close(audioPlayQueue)
					playWg.Wait()
					return
				}
				// Check if it's a timeout error
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				if err == io.EOF || strings.Contains(err.Error(), "closed") {
					safeInfo("Connection closed by server.")
				} else {
					safeErrorf("Receive message error: %v", err)
				}
				close(audioPlayQueue)
				playWg.Wait()
				cancel()
				return
			}

			switch resp.GetEvent() {
			case event.Type_SessionFailed, event.Type_SessionCanceled, event.Type_SessionFinished:
				safeInfof("Session ended (event: %s, session_id: %s, message: %s)",
					resp.GetEvent(), resp.GetResponseMeta().GetSessionID(), resp.GetResponseMeta().GetMessage())
				close(audioPlayQueue)
				playWg.Wait()
				cancel()
				return
			case event.Type_TTSResponse:
				if len(resp.GetData()) > 0 {
					safeInfof("Received translated audio: %d bytes, playing to physical speaker", len(resp.GetData()))

					// Send to playback queue
					select {
					case audioPlayQueue <- resp.GetData():
						safeV(3).Infof("Enqueued %d bytes for physical speaker playback", len(resp.GetData()))
					case <-ctx.Done():
						close(audioPlayQueue)
						playWg.Wait()
						return
					default:
						dropMutex.Lock()
						droppedPackets++
						droppedBytes += int64(len(resp.GetData()))
						safeErrorf("!!! AUDIO DROP !!! Queue full, dropped packet #%d (%d bytes, total: %d bytes)", droppedPackets, len(resp.GetData()), droppedBytes)
						dropMutex.Unlock()
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
		fmt.Println("\nCaught signal, shutting down CABLE-B Output to physical speaker...")
		cancel()
	case <-ctx.Done():
	}

	// Wait for all goroutines to finish
	wg.Wait()

	// Send FinishSession event after sender goroutine exits to avoid concurrent websocket writes.
	if err := sendV4Request(conn, &ast.TranslateRequest{
		RequestMeta: &rpcmeta.RequestMeta{SessionID: sessionId},
		Event:       event.Type_FinishSession,
	}); err != nil {
		safeErrorf("Finish session: %v", err)
	}
	safeInfo("FinishSession request sent.")

	// Report audio drop statistics
	dropMutex.Lock()
	if droppedPackets > 0 {
		safeErrorf("=== AUDIO DROP SUMMARY === Dropped %d packets (%d bytes total) during session", droppedPackets, droppedBytes)
	} else {
		safeInfof("=== SESSION COMPLETE === No audio packets were dropped")
	}
	dropMutex.Unlock()

	safeInfo("CABLE-B Output to physical speaker translation finished.")
}

// bidirectionalTranslation runs both translation modes simultaneously
func bidirectionalTranslation(conf Config) {
	safeInfo("Starting bidirectional translation (physical mic -> CABLE-A + CABLE-B -> physical speaker)...")

	var wg sync.WaitGroup
	wg.Add(2)

	// Mode 1: Physical Mic -> CABLE-A Input
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				safeErrorf("Mode 1 (Physical Mic -> CABLE-A) panicked: %v", r)
			}
		}()
		physicalMicToVirtualMic(conf)
	}()

	// Mode 2: CABLE-B Output -> Physical Speaker
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				safeErrorf("Mode 2 (CABLE-B -> Physical Speaker) panicked: %v", r)
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
		if errorCallback != nil {
			errorCallback(fmt.Errorf("启动会话失败: %v", err))
		}
		return
	}
	safeInfof("Session (ID=%s) started. Please start speaking.", sessionId)

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	var wg sync.WaitGroup
	wg.Add(2)

	// Audio buffer channel to decouple reading from sending
	audioChan := make(chan []byte, 20)

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
				if errorCallback != nil {
					errorCallback(err)
				}
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
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

			switch resp.GetEvent() {
			case event.Type_SessionFailed:
				safeInfof("Session ended (event: %s)", resp.GetEvent())
				if errorCallback != nil {
					errorCallback(fmt.Errorf("会话失败: %s", resp.GetResponseMeta().GetMessage()))
				}
				runCancel()
				senderWg.Wait()
				return
			case event.Type_SessionCanceled:
				safeInfof("Session ended (event: %s)", resp.GetEvent())
				if errorCallback != nil {
					errorCallback(fmt.Errorf("会话被取消: %s", resp.GetResponseMeta().GetMessage()))
				}
				runCancel()
				senderWg.Wait()
				return
			case event.Type_SessionFinished:
				safeInfof("Session ended (event: %s)", resp.GetEvent())
				runCancel()
				senderWg.Wait()
				return
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

// physicalMicToVirtualMicWithDevices records from physical microphone with specific device and plays to CABLE-A Input
func physicalMicToVirtualMicWithDevices(conf Config, micDevice string, ctx context.Context) {
	physicalMicToVirtualMicWithDevicesAndCallback(conf, micDevice, ctx, nil, nil)
}

// physicalMicToVirtualMicWithDevicesAndCallback records from physical microphone with callback support
func physicalMicToVirtualMicWithDevicesAndCallback(conf Config, micDevice string, ctx context.Context, textCallback TextCallback, errorCallback ErrorCallback) {
	safeInfo("Initializing physical mic to CABLE-A Input translation...")

	// Create mpv player for CABLE-A Input (virtual speaker device)
	audioPlayer, err := NewPCMPlayerWithDevice(16000, 1, 16, VirtualMicRouteDevice)
	if err != nil {
		safeErrorf("Create PCM player for CABLE-A Input: %v", err)
		return
	}
	defer audioPlayer.Close()

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
			Uid: "mic_to_cable_a_client",
			Did: "mic_to_cable_a_client",
		},
		SourceAudio: &base.Audio{
			Format:  "pcm",
			Rate:    INPUT_SAMPLE_RATE,
			Bits:    16,
			Channel: INPUT_CHANNELS,
		},
		TargetAudio: &base.Audio{
			Format:  "pcm",
			Rate:    16000,
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
	safeInfof("Session (ID=%s) started. Recording from %s...", sessionId, micDevice)

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	var wg sync.WaitGroup
	wg.Add(2) // recording+sending, receiving+playing

	// Audio buffer channel for microphone input
	audioChan := make(chan []byte, 20)

	// Audio playback queue - balanced buffer to prevent drops while keeping latency reasonable
	audioPlayQueue := make(chan []byte, 50) // ~800ms buffer to handle bursts

	// Audio drop statistics
	var droppedPackets int64
	var droppedBytes int64
	var dropMutex sync.Mutex

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

		// Playback sub-goroutine
		var playWg sync.WaitGroup
		playWg.Add(1)
		go func() {
			defer playWg.Done()
			for audioData := range audioPlayQueue {
				if err := audioPlayer.WriteAudio(audioData); err != nil {
					safeErrorf("Failed to play audio to CABLE-A Input: %v", err)
					if errorCallback != nil {
						errorCallback(fmt.Errorf("播放音频失败: %v", err))
					}
					runCancel()
					return
				}
			}
		}()

		var closePlayQueueOnce sync.Once
		closePlayQueue := func() {
			closePlayQueueOnce.Do(func() {
				close(audioPlayQueue)
			})
		}

		waitWorkers := func(withCancel bool) {
			closePlayQueue()
			playWg.Wait()
			if withCancel {
				runCancel()
			}
			senderWg.Wait()
		}

		// Receiver
		for {
			select {
			case <-runCtx.Done():
				waitWorkers(false)
				return
			default:
			}

			resp := new(ast.TranslateResponse)
			if err := receiveV4MessageWithCancel(runCtx, conn, resp); err != nil {
				if err == context.Canceled {
					waitWorkers(false)
					return
				}
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				// WebSocket异常关闭
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
				waitWorkers(true)
				return
			}

			switch resp.GetEvent() {
			case event.Type_SessionFailed, event.Type_SessionCanceled, event.Type_SessionFinished:
				waitWorkers(true)
				return
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
					select {
					case audioPlayQueue <- resp.GetData():
					case <-runCtx.Done():
						waitWorkers(false)
						return
					default:
						dropMutex.Lock()
						droppedPackets++
						droppedBytes += int64(len(resp.GetData()))
						safeErrorf("!!! AUDIO DROP !!! Queue full, dropped packet #%d (%d bytes, total: %d bytes)", droppedPackets, len(resp.GetData()), droppedBytes)
						dropMutex.Unlock()
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

	// Report audio drop statistics
	dropMutex.Lock()
	if droppedPackets > 0 {
		safeErrorf("=== AUDIO DROP SUMMARY === Dropped %d packets (%d bytes total) during session", droppedPackets, droppedBytes)
	} else {
		safeInfof("=== SESSION COMPLETE === No audio packets were dropped")
	}
	dropMutex.Unlock()

	safeInfo("physicalMicToVirtualMicWithDevices finished.")
}

// virtualSpeakerToPhysicalSpeakerWithDevices reads from CABLE-B Output and plays to specific physical speaker
func virtualSpeakerToPhysicalSpeakerWithDevices(conf Config, speakerDevice string, ctx context.Context) {
	virtualSpeakerToPhysicalSpeakerWithDevicesAndCallback(conf, speakerDevice, ctx, nil, nil)
}

// virtualSpeakerToPhysicalSpeakerWithDevicesAndCallback reads from CABLE-B Output with callback support
func virtualSpeakerToPhysicalSpeakerWithDevicesAndCallback(conf Config, speakerDevice string, ctx context.Context, textCallback TextCallback, errorCallback ErrorCallback) {
	safeInfo("Initializing CABLE-B Output to physical speaker translation...")

	// Create mpv player for physical speaker playback
	audioPlayer, err := NewPCMPlayerWithDevice(16000, 1, 16, speakerDevice)
	if err != nil {
		safeErrorf("Create PCM player for %s: %v", speakerDevice, err)
		return
	}
	defer audioPlayer.Close()

	// Create FFmpeg recorder for CABLE-B Output (virtual microphone)
	cableBRecorder, err := NewMicrophoneRecorderWithDevice("audio=CABLE-B Output (VB-Audio Cable B)")
	if err != nil {
		safeErrorf("Create CABLE-B Output recorder: %v", err)
		return
	}
	defer cableBRecorder.Close()

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
			Uid: "cable_b_to_physical_speaker_client",
			Did: "cable_b_to_physical_speaker_client",
		},
		SourceAudio: &base.Audio{
			Format:  "pcm",
			Rate:    INPUT_SAMPLE_RATE,
			Bits:    16,
			Channel: INPUT_CHANNELS,
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
	safeInfof("Session (ID=%s) started. Reading from CABLE-B Output...", sessionId)

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	var wg sync.WaitGroup
	wg.Add(2) // recording+sending, receiving+playing

	// Audio buffer channel
	audioChan := make(chan []byte, 20)

	// Audio playback queue - balanced buffer to prevent drops while keeping latency reasonable
	audioPlayQueue := make(chan []byte, 50) // ~800ms buffer to handle bursts

	// Audio drop statistics
	var droppedPackets int64
	var droppedBytes int64
	var dropMutex sync.Mutex

	// --- Context Cancellation Monitor ---
	// Close WebSocket when context is cancelled to unblock receiveV4Message
	go func() {
		<-runCtx.Done()
		safeInfo("Context cancelled, closing WebSocket connection...")
		conn.Close()
	}()

	// --- CABLE-B Recording Goroutine ---
	go func() {
		defer wg.Done()
		cableBRecorder.StartRecording(runCtx, audioChan)
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

		// Playback sub-goroutine
		var playWg sync.WaitGroup
		playWg.Add(1)
		go func() {
			defer playWg.Done()
			for audioData := range audioPlayQueue {
				if err := audioPlayer.WriteAudio(audioData); err != nil {
					safeErrorf("Failed to play audio to %s: %v", speakerDevice, err)
					if errorCallback != nil {
						errorCallback(fmt.Errorf("播放音频失败: %v", err))
					}
					runCancel()
					return
				}
			}
		}()

		var closePlayQueueOnce sync.Once
		closePlayQueue := func() {
			closePlayQueueOnce.Do(func() {
				close(audioPlayQueue)
			})
		}

		waitWorkers := func(withCancel bool) {
			closePlayQueue()
			playWg.Wait()
			if withCancel {
				runCancel()
			}
			senderWg.Wait()
		}

		// Receiver
		for {
			select {
			case <-runCtx.Done():
				waitWorkers(false)
				return
			default:
			}

			resp := new(ast.TranslateResponse)
			if err := receiveV4MessageWithCancel(runCtx, conn, resp); err != nil {
				if err == context.Canceled {
					waitWorkers(false)
					return
				}
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				// WebSocket异常关闭
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
				waitWorkers(true)
				return
			}

			switch resp.GetEvent() {
			case event.Type_SessionFailed, event.Type_SessionCanceled, event.Type_SessionFinished:
				waitWorkers(true)
				return
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
					select {
					case audioPlayQueue <- resp.GetData():
					case <-runCtx.Done():
						waitWorkers(false)
						return
					default:
						dropMutex.Lock()
						droppedPackets++
						droppedBytes += int64(len(resp.GetData()))
						safeErrorf("!!! AUDIO DROP !!! Queue full, dropped packet #%d (%d bytes, total: %d bytes)", droppedPackets, len(resp.GetData()), droppedBytes)
						dropMutex.Unlock()
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

	// Report audio drop statistics
	dropMutex.Lock()
	if droppedPackets > 0 {
		safeErrorf("=== AUDIO DROP SUMMARY === Dropped %d packets (%d bytes total) during session", droppedPackets, droppedBytes)
	} else {
		safeInfof("=== SESSION COMPLETE === No audio packets were dropped")
	}
	dropMutex.Unlock()

	safeInfo("virtualSpeakerToPhysicalSpeakerWithDevices finished.")
}
