package main

// ModelType 表示翻译模型类型
type ModelType int

const (
	ModelDoubao ModelType = iota // Doubao模型（模型一）
	ModelQwen                    // Qwen模型（模型二）
)

const (
	defaultDoubaoHost       = "wss://openspeech.bytedance.com"
	defaultDoubaoEndpoint   = "api/v4/ast/v2/translate"
	defaultDoubaoResourceID = "volc.service_type.10053"

	defaultQwenHost     = "wss://dashscope.aliyuncs.com"
	defaultQwenEndpoint = "api-ws/v1/realtime"
	defaultQwenModel    = "qwen3-livetranslate-flash-realtime"
)

// String 返回模型类型的字符串表示
func (m ModelType) String() string {
	switch m {
	case ModelDoubao:
		return "模型一"
	case ModelQwen:
		return "模型二"
	default:
		return "Unknown"
	}
}

// GetModelConfig 根据模型类型返回对应的官方连接配置。
func GetModelConfig(modelType ModelType) (host, endpoint string) {
	switch modelType {
	case ModelDoubao:
		return defaultDoubaoHost, defaultDoubaoEndpoint
	case ModelQwen:
		return defaultQwenHost, defaultQwenEndpoint
	default:
		return defaultDoubaoHost, defaultDoubaoEndpoint
	}
}
