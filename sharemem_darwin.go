//go:build darwin
// +build darwin

package main

/*
#cgo linux LDFLAGS: -lrt
#include <stdio.h>
#include <stdlib.h>
#include <errno.h>
#include <unistd.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <fcntl.h>

// shm_open wrapper that returns errno via out parameter to avoid using C.errno in Go.
static int shm_open_wrapper(const char* name, int oflag, mode_t mode, int* err_out) {
    int fd = shm_open(name, oflag, mode);
    if (fd == -1) {
        *err_out = errno;
    }
    return fd;
}
*/
import "C"
import (
	"fmt"
	"os"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Must match the driver's configuration
const (
	SharedMemoryInputName  = "/translatoraudio_input_shm"
	SharedMemoryOutputName = "/translatoraudio_output_shm"
	RingBufferFrames       = 16384
	Channels               = 1
	SampleSize             = 2 // sizeof(int16_t)
	FrameSize              = Channels * SampleSize
	RingBufferSize         = RingBufferFrames * FrameSize
	SampleRate             = 16000.0
	InputPcmFile           = "debug_pcm.pcm"

	// Magic number and version for validation
	TranslatorAudioMagic   = 0x54415544 // "TAUD"
	TranslatorAudioVersion = 3
)

// Lock-free ring buffer structure (must match driver)
// typedef struct {
//     uint32_t magic;                 // Magic number for validation
//     uint32_t version;               // Structure version
//     _Atomic uint64_t write_pos;     // Write position (producer) - Free running
//     _Atomic uint64_t read_pos;      // Read position (consumer) - Free running
//     uint32_t buffer_size;           // Size in frames
//     _Atomic uint32_t sample_rate;   // Current sample rate (set by driver)
//     uint32_t channels;              // Number of channels
//     uint32_t bits_per_sample;       // Bits per sample
//     uint8_t reserved[8];            // Reserved for future use
//     uint8_t audio_data[];           // Flexible array member for audio data
// } LockFreeRingBuffer;

// We can't use a flexible array member in Go, so we define the struct
// with only the fixed-size fields. The audio_data is accessed by pointer arithmetic.
type LockFreeRingBuffer struct {
	Magic         uint32   // Magic number for validation (TranslatorAudioMagic)
	Version       uint32   // Structure version (TranslatorAudioVersion)
	WritePos      uint64   // Write position (producer)
	ReadPos       uint64   // Read position (consumer)
	BufferSize    uint32   // Size in frames
	SampleRate    uint32   // Current sample rate (set by driver, read-only for clients)
	Channels      uint32   // Number of channels
	BitsPerSample uint32   // Bits per sample
	Reserved      [8]uint8 // Reserved for future use
	// audio_data is the flexible member, not explicitly in the struct
}

// Shared memory structure (must match driver)
type SharedMemoryData struct {
	RingBuffer LockFreeRingBuffer
}

var (
	gSharedMemory     *SharedMemoryData
	gSharedMemoryFd   int = -1
	gSharedMemorySize uintptr
	// Mapped address for munmap
	mappedAddr []byte

	// Output (speaker) shared memory
	gSharedMemoryOutput     *SharedMemoryData
	gSharedMemoryOutputFd   int = -1
	gSharedMemoryOutputSize uintptr
	mappedAddrOutput        []byte
)

// shm_open implementation using cgo
func shmOpen(name string, flag int, perm uint32) (int, error) {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))

	var cErr C.int = 0
	fd := C.shm_open_wrapper(cName, C.int(flag), C.mode_t(perm), &cErr)
	if fd == -1 {
		return -1, syscall.Errno(cErr)
	}
	return int(fd), nil
}

// ValidateSharedMemory checks magic number and version
func ValidateSharedMemory(shm *SharedMemoryData) error {
	if shm == nil {
		return fmt.Errorf("shared memory is nil")
	}

	rb := &shm.RingBuffer

	if rb.Magic != TranslatorAudioMagic {
		return fmt.Errorf("invalid magic number: 0x%08X (expected 0x%08X)",
			rb.Magic, TranslatorAudioMagic)
	}
	if rb.Version != TranslatorAudioVersion {
		return fmt.Errorf("version mismatch: %d (expected %d)",
			rb.Version, TranslatorAudioVersion)
	}
	if rb.BufferSize == 0 || rb.BufferSize > RingBufferFrames {
		return fmt.Errorf("invalid buffer size: %d (expected 1-%d frames)",
			rb.BufferSize, RingBufferFrames)
	}
	if rb.Channels != Channels {
		return fmt.Errorf("unexpected channels: %d (expected %d)",
			rb.Channels, Channels)
	}
	expectedBitsPerSample := uint32(SampleSize * 8)
	if rb.BitsPerSample != expectedBitsPerSample {
		return fmt.Errorf("unexpected bits per sample: %d (expected %d)",
			rb.BitsPerSample, expectedBitsPerSample)
	}
	if rb.SampleRate == 0 {
		return fmt.Errorf("invalid sample rate: %d", rb.SampleRate)
	}
	return nil
}

// Initialize connection to shared memory
func initializeSharedMemoryConnection() error {
	// Calculate total shared memory size
	gSharedMemorySize = unsafe.Sizeof(SharedMemoryData{}) + uintptr(RingBufferSize)

	// Open existing shared memory using shm_open from C
	var err error
	gSharedMemoryFd, err = shmOpen(SharedMemoryInputName, os.O_RDWR, 0666)
	if err != nil {
		return fmt.Errorf("failed to open shared memory: %v\nMake sure the translatorAudio driver is loaded first", err)
	}

	// Map shared memory to process address space
	// We use the Go unix package's Mmap for this.
	mappedAddr, err = unix.Mmap(int(gSharedMemoryFd), 0, int(gSharedMemorySize), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		unix.Close(int(gSharedMemoryFd))
		return fmt.Errorf("failed to map shared memory: %v", err)
	}

	// Cast the mapped memory to our struct type
	gSharedMemory = (*SharedMemoryData)(unsafe.Pointer(&mappedAddr[0]))

	// Validate magic number and version
	if err := ValidateSharedMemory(gSharedMemory); err != nil {
		unix.Munmap(mappedAddr)
		unix.Close(int(gSharedMemoryFd))
		mappedAddr = nil
		gSharedMemory = nil
		gSharedMemoryFd = -1
		return fmt.Errorf("shared memory validation failed: %v", err)
	}

	fmt.Println("Connected to shared memory successfully")
	fmt.Printf("Magic: 0x%08X, Version: %d\n", gSharedMemory.RingBuffer.Magic, gSharedMemory.RingBuffer.Version)
	fmt.Printf("Ring buffer size: %d frames, Sample rate: %d Hz\n",
		gSharedMemory.RingBuffer.BufferSize, gSharedMemory.RingBuffer.SampleRate)
	return nil
}

// Cleanup shared memory connection
func cleanupSharedMemoryConnection() {
	if mappedAddr != nil {
		unix.Munmap(mappedAddr)
		mappedAddr = nil
		gSharedMemory = nil
	}

	if gSharedMemoryFd != -1 {
		unix.Close(int(gSharedMemoryFd))
		gSharedMemoryFd = -1
	}
}

// Calculate available space for writing
func ringBufferAvailableToWrite(rb *LockFreeRingBuffer) uint32 {
	// Using atomic operations to read positions
	writePos := atomicLoadUint64(&rb.WritePos)
	readPos := atomicLoadUint64(&rb.ReadPos)

	diff := writePos - readPos
	if diff > uint64(rb.BufferSize) {
		return 0
	}
	return rb.BufferSize - uint32(diff)
}

// Write frames to ring buffer (lock-free)
// For real-time microphone input: overwrites old data when buffer is full
func ringBufferWriteFrames(rb *LockFreeRingBuffer, buffer []byte, frameCount uint32) uint32 {
	if rb == nil || frameCount == 0 {
		return 0
	}

	maxFramesFromBuffer := uint32(len(buffer) / FrameSize)
	if maxFramesFromBuffer == 0 {
		return 0
	}
	if frameCount > maxFramesFromBuffer {
		frameCount = maxFramesFromBuffer
	}

	bufferSize := rb.BufferSize
	if bufferSize == 0 || bufferSize > RingBufferFrames {
		return 0
	}

	// If asking to write more than total buffer size, only write the last buffer_size frames
	if frameCount > bufferSize {
		startOffset := (frameCount - bufferSize) * FrameSize
		buffer = buffer[startOffset:]
		frameCount = bufferSize
	}

	// We allow overwriting, so we don't need to check available space or modify read_pos.
	// We simply write to the next write position.

	writePos := atomicLoadUint64(&rb.WritePos)

	// Calculate bytes to write
	bytesToWrite := int(frameCount) * FrameSize
	writeIndex := writePos % uint64(bufferSize)
	writeBytesPos := int(writeIndex) * FrameSize
	audioData := ringBufferAudioData(rb)
	if len(audioData) == 0 {
		return 0
	}

	// Handle wrap-around
	if writeIndex+uint64(frameCount) <= uint64(bufferSize) {
		// No wrap-around needed
		copy(audioData[writeBytesPos:writeBytesPos+bytesToWrite], buffer[:bytesToWrite])
	} else {
		// Wrap-around needed
		framesToEnd := int(uint64(bufferSize) - writeIndex)
		bytesToEnd := framesToEnd * FrameSize
		remainingBytes := bytesToWrite - bytesToEnd

		copy(audioData[writeBytesPos:writeBytesPos+bytesToEnd], buffer[:bytesToEnd])
		copy(audioData[:remainingBytes], buffer[bytesToEnd:bytesToWrite])
	}

	// Update write position atomically
	atomicStoreUint64(&rb.WritePos, writePos+uint64(frameCount))

	return frameCount // Always write all frames (overwriting old data if needed)
}

// Atomic operations using sync/atomic
func atomicLoadUint32(addr *uint32) uint32 {
	return atomic.LoadUint32(addr)
}

func atomicStoreUint32(addr *uint32, val uint32) {
	atomic.StoreUint32(addr, val)
}

func atomicLoadUint64(addr *uint64) uint64 {
	return atomic.LoadUint64(addr)
}

func atomicStoreUint64(addr *uint64, val uint64) {
	atomic.StoreUint64(addr, val)
}

func ringBufferAudioData(rb *LockFreeRingBuffer) []byte {
	if rb == nil || rb.BufferSize == 0 || rb.BufferSize > RingBufferFrames {
		return nil
	}
	audioDataPtr := unsafe.Pointer(uintptr(unsafe.Pointer(rb)) + unsafe.Sizeof(LockFreeRingBuffer{}))
	audioDataBytes := int(rb.BufferSize) * FrameSize
	return unsafe.Slice((*byte)(audioDataPtr), audioDataBytes)
}

// Initialize connection to output (speaker) shared memory
func initializeOutputSharedMemoryConnection() error {
	// Calculate total shared memory size
	gSharedMemoryOutputSize = unsafe.Sizeof(SharedMemoryData{}) + uintptr(RingBufferSize)

	// Open existing shared memory using shm_open from C
	var err error
	gSharedMemoryOutputFd, err = shmOpen(SharedMemoryOutputName, os.O_RDWR, 0666)
	if err != nil {
		return fmt.Errorf("failed to open output shared memory: %v\nMake sure the translatorAudio driver is loaded", err)
	}

	// Map shared memory to process address space
	mappedAddrOutput, err = unix.Mmap(int(gSharedMemoryOutputFd), 0, int(gSharedMemoryOutputSize), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		unix.Close(int(gSharedMemoryOutputFd))
		return fmt.Errorf("failed to map output shared memory: %v", err)
	}

	// Cast the mapped memory to our struct type
	gSharedMemoryOutput = (*SharedMemoryData)(unsafe.Pointer(&mappedAddrOutput[0]))

	// Validate magic number and version
	if err := ValidateSharedMemory(gSharedMemoryOutput); err != nil {
		unix.Munmap(mappedAddrOutput)
		unix.Close(int(gSharedMemoryOutputFd))
		mappedAddrOutput = nil
		gSharedMemoryOutput = nil
		gSharedMemoryOutputFd = -1
		return fmt.Errorf("output shared memory validation failed: %v", err)
	}

	fmt.Println("Connected to output (speaker) shared memory successfully")
	fmt.Printf("Magic: 0x%08X, Version: %d\n", gSharedMemoryOutput.RingBuffer.Magic, gSharedMemoryOutput.RingBuffer.Version)
	fmt.Printf("Ring buffer size: %d frames, Sample rate: %d Hz\n",
		gSharedMemoryOutput.RingBuffer.BufferSize, gSharedMemoryOutput.RingBuffer.SampleRate)
	return nil
}

// Cleanup output shared memory connection
func cleanupOutputSharedMemoryConnection() {
	if mappedAddrOutput != nil {
		unix.Munmap(mappedAddrOutput)
		mappedAddrOutput = nil
		gSharedMemoryOutput = nil
	}

	if gSharedMemoryOutputFd != -1 {
		unix.Close(int(gSharedMemoryOutputFd))
		gSharedMemoryOutputFd = -1
	}
}

// Calculate available data for reading
func ringBufferAvailableToRead(rb *LockFreeRingBuffer) uint32 {
	writePos := atomicLoadUint64(&rb.WritePos)
	readPos := atomicLoadUint64(&rb.ReadPos)

	if writePos < readPos {
		return 0
	}
	diff := writePos - readPos
	if diff > uint64(rb.BufferSize) {
		return rb.BufferSize
	}
	return uint32(diff)
}

// Read frames from ring buffer (lock-free)
func ringBufferReadFrames(rb *LockFreeRingBuffer, buffer []byte, frameCount uint32) uint32 {
	if rb == nil || frameCount == 0 {
		return 0
	}

	maxFramesFromBuffer := uint32(len(buffer) / FrameSize)
	if maxFramesFromBuffer == 0 {
		return 0
	}
	if frameCount > maxFramesFromBuffer {
		frameCount = maxFramesFromBuffer
	}

	bufferSize := rb.BufferSize
	if bufferSize == 0 || bufferSize > RingBufferFrames {
		return 0
	}

	writePos := atomicLoadUint64(&rb.WritePos)
	readPos := atomicLoadUint64(&rb.ReadPos)

	if writePos < readPos {
		return 0
	}

	diff := writePos - readPos
	// Check for overrun
	if diff > uint64(rb.BufferSize) {
		readPos = writePos - uint64(rb.BufferSize)
		diff = uint64(rb.BufferSize)
		atomicStoreUint64(&rb.ReadPos, readPos)
	}

	available := uint32(diff)
	toRead := frameCount
	if toRead > available {
		toRead = available
	}

	if toRead == 0 {
		return 0
	}

	// Calculate bytes to read
	bytesToRead := int(toRead) * FrameSize
	readIndex := readPos % uint64(bufferSize)
	readBytesPos := int(readIndex) * FrameSize
	audioData := ringBufferAudioData(rb)
	if len(audioData) == 0 {
		return 0
	}

	// Handle wrap-around
	if readIndex+uint64(toRead) <= uint64(bufferSize) {
		// No wrap-around needed
		copy(buffer[:bytesToRead], audioData[readBytesPos:readBytesPos+bytesToRead])
	} else {
		// Wrap-around needed
		framesToEnd := int(uint64(bufferSize) - readIndex)
		bytesToEnd := framesToEnd * FrameSize
		remainingBytes := bytesToRead - bytesToEnd

		copy(buffer[:bytesToEnd], audioData[readBytesPos:readBytesPos+bytesToEnd])
		copy(buffer[bytesToEnd:bytesToRead], audioData[:remainingBytes])
	}

	// Update read position atomically
	atomicStoreUint64(&rb.ReadPos, readPos+uint64(toRead))

	return toRead
}
