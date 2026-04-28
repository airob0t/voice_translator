package main

import (
	"math"
)

// Resampler 高质量音频重采样器
// 使用带限sinc插值实现24kHz -> 16kHz的下采样
type Resampler struct {
	inputRate  int
	outputRate int
	ratio      float64 // inputRate / outputRate

	// Sinc滤波器参数
	filterHalfLen int       // 滤波器半长度（单边tap数）
	filterTaps    []float64 // 预计算的滤波器系数
	oversampling  int       // 过采样因子，用于插值精度

	// 状态缓冲区（存储历史样本用于滤波）
	history    []float64
	historyLen int

	// 输出位置追踪
	inputPos float64
}

// NewResampler 创建一个新的重采样器
// inputRate: 输入采样率 (例如 24000)
// outputRate: 输出采样率 (例如 16000)
func NewResampler(inputRate, outputRate int) *Resampler {
	r := &Resampler{
		inputRate:     inputRate,
		outputRate:    outputRate,
		ratio:         float64(inputRate) / float64(outputRate),
		filterHalfLen: 16,  // 每侧16个tap，共32个tap的滤波器
		oversampling:  256, // 256倍过采样用于插值精度
	}

	// 预计算sinc滤波器系数
	r.precomputeFilter()

	// 初始化历史缓冲区
	r.historyLen = r.filterHalfLen * 2
	r.history = make([]float64, r.historyLen)
	r.inputPos = 0

	return r
}

// precomputeFilter 预计算带限sinc滤波器系数
func (r *Resampler) precomputeFilter() {
	// 滤波器总长度 = (filterHalfLen * 2) * oversampling
	filterLen := r.filterHalfLen * 2 * r.oversampling
	r.filterTaps = make([]float64, filterLen+1)

	// 使用Blackman-Harris窗口函数以获得更好的阻带衰减
	// 截止频率设为输出采样率的Nyquist频率
	cutoff := 0.5 / r.ratio // 归一化截止频率
	if cutoff > 0.5 {
		cutoff = 0.5
	}

	// 调整截止频率以减少混叠（乘以0.9提供一些余量）
	cutoff *= 0.95

	for i := 0; i <= filterLen; i++ {
		// 计算相对于滤波器中心的位置
		t := float64(i-filterLen/2) / float64(r.oversampling)

		// sinc函数
		var sinc float64
		if math.Abs(t) < 1e-10 {
			sinc = 1.0
		} else {
			x := math.Pi * t * 2 * cutoff
			sinc = math.Sin(x) / x
		}

		// Blackman-Harris窗口 (4-term)
		n := float64(i) / float64(filterLen)
		a0, a1, a2, a3 := 0.35875, 0.48829, 0.14128, 0.01168
		window := a0 - a1*math.Cos(2*math.Pi*n) + a2*math.Cos(4*math.Pi*n) - a3*math.Cos(6*math.Pi*n)

		r.filterTaps[i] = sinc * window * 2 * cutoff
	}

	// 归一化滤波器系数（确保DC增益为1）
	sum := 0.0
	for i := 0; i <= filterLen; i += r.oversampling {
		sum += r.filterTaps[i]
	}
	if sum > 0 {
		scale := 1.0 / sum
		for i := range r.filterTaps {
			r.filterTaps[i] *= scale
		}
	}
}

// interpolate 使用预计算的滤波器进行插值
func (r *Resampler) interpolate(samples []float64, pos float64) float64 {
	intPos := int(pos)
	fracPos := pos - float64(intPos)

	// 计算滤波器相位索引
	phaseIdx := int(fracPos * float64(r.oversampling))

	result := 0.0
	filterCenter := r.filterHalfLen * r.oversampling

	// 对历史样本和当前样本进行卷积
	for tap := -r.filterHalfLen; tap < r.filterHalfLen; tap++ {
		sampleIdx := intPos + tap

		var sample float64
		if sampleIdx < 0 {
			// 从历史缓冲区读取
			histIdx := r.historyLen + sampleIdx
			if histIdx >= 0 && histIdx < r.historyLen {
				sample = r.history[histIdx]
			}
		} else if sampleIdx < len(samples) {
			sample = samples[sampleIdx]
		}

		// 获取对应的滤波器系数
		filterIdx := filterCenter - tap*r.oversampling - phaseIdx
		if filterIdx >= 0 && filterIdx < len(r.filterTaps) {
			result += sample * r.filterTaps[filterIdx]
		}
	}

	return result
}

// Resample16 重采样16位PCM音频数据
// input: 输入的16位小端PCM数据 (24kHz)
// 返回: 重采样后的16位小端PCM数据 (16kHz)
func (r *Resampler) Resample16(input []byte) []byte {
	if len(input) < 2 {
		return nil
	}

	// 转换输入字节为float64样本
	inputSamples := len(input) / 2
	samples := make([]float64, inputSamples)
	for i := 0; i < inputSamples; i++ {
		// 16-bit little endian
		sample := int16(input[i*2]) | int16(input[i*2+1])<<8
		samples[i] = float64(sample) / 32768.0
	}

	// 计算输出样本数
	outputSamples := int(float64(inputSamples) / r.ratio)
	if outputSamples <= 0 {
		// 保存历史并返回
		r.updateHistory(samples)
		return nil
	}

	output := make([]float64, outputSamples)

	// 对每个输出样本进行插值
	for i := 0; i < outputSamples; i++ {
		inputIdx := r.inputPos + float64(i)*r.ratio
		output[i] = r.interpolate(samples, inputIdx)
	}

	// 更新输入位置（保留小数部分用于下次处理）
	r.inputPos = r.inputPos + float64(outputSamples)*r.ratio - float64(inputSamples)

	// 更新历史缓冲区
	r.updateHistory(samples)

	// 转换输出为16位PCM字节
	result := make([]byte, outputSamples*2)
	for i := 0; i < outputSamples; i++ {
		// 限幅
		sample := output[i]
		if sample > 1.0 {
			sample = 1.0
		} else if sample < -1.0 {
			sample = -1.0
		}

		// 转换为16位整数
		intSample := int16(sample * 32767.0)
		result[i*2] = byte(intSample)
		result[i*2+1] = byte(intSample >> 8)
	}

	return result
}

// updateHistory 更新历史缓冲区
func (r *Resampler) updateHistory(samples []float64) {
	if len(samples) >= r.historyLen {
		// 如果输入足够长，直接取最后historyLen个样本
		copy(r.history, samples[len(samples)-r.historyLen:])
	} else {
		// 否则，左移历史并添加新样本
		shiftLen := r.historyLen - len(samples)
		copy(r.history[:shiftLen], r.history[len(samples):])
		copy(r.history[shiftLen:], samples)
	}
}

// Reset 重置重采样器状态
func (r *Resampler) Reset() {
	r.inputPos = 0
	for i := range r.history {
		r.history[i] = 0
	}
}

// Resample24to16 便捷函数：将24kHz音频重采样到16kHz
// 这是一个无状态的简化版本，适用于不需要跨块连续性的场景
func Resample24to16(input []byte) []byte {
	if len(input) < 2 {
		return nil
	}

	// 24kHz -> 16kHz 的比率是 3:2
	inputSamples := len(input) / 2
	outputSamples := (inputSamples * 2) / 3

	if outputSamples <= 0 {
		return nil
	}

	output := make([]byte, outputSamples*2)

	// 使用线性插值的简化版本
	for i := 0; i < outputSamples; i++ {
		// 计算输入位置 (使用1.5的比率)
		srcPos := float64(i) * 1.5
		srcIdx := int(srcPos)
		frac := srcPos - float64(srcIdx)

		// 获取两个相邻样本
		var s0, s1 int16
		if srcIdx*2+1 < len(input) {
			s0 = int16(input[srcIdx*2]) | int16(input[srcIdx*2+1])<<8
		}
		if (srcIdx+1)*2+1 < len(input) {
			s1 = int16(input[(srcIdx+1)*2]) | int16(input[(srcIdx+1)*2+1])<<8
		}

		// 线性插值
		sample := float64(s0)*(1.0-frac) + float64(s1)*frac
		intSample := int16(sample)

		output[i*2] = byte(intSample)
		output[i*2+1] = byte(intSample >> 8)
	}

	return output
}
