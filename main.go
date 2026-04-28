package main

import (
	"flag"
	log "log/slog"
	"os"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2/app"
)

var (
	// Common flags.
	target = flag.String("target", "gui", "Target service: ast, sts, etc.")
	outdir = flag.String("outdir", "./", "Result output directory")
	repeat = flag.Int("repeat", 1, "Number of repeat times")
	audio  = flag.String("audio", "test_audio.wav", "Test audio file path")
)

type Config struct {
	Host     string `yaml:"host"`
	Endpoint string `yaml:"endpoint"`

	DoubaoAppID      string `yaml:"app_id"`
	DoubaoAccessKey  string `yaml:"access_key"`
	DoubaoResourceID string `yaml:"resource_id"`

	QwenAPIKey string    `yaml:"qwen_api_key"`
	QwenModel  string    `yaml:"qwen_model"`
	ModelType  ModelType `yaml:"model_type"` // 翻译模型类型
}

var (
	conf Config
)

func init() {
	conf.ModelType = ModelDoubao // 默认使用Doubao模型

	// 根据模型类型设置官方 Host 和 Endpoint
	conf.Host, conf.Endpoint = GetModelConfig(conf.ModelType)
	conf.DoubaoResourceID = defaultDoubaoResourceID
	conf.QwenModel = defaultQwenModel

	// 可选环境变量预填充，便于本地调试/CLI 使用；GUI 仍可直接修改。
	conf.DoubaoAppID = strings.TrimSpace(os.Getenv("DOUBAO_APP_ID"))
	conf.DoubaoAccessKey = strings.TrimSpace(os.Getenv("DOUBAO_ACCESS_KEY"))
	conf.QwenAPIKey = strings.TrimSpace(os.Getenv("QWEN_API_KEY"))
}

// setupBundledTools 设置打包在应用内的 ffmpeg/mpv 路径
func setupBundledTools() {
	// 获取可执行文件路径
	exePath, err := os.Executable()
	if err != nil {
		return
	}

	// 获取可执行文件所在目录
	exeDir := filepath.Dir(exePath)

	// 检查是否在 .app bundle 中 (MacOS 目录)
	if filepath.Base(exeDir) == "MacOS" {
		// 构建库目录路径 (mpv 的库在 MacOS/lib)
		libPath := filepath.Join(exeDir, "lib")

		// 将当前目录（包含 ffmpeg/mpv）添加到 PATH 最前面
		currentPath := os.Getenv("PATH")
		newPath := exeDir + string(filepath.ListSeparator) + currentPath
		os.Setenv("PATH", newPath)

		// 设置库路径
		if _, err := os.Stat(libPath); err == nil {
			dyldPath := os.Getenv("DYLD_LIBRARY_PATH")
			if dyldPath == "" {
				os.Setenv("DYLD_LIBRARY_PATH", libPath)
			} else {
				os.Setenv("DYLD_LIBRARY_PATH", libPath+string(filepath.ListSeparator)+dyldPath)
			}

			dyldFallbackPath := os.Getenv("DYLD_FALLBACK_LIBRARY_PATH")
			if dyldFallbackPath == "" {
				os.Setenv("DYLD_FALLBACK_LIBRARY_PATH", libPath)
			} else {
				os.Setenv("DYLD_FALLBACK_LIBRARY_PATH", libPath+string(filepath.ListSeparator)+dyldFallbackPath)
			}
		}

		log.Info("Set bundled tools path:", "path", exeDir)
	}
}

func main() {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// 设置打包的 ffmpeg/mpv 路径（用于 macOS .app bundle）
	setupBundledTools()

	// Check if GUI mode is requested
	if *target == "gui" {
		runGUI()
		return
	}

	// CLI mode
	for i := 0; i < *repeat; i++ {
		log.Info("\n====================== Running count ======================\n", "count", i+1)
		switch *target {
		case "ast":
			translateV4(conf, *audio, i)
		case "sts":
			streamSTSV4(conf)
		case "mic2vmic":
			physicalMicToVirtualMic(conf)
		case "vspeaker2pspeaker":
			virtualSpeakerToPhysicalSpeaker(conf)
		case "bidirectional":
			bidirectionalTranslation(conf)
		default:
			panic("Target not supported for v4: " + *target)
		}
	}
}

func runGUI() {
	application := app.New()
	application.Settings().SetTheme(&ModernTheme{})
	ui := NewTranslationUIWithApp(application, conf)
	ui.ShowAndRun()
}
