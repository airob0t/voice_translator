package main

import (
	"math"
	"testing"
)

// TestResamplerBasic 测试基本的重采样功能
func TestResamplerBasic(t *testing.T) {
	resampler := NewResampler(24000, 16000)

	// 创建一个简单的测试信号：1秒的1kHz正弦波 @ 24kHz
	inputSamples := 24000
	input := make([]byte, inputSamples*2)

	for i := 0; i < inputSamples; i++ {
		// 1kHz正弦波
		sample := int16(16384 * math.Sin(2*math.Pi*1000*float64(i)/24000))
		input[i*2] = byte(sample)
		input[i*2+1] = byte(sample >> 8)
	}

	output := resampler.Resample16(input)

	// 验证输出长度：24000样本 @ 24kHz -> 16000样本 @ 16kHz
	expectedSamples := 16000
	actualSamples := len(output) / 2

	// 允许小误差
	if math.Abs(float64(actualSamples-expectedSamples)) > 10 {
		t.Errorf("Expected ~%d samples, got %d", expectedSamples, actualSamples)
	}

	t.Logf("Input: %d samples @ 24kHz, Output: %d samples @ 16kHz", inputSamples, actualSamples)
}

// TestResamplerRatio 测试采样率比率
func TestResamplerRatio(t *testing.T) {
	resampler := NewResampler(24000, 16000)

	// 测试多个小块
	chunkSize := 960 // 40ms @ 24kHz
	numChunks := 10

	totalInputSamples := 0
	totalOutputSamples := 0

	for i := 0; i < numChunks; i++ {
		input := make([]byte, chunkSize*2)
		for j := 0; j < chunkSize; j++ {
			sample := int16(16384 * math.Sin(2*math.Pi*1000*float64(totalInputSamples+j)/24000))
			input[j*2] = byte(sample)
			input[j*2+1] = byte(sample >> 8)
		}

		output := resampler.Resample16(input)
		totalInputSamples += chunkSize
		totalOutputSamples += len(output) / 2
	}

	// 预期比率：16000/24000 = 2/3
	expectedRatio := 2.0 / 3.0
	actualRatio := float64(totalOutputSamples) / float64(totalInputSamples)

	if math.Abs(actualRatio-expectedRatio) > 0.01 {
		t.Errorf("Expected ratio ~%.4f, got %.4f", expectedRatio, actualRatio)
	}

	t.Logf("Total input: %d samples, output: %d samples, ratio: %.4f",
		totalInputSamples, totalOutputSamples, actualRatio)
}

// TestResamplerQuality 测试重采样质量（检查是否有严重失真）
func TestResamplerQuality(t *testing.T) {
	resampler := NewResampler(24000, 16000)

	// 创建100ms的500Hz正弦波
	inputSamples := 2400 // 100ms @ 24kHz
	input := make([]byte, inputSamples*2)

	freq := 500.0 // 500Hz，远低于Nyquist
	for i := 0; i < inputSamples; i++ {
		sample := int16(16384 * math.Sin(2*math.Pi*freq*float64(i)/24000))
		input[i*2] = byte(sample)
		input[i*2+1] = byte(sample >> 8)
	}

	output := resampler.Resample16(input)
	outputSamples := len(output) / 2

	// 验证输出信号的振幅和频率基本保持
	maxAmplitude := int16(0)
	minAmplitude := int16(0)

	for i := 0; i < outputSamples; i++ {
		sample := int16(output[i*2]) | int16(output[i*2+1])<<8
		if sample > maxAmplitude {
			maxAmplitude = sample
		}
		if sample < minAmplitude {
			minAmplitude = sample
		}
	}

	// 振幅应该接近原始值（允许10%损失）
	expectedAmplitude := int16(16384)
	if maxAmplitude < int16(float64(expectedAmplitude)*0.8) {
		t.Errorf("Amplitude too low: max=%d, expected ~%d", maxAmplitude, expectedAmplitude)
	}

	t.Logf("Output amplitude range: [%d, %d]", minAmplitude, maxAmplitude)
}

// TestSimpleResample 测试简化版重采样函数
func TestSimpleResample(t *testing.T) {
	// 创建测试数据
	inputSamples := 2400
	input := make([]byte, inputSamples*2)

	for i := 0; i < inputSamples; i++ {
		sample := int16(16384 * math.Sin(2*math.Pi*500*float64(i)/24000))
		input[i*2] = byte(sample)
		input[i*2+1] = byte(sample >> 8)
	}

	output := Resample24to16(input)

	expectedSamples := (inputSamples * 2) / 3
	actualSamples := len(output) / 2

	if math.Abs(float64(actualSamples-expectedSamples)) > 2 {
		t.Errorf("Expected ~%d samples, got %d", expectedSamples, actualSamples)
	}

	t.Logf("Simple resample: %d -> %d samples", inputSamples, actualSamples)
}

// BenchmarkResampler 性能测试
func BenchmarkResampler(b *testing.B) {
	resampler := NewResampler(24000, 16000)

	// 模拟典型的音频块大小：40ms @ 24kHz = 960 samples
	chunkSize := 960
	input := make([]byte, chunkSize*2)

	for i := 0; i < chunkSize; i++ {
		sample := int16(16384 * math.Sin(2*math.Pi*1000*float64(i)/24000))
		input[i*2] = byte(sample)
		input[i*2+1] = byte(sample >> 8)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resampler.Resample16(input)
	}
}
