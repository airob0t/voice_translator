package main

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// TranslationMode represents the translation mode
type TranslationMode int

const (
	ModeMicrophone    TranslationMode = iota // 单麦克风模式 (物理麦→虚拟麦)
	ModeSpeaker                              // 单扬声器模式 (虚拟扬→物理扬)
	ModeBidirectional                        // 双向翻译模式
	ModeZH2Target                            // 本机中译英模式 (物理麦→物理扬)
	ModeSource2ZH                            // 本机英译中模式 (物理麦→物理扬)
)

type optionItem struct {
	Label string
	Code  string
}

var qwenLanguageOptions = []optionItem{
	{Label: "英语", Code: "en"},
	{Label: "中文", Code: "zh"},
	{Label: "俄语", Code: "ru"},
	{Label: "法语", Code: "fr"},
	{Label: "德语", Code: "de"},
	{Label: "葡萄牙语", Code: "pt"},
	{Label: "西班牙语", Code: "es"},
	{Label: "意大利语", Code: "it"},
	{Label: "韩语", Code: "ko"},
	{Label: "日语", Code: "ja"},
	{Label: "粤语", Code: "yue"},
}

func getOptionCode(options []optionItem, label string) string {
	for _, opt := range options {
		if opt.Label == label {
			return opt.Code
		}
	}
	return ""
}

const (
	defaultSourceLanguage = "zh"
	defaultTargetLanguage = "en"
	defaultVoiceCode      = "Cherry"
)

var fyneDo = fyne.Do

func defaultQwenLanguageLabelsForMode(mode TranslationMode) (string, string) {
	switch mode {
	case ModeSpeaker, ModeSource2ZH:
		return "英语", "中文"
	case ModeMicrophone, ModeBidirectional, ModeZH2Target:
		fallthrough
	default:
		return "中文", "英语"
	}
}

func (ui *TranslationUI) getSelectedSourceLanguage() string {
	if ui.selectedSourceLang != "" {
		return ui.selectedSourceLang
	}
	return defaultSourceLanguage
}

func (ui *TranslationUI) getSelectedTargetLanguage() string {
	if ui.selectedTargetLang != "" {
		return ui.selectedTargetLang
	}
	return defaultTargetLanguage
}

func (ui *TranslationUI) getSelectedVoice() string {
	if ui.selectedVoice != "" {
		return ui.selectedVoice
	}
	return defaultVoiceCode
}

func (ui *TranslationUI) getQwenSpeakerLanguagePair() (string, string) {
	source := ui.getSelectedSourceLanguage()
	target := ui.getSelectedTargetLanguage()
	if ui.mode == ModeBidirectional {
		return target, source
	}
	return source, target
}

func (ui *TranslationUI) applyQwenLanguageDefaultsForMode() {
	if ui.conf.ModelType != ModelQwen || ui.running {
		return
	}
	if ui.sourceLangSelect == nil || ui.targetLangSelect == nil {
		return
	}

	defaultSourceLabel, defaultTargetLabel := defaultQwenLanguageLabelsForMode(ui.mode)
	if ui.sourceLangSelect.Selected != defaultSourceLabel {
		ui.sourceLangSelect.SetSelected(defaultSourceLabel)
	}
	if ui.targetLangSelect.Selected != defaultTargetLabel {
		ui.targetLangSelect.SetSelected(defaultTargetLabel)
	}
}

var qwenVoiceOptions = []optionItem{
	{Label: "普通话女", Code: "Cherry"},
	{Label: "普通话男", Code: "Nofish"},
	{Label: "上海女", Code: "Jada"},
	{Label: "北京男", Code: "Dylan"},
	{Label: "四川女", Code: "Sunny"},
	{Label: "天津男", Code: "Peter"},
	{Label: "粤语女", Code: "Kiki"},
	{Label: "四川男", Code: "Eric"},
}

func (ui *TranslationUI) updateLanguageSelectorsState() {
	if ui.sourceLangRow == nil || ui.targetLangRow == nil || ui.voiceRow == nil || ui.doubaoAppIDRow == nil || ui.doubaoAccessKeyRow == nil || ui.qwenAPIKeyRow == nil {
		return
	}

	isQwen := ui.conf.ModelType == ModelQwen
	isEditable := !ui.running

	if isQwen {
		ui.doubaoAppIDRow.Hide()
		ui.doubaoAccessKeyRow.Hide()
		ui.qwenAPIKeyRow.Show()
		ui.sourceLangRow.Show()
		ui.targetLangRow.Show()
		ui.voiceRow.Show()

		if isEditable {
			ui.qwenAPIKeyEntry.Enable()
			ui.sourceLangSelect.Enable()
			ui.targetLangSelect.Enable()
			ui.voiceSelect.Enable()
		} else {
			ui.qwenAPIKeyEntry.Disable()
			ui.sourceLangSelect.Disable()
			ui.targetLangSelect.Disable()
			ui.voiceSelect.Disable()
		}
	} else {
		ui.doubaoAppIDRow.Show()
		ui.doubaoAccessKeyRow.Show()
		ui.qwenAPIKeyRow.Hide()
		ui.sourceLangRow.Hide()
		ui.targetLangRow.Hide()
		ui.voiceRow.Hide()

		if isEditable {
			ui.doubaoAppIDEntry.Enable()
			ui.doubaoAccessKeyEntry.Enable()
		} else {
			ui.doubaoAppIDEntry.Disable()
			ui.doubaoAccessKeyEntry.Disable()
		}
	}
	ui.doubaoAppIDRow.Refresh()
	ui.doubaoAccessKeyRow.Refresh()
	ui.qwenAPIKeyRow.Refresh()
	ui.sourceLangRow.Refresh()
	ui.targetLangRow.Refresh()
	ui.voiceRow.Refresh()
}

var (
	// Dark Theme Colors
	darkBackground      = color.NRGBA{0x0f, 0x17, 0x2a, 0xff}
	darkSurface         = color.NRGBA{0x18, 0x24, 0x3b, 0xff}
	darkSurfaceElevated = color.NRGBA{0x1f, 0x2b, 0x40, 0xff}
	darkPrimary         = color.NRGBA{0x3b, 0x82, 0xf6, 0xff}
	darkSuccess         = color.NRGBA{0x22, 0xc5, 0x5e, 0xff}
	darkWarning         = color.NRGBA{0xf4, 0x78, 0x03, 0xff}
	darkText            = color.NRGBA{0xf8, 0xfa, 0xfc, 0xff}
	darkSubtleText      = color.NRGBA{0x94, 0xa3, 0xb8, 0xff}

	// Light Theme Colors
	lightBackground      = color.NRGBA{0xff, 0xff, 0xff, 0xff}
	lightSurface         = color.NRGBA{0xf1, 0xf5, 0xf9, 0xff}
	lightSurfaceElevated = color.NRGBA{0xe2, 0xe8, 0xf0, 0xff}
	lightPrimary         = color.NRGBA{0x25, 0x63, 0xeb, 0xff}
	lightSuccess         = color.NRGBA{0x16, 0xa3, 0x4a, 0xff}
	lightWarning         = color.NRGBA{0xd9, 0x77, 0x06, 0xff}
	lightText            = color.NRGBA{0x0f, 0x17, 0x2a, 0xff}
	lightSubtleText      = color.NRGBA{0x64, 0x74, 0x8b, 0xff}
)

// ModernTheme customizes the application palette for a sleeker appearance.
type ModernTheme struct{}

// Color overrides key theme colors to provide a consistent palette.
// Color overrides key theme colors to provide a consistent palette.
func (ModernTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	isDark := variant == theme.VariantDark

	if isDark {
		switch name {
		case theme.ColorNameBackground:
			return darkBackground
		case theme.ColorNameForeground:
			return darkText
		case theme.ColorNamePrimary:
			return darkPrimary
		case theme.ColorNameButton:
			return darkPrimary
		case theme.ColorNameDisabled:
			return darkSubtleText
		case theme.ColorNameInputBackground:
			return darkSurface
		case theme.ColorNamePlaceHolder:
			return darkSubtleText
		case theme.ColorNameShadow:
			return color.NRGBA{0x00, 0x00, 0x00, 0x55}
		case theme.ColorNameFocus:
			return darkPrimary
		}
	} else {
		switch name {
		case theme.ColorNameBackground:
			return lightBackground
		case theme.ColorNameForeground:
			return lightText
		case theme.ColorNamePrimary:
			return lightPrimary
		case theme.ColorNameButton:
			return lightPrimary
		case theme.ColorNameDisabled:
			return lightSubtleText
		case theme.ColorNameInputBackground:
			return lightSurface
		case theme.ColorNamePlaceHolder:
			return lightSubtleText
		case theme.ColorNameShadow:
			return color.NRGBA{0x00, 0x00, 0x00, 0x33}
		case theme.ColorNameFocus:
			return lightPrimary
		}
	}

	return theme.DefaultTheme().Color(name, variant)
}

// Icon defers to the default theme icons.
func (ModernTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

// Font reuses the default theme fonts for compatibility.
func (ModernTheme) Font(style fyne.TextStyle) fyne.Resource {
	return theme.DefaultTheme().Font(style)
}

// Size tweaks default sizes for more spacious layouts.
func (ModernTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNamePadding:
		return theme.DefaultTheme().Size(name) + 2
	case theme.SizeNameText:
		return theme.DefaultTheme().Size(name) + 1
	case theme.SizeNameInnerPadding:
		return theme.DefaultTheme().Size(name) + 1
	default:
		return theme.DefaultTheme().Size(name)
	}
}

// buildSectionCard wraps content in a padded card to keep sections consistent.
func buildSectionCard(title, subtitle string, body fyne.CanvasObject) fyne.CanvasObject {
	return widget.NewCard(title, subtitle, container.NewPadded(body))
}

func newSeparatorLine(c color.Color) *canvas.Rectangle {
	line := canvas.NewRectangle(c)
	line.SetMinSize(fyne.NewSize(0, 2))
	return line
}

func buildAccentPanel(title string, accent color.Color, bg color.Color, separatorColor color.Color, content fyne.CanvasObject) fyne.CanvasObject {
	titleLabel := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	accentBar := canvas.NewRectangle(accent)
	accentBar.SetMinSize(fyne.NewSize(4, 0))
	background := canvas.NewRectangle(bg)
	headers := container.NewVBox(titleLabel, newSeparatorLine(separatorColor))
	contentArea := container.NewBorder(nil, nil, nil, nil, container.NewMax(content))
	body := container.NewBorder(headers, nil, nil, nil, container.NewPadded(contentArea))
	return container.NewMax(
		background,
		container.NewBorder(nil, nil, accentBar, nil, body),
	)
}

func buildFormRow(label string, content fyne.CanvasObject) fyne.CanvasObject {
	return container.NewVBox(
		widget.NewLabelWithStyle(label, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewPadded(content),
	)
}

func setSelectByExactLabel(selectWidget *widget.Select, options []string, wanted string) bool {
	if selectWidget == nil {
		return false
	}
	if wanted == "" {
		return false
	}
	for _, option := range options {
		if option == wanted {
			selectWidget.SetSelected(option)
			return true
		}
	}
	return false
}

// TranslationUI holds the UI components and state
type TranslationUI struct {
	app    fyne.App
	window fyne.Window

	// Device selections
	micSelect     *widget.Select
	speakerSelect *widget.Select

	// Mode selection
	modeSelect *widget.Select

	// Model selection
	modelSelect *widget.Select

	// Qwen language & voice selection
	sourceLangSelect *widget.Select
	targetLangSelect *widget.Select
	voiceSelect      *widget.Select

	// Credential inputs
	doubaoAppIDEntry     *widget.Entry
	doubaoAccessKeyEntry *widget.Entry
	qwenAPIKeyEntry      *widget.Entry

	// Layout containers for dynamic visibility
	sourceLangRow      fyne.CanvasObject
	targetLangRow      fyne.CanvasObject
	voiceRow           fyne.CanvasObject
	doubaoAppIDRow     fyne.CanvasObject
	doubaoAccessKeyRow fyne.CanvasObject
	qwenAPIKeyRow      fyne.CanvasObject

	// Control buttons
	startButton         *widget.Button
	stopButton          *widget.Button
	micToggleButton     *widget.Button
	speakerToggleButton *widget.Button

	// Status
	statusText   *widget.Entry
	statusString binding.String

	// Translation text display
	sourceText        *widget.RichText
	sourceString      binding.String
	sourceScroll      *container.Scroll
	translationText   *widget.RichText
	translationString binding.String
	translationScroll *container.Scroll
	statusMu          sync.Mutex
	sourceTextMu      sync.Mutex
	translationTextMu sync.Mutex
	statusBuffer      string
	sourceBuffer      string
	translationBuffer string

	// State
	mode               TranslationMode
	running            bool
	micRunning         bool
	speakerRunning     bool
	selectedMic        string
	selectedSpeaker    string
	selectedSourceLang string
	selectedTargetLang string
	selectedVoice      string

	// Control
	cancelFunc         context.CancelFunc
	micCancelFunc      context.CancelFunc
	speakerCancelFunc  context.CancelFunc
	mainContext        context.Context // 保存双向翻译模式的主 context
	runSerial          uint64
	activeRunSerial    uint64
	bidirectionalReady atomic.Bool
	mutex              sync.Mutex

	// Config
	conf Config
}

// NewTranslationUI creates a new translation UI (保留向后兼容)
func NewTranslationUI(conf Config) *TranslationUI {
	application := app.New()
	return NewTranslationUIWithApp(application, conf)
}

func NewTranslationUIWithApp(application fyne.App, conf Config) *TranslationUI {
	ui := &TranslationUI{
		app:  application,
		conf: conf,
	}
	ui.app.Settings().SetTheme(&ModernTheme{})
	ui.window = ui.app.NewWindow("星译语音翻译系统")
	return ui
}

// ShowAndRun displays the UI and runs the application
func (ui *TranslationUI) ShowAndRun() {
	ui.setupUI()

	ui.window.Resize(fyne.NewSize(1100, 700))

	ui.startAutotestLifecycleIfEnabled()

	ui.window.ShowAndRun()
}

// Show displays the UI without blocking (for use when app is already running)
func (ui *TranslationUI) Show() {
	ui.setupUI()

	ui.window.Resize(fyne.NewSize(1100, 700))

	ui.startAutotestLifecycleIfEnabled()

	ui.window.Show()
}

func (ui *TranslationUI) SetCloseIntercept(callback func()) {
	ui.window.SetCloseIntercept(callback)
}

func parsePositiveEnvInt(key string, defaultValue int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultValue
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return defaultValue
	}
	return value
}

func (ui *TranslationUI) startAutotestLifecycleIfEnabled() {
	if strings.TrimSpace(os.Getenv("XINGYI_AUTOTEST_AUTO_START")) != "1" {
		return
	}

	startDelaySeconds := parsePositiveEnvInt("XINGYI_AUTOTEST_START_DELAY_SECONDS", 3)
	stopAfterSeconds := parsePositiveEnvInt("XINGYI_AUTOTEST_STOP_AFTER_SECONDS", 20)
	quitAfterStop := strings.TrimSpace(os.Getenv("XINGYI_AUTOTEST_QUIT_AFTER_STOP")) == "1"

	go func() {
		time.Sleep(time.Duration(startDelaySeconds) * time.Second)
		ui.runOnUIThread(func() {
			ui.onStart()
		})

		if stopAfterSeconds <= 0 {
			return
		}

		time.Sleep(time.Duration(stopAfterSeconds) * time.Second)
		ui.runOnUIThread(func() {
			ui.onStop()
		})

		if quitAfterStop {
			time.Sleep(time.Second)
			ui.runOnUIThread(func() {
				ui.app.Quit()
			})
		}
	}()
}

// setupUI sets up the UI components
func (ui *TranslationUI) setupUI() {
	// Get available devices
	inputDevices := GetAudioInputDevices()
	outputDevices := GetAudioOutputDevices()

	// Create device name lists
	micNames := make([]string, 0, len(inputDevices))
	for _, dev := range inputDevices {
		micNames = append(micNames, dev.Name)
	}
	if len(micNames) == 0 {
		micNames = append(micNames, "无可用设备")
	}

	speakerNames := make([]string, 0, len(outputDevices))
	for _, dev := range outputDevices {
		speakerNames = append(speakerNames, dev.Name)
	}
	if len(speakerNames) == 0 {
		speakerNames = append(speakerNames, "无可用设备")
	}

	// Device selection
	ui.micSelect = widget.NewSelect(micNames, func(value string) {
		for _, dev := range inputDevices {
			if dev.Name == value {
				ui.selectedMic = dev.ID
				ui.addStatus(fmt.Sprintf("选择麦克风: %s (%s)", dev.Name, dev.ID))
				break
			}
		}
	})

	ui.speakerSelect = widget.NewSelect(speakerNames, func(value string) {
		for _, dev := range outputDevices {
			if dev.Name == value {
				ui.selectedSpeaker = dev.ID
				ui.addStatus(fmt.Sprintf("选择扬声器: %s (%s)", dev.Name, dev.ID))
				break
			}
		}
	})

	// Mode selection
	modes := []string{"单麦克风模式", "单扬声器模式", "双向翻译模式", "本机中译英模式", "本机英译中模式"}
	ui.modeSelect = widget.NewSelect(modes, func(value string) {
		switch value {
		case "单麦克风模式":
			ui.mode = ModeMicrophone
		case "单扬声器模式":
			ui.mode = ModeSpeaker
		case "双向翻译模式":
			ui.mode = ModeBidirectional
		case "本机中译英模式":
			ui.mode = ModeZH2Target
		case "本机英译中模式":
			ui.mode = ModeSource2ZH
		}
		ui.applyQwenLanguageDefaultsForMode()
		ui.addStatus(fmt.Sprintf("选择模式: %s", value))
		ui.updateControlButtons()
	})

	// Model selection
	models := []string{"模型一(低延迟和音色克隆)", "模型二(流畅多语言和音色选择)"}
	ui.modelSelect = widget.NewSelect(models, func(value string) {
		// 如果正在运行，需要先停止
		if ui.running {
			ui.addStatus("切换模型前需要先停止当前翻译")
			dialog.ShowInformation("提示", "切换模型前请先停止当前翻译", ui.window)
			// 恢复之前的选择
			if ui.conf.ModelType == ModelDoubao {
				ui.modelSelect.SetSelected("模型一(低延迟和音色克隆)")
			} else {
				ui.modelSelect.SetSelected("模型二(流畅多语言和音色选择)")
			}
			return
		}

		// 更新模型类型
		switch value {
		case "模型一(低延迟和音色克隆)":
			ui.conf.ModelType = ModelDoubao
		case "模型二(流畅多语言和音色选择)":
			ui.conf.ModelType = ModelQwen
		}

		// 更新官方 Host 和 Endpoint
		ui.conf.Host, ui.conf.Endpoint = GetModelConfig(ui.conf.ModelType)
		ui.addStatus(fmt.Sprintf("已切换到%s", value))
		ui.applyQwenLanguageDefaultsForMode()
		ui.updateLanguageSelectorsState()
	})

	// Language & voice selections (Model 2 only)
	languageLabels := make([]string, len(qwenLanguageOptions))
	for i, opt := range qwenLanguageOptions {
		languageLabels[i] = opt.Label
	}

	var targetLanguageLabels []string
	for _, label := range languageLabels {
		if label == "粤语" {
			continue
		}
		targetLanguageLabels = append(targetLanguageLabels, label)
	}
	voiceLabels := make([]string, len(qwenVoiceOptions))
	for i, opt := range qwenVoiceOptions {
		voiceLabels[i] = opt.Label
	}

	ui.sourceLangSelect = widget.NewSelect(languageLabels, func(value string) {
		if code := getOptionCode(qwenLanguageOptions, value); code != "" {
			ui.selectedSourceLang = code
			ui.addStatus(fmt.Sprintf("选择源语言: %s (%s)", value, code))
		}
	})
	ui.targetLangSelect = widget.NewSelect(targetLanguageLabels, func(value string) {
		if code := getOptionCode(qwenLanguageOptions, value); code != "" {
			ui.selectedTargetLang = code
			ui.addStatus(fmt.Sprintf("选择目标语言: %s (%s)", value, code))
		}
	})
	ui.voiceSelect = widget.NewSelect(voiceLabels, func(value string) {
		if code := getOptionCode(qwenVoiceOptions, value); code != "" {
			ui.selectedVoice = code
			ui.addStatus(fmt.Sprintf("选择音色: %s (%s)", value, code))
		}
	})

	ui.doubaoAppIDEntry = widget.NewEntry()
	ui.doubaoAppIDEntry.SetPlaceHolder("填写豆包 APP ID")
	ui.doubaoAppIDEntry.SetText(ui.conf.DoubaoAppID)
	ui.doubaoAppIDEntry.OnChanged = func(value string) {
		ui.conf.DoubaoAppID = strings.TrimSpace(value)
	}

	ui.doubaoAccessKeyEntry = widget.NewPasswordEntry()
	ui.doubaoAccessKeyEntry.SetPlaceHolder("填写豆包 Access Token")
	ui.doubaoAccessKeyEntry.SetText(ui.conf.DoubaoAccessKey)
	ui.doubaoAccessKeyEntry.OnChanged = func(value string) {
		ui.conf.DoubaoAccessKey = strings.TrimSpace(value)
	}

	ui.qwenAPIKeyEntry = widget.NewPasswordEntry()
	ui.qwenAPIKeyEntry.SetPlaceHolder("填写阿里云 DashScope API Key")
	ui.qwenAPIKeyEntry.SetText(ui.conf.QwenAPIKey)
	ui.qwenAPIKeyEntry.OnChanged = func(value string) {
		ui.conf.QwenAPIKey = strings.TrimSpace(value)
	}

	// Control buttons
	ui.startButton = widget.NewButton("启动", ui.onStart)
	ui.stopButton = widget.NewButton("停止", ui.onStop)
	ui.stopButton.Disable()

	ui.micToggleButton = widget.NewButton("停止麦克风", ui.onMicToggle)
	ui.micToggleButton.Hide()

	ui.speakerToggleButton = widget.NewButton("停止扬声器", ui.onSpeakerToggle)
	ui.speakerToggleButton.Hide()

	// Translation text display with better styling and colors
	ui.sourceString = binding.NewString()
	ui.sourceText = widget.NewRichTextFromMarkdown("")
	ui.sourceText.Wrapping = fyne.TextWrapWord

	ui.translationString = binding.NewString()
	ui.translationText = widget.NewRichTextFromMarkdown("")
	ui.translationText.Wrapping = fyne.TextWrapWord

	// Status display with monospace font for better log readability
	// Use data binding for thread-safe UI updates
	ui.statusString = binding.NewString()
	ui.statusText = widget.NewMultiLineEntry()
	ui.statusText.Bind(ui.statusString)
	ui.statusText.Disable()
	ui.statusText.SetMinRowsVisible(3)
	ui.statusText.Wrapping = fyne.TextWrapWord
	ui.statusText.TextStyle = fyne.TextStyle{Monospace: true}

	// Create scroll containers and save references
	ui.sourceScroll = container.NewScroll(ui.sourceText)
	ui.translationScroll = container.NewScroll(ui.translationText)

	// Create layout rows and save references
	ui.doubaoAppIDRow = buildFormRow("豆包 APP ID（模型一）", ui.doubaoAppIDEntry)
	ui.doubaoAccessKeyRow = buildFormRow("豆包 Access Token（模型一）", ui.doubaoAccessKeyEntry)
	ui.qwenAPIKeyRow = buildFormRow("DashScope API Key（模型二）", ui.qwenAPIKeyEntry)
	ui.sourceLangRow = buildFormRow("源语言（模型二）", ui.sourceLangSelect)
	ui.targetLangRow = buildFormRow("目标语言（模型二）", ui.targetLangSelect)
	ui.voiceRow = buildFormRow("音色（模型二）", ui.voiceSelect)

	// Get current theme colors for initial rendering
	variant := ui.app.Settings().ThemeVariant()
	var currentSurfaceElevated, currentSeparator, currentPrimary, currentSuccess, currentWarning color.Color

	if variant == theme.VariantLight {
		currentSurfaceElevated = lightSurfaceElevated
		currentSeparator = color.NRGBA{0xe2, 0xe8, 0xf0, 0xff} // Light separator
		currentPrimary = lightPrimary
		currentSuccess = lightSuccess
		currentWarning = lightWarning
	} else {
		currentSurfaceElevated = darkSurfaceElevated
		currentSeparator = color.NRGBA{0x33, 0x44, 0x5c, 0xff} // Dark separator
		currentPrimary = darkPrimary
		currentSuccess = darkSuccess
		currentWarning = darkWarning
	}

	translationSection := container.NewGridWithColumns(2,
		buildAccentPanel("📝 原文", currentPrimary, currentSurfaceElevated, currentSeparator, ui.sourceScroll),
		buildAccentPanel("🌐 译文", currentSuccess, currentSurfaceElevated, currentSeparator, ui.translationScroll),
	)

	statusSection := buildAccentPanel("📊 状态日志", currentWarning, currentSurfaceElevated, currentSeparator, container.NewScroll(ui.statusText))

	// Primary Actions (Start/Stop) - Prominent at the top
	primaryActions := container.NewGridWithColumns(2,
		ui.startButton,
		ui.stopButton,
	)

	// Secondary Actions (Credit, Logout, Mic Toggle, etc)
	secondaryActions := container.NewVBox(
		ui.micToggleButton,
		ui.speakerToggleButton,
	)

	// Sidebar Layout - Clean and Flat
	sidebarContent := container.NewVBox(
		widget.NewLabelWithStyle("控制中心", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		primaryActions,
		newSeparatorLine(currentSeparator),

		widget.NewLabelWithStyle("设备与设置", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		buildFormRow("麦克风", ui.micSelect),
		buildFormRow("扬声器", ui.speakerSelect),
		newSeparatorLine(currentSeparator),

		buildFormRow("翻译模式", ui.modeSelect),
		buildFormRow("翻译模型", ui.modelSelect),

		// Dynamic rows
		ui.doubaoAppIDRow,
		ui.doubaoAccessKeyRow,
		ui.qwenAPIKeyRow,
		ui.sourceLangRow,
		ui.targetLangRow,
		ui.voiceRow,

		newSeparatorLine(currentSeparator),
		secondaryActions,
	)

	// Wrap sidebar in a scroll container
	sidebarScroll := container.NewVScroll(container.NewPadded(sidebarContent))

	// Main content contains Translation (Left) and Logs (Right)
	mainContent := container.NewHSplit(
		translationSection,
		statusSection,
	)
	mainContent.SetOffset(0.75) // Translation takes 75% width of the main area

	// Overall layout: Sidebar (Left) | Main Content (Right)
	body := container.NewHSplit(sidebarScroll, mainContent)
	body.SetOffset(0.2) // Sidebar takes 20% width

	// Remove the manual background rectangle so the window background (set by theme) shows through.
	content := container.NewPadded(body)

	ui.window.SetContent(content)
	ui.addStatus("系统已初始化")

	// Set default selections after UI is fully initialized.
	// For automation/reproducible testing, allow selecting exact device labels by env.
	preferredMic := strings.TrimSpace(os.Getenv("XINGYI_MIC_DEVICE_NAME"))
	preferredSpeaker := strings.TrimSpace(os.Getenv("XINGYI_SPEAKER_DEVICE_NAME"))
	if preferredMic == "" {
		preferredMic = strings.TrimSpace(os.Getenv("XINGYI_AUDIO_DEVICE_NAME"))
	}
	if preferredSpeaker == "" {
		preferredSpeaker = strings.TrimSpace(os.Getenv("XINGYI_AUDIO_DEVICE_NAME"))
	}

	if len(micNames) > 0 {
		if !setSelectByExactLabel(ui.micSelect, micNames, preferredMic) {
			ui.micSelect.SetSelected(micNames[0])
			if preferredMic != "" {
				ui.addStatus(fmt.Sprintf("未找到指定麦克风设备，回退默认: %s", preferredMic))
			}
		}
	}
	if len(speakerNames) > 0 {
		if !setSelectByExactLabel(ui.speakerSelect, speakerNames, preferredSpeaker) {
			ui.speakerSelect.SetSelected(speakerNames[0])
			if preferredSpeaker != "" {
				ui.addStatus(fmt.Sprintf("未找到指定扬声器设备，回退默认: %s", preferredSpeaker))
			}
		}
	}
	ui.modeSelect.SetSelected("单麦克风模式")

	// Set default model selection
	if ui.conf.ModelType == ModelDoubao {
		ui.modelSelect.SetSelected("模型一(低延迟和音色克隆)")
	} else {
		ui.modelSelect.SetSelected("模型二(流畅多语言和音色选择)")
	}

	ui.sourceLangSelect.SetSelected("中文")
	ui.targetLangSelect.SetSelected("英语")
	ui.voiceSelect.SetSelected("普通话男")
	ui.applyQwenLanguageDefaultsForMode()
	ui.updateLanguageSelectorsState()
	ui.addStatus("当前版本为本机直连模式：不做版本检查、不登录、不计费，密钥仅保存在当前进程内存中")
}

// updateControlButtons updates the visibility of control buttons based on mode
func (ui *TranslationUI) updateControlButtons() {
	if ui.shouldShowBidirectionalControls() {
		ui.micToggleButton.Show()
		ui.speakerToggleButton.Show()
	} else {
		ui.micToggleButton.Hide()
		ui.speakerToggleButton.Hide()
	}
}

func (ui *TranslationUI) shouldShowBidirectionalControls() bool {
	return ui.mode == ModeBidirectional && ui.running && ui.bidirectionalReady.Load()
}

func (ui *TranslationUI) prepareBidirectionalControlsForStart() {
	if ui.micToggleButton != nil {
		ui.micToggleButton.SetText("停止麦克风")
	}
	if ui.speakerToggleButton != nil {
		ui.speakerToggleButton.SetText("停止扬声器")
	}
}

func (ui *TranslationUI) shouldAbortRun(ctx context.Context, runSerial uint64) bool {
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	ui.mutex.Lock()
	defer ui.mutex.Unlock()
	return ui.activeRunSerial != runSerial
}

func (ui *TranslationUI) finalizeRunIfCurrent(runSerial uint64, bidirectional bool) bool {
	ui.mutex.Lock()
	defer ui.mutex.Unlock()

	if ui.activeRunSerial != runSerial {
		return false
	}

	ui.running = false
	ui.cancelFunc = nil
	if bidirectional {
		ui.micRunning = false
		ui.speakerRunning = false
		ui.mainContext = nil
		ui.micCancelFunc = nil
		ui.speakerCancelFunc = nil
		ui.bidirectionalReady.Store(false)
	}
	return true
}

func (ui *TranslationUI) syncCredentialConfig() {
	ui.conf.DoubaoAppID = strings.TrimSpace(ui.doubaoAppIDEntry.Text)
	ui.conf.DoubaoAccessKey = strings.TrimSpace(ui.doubaoAccessKeyEntry.Text)
	ui.conf.QwenAPIKey = strings.TrimSpace(ui.qwenAPIKeyEntry.Text)
	ui.conf.Host, ui.conf.Endpoint = GetModelConfig(ui.conf.ModelType)
}

func (ui *TranslationUI) validateModelCredentials() error {
	ui.syncCredentialConfig()

	switch ui.conf.ModelType {
	case ModelDoubao:
		if ui.conf.DoubaoAppID == "" {
			return fmt.Errorf("请填写豆包 APP ID")
		}
		if ui.conf.DoubaoAccessKey == "" {
			return fmt.Errorf("请填写豆包 Access Token")
		}
	case ModelQwen:
		if ui.conf.QwenAPIKey == "" {
			return fmt.Errorf("请填写 DashScope API Key")
		}
	default:
		return fmt.Errorf("未知的模型类型: %v", ui.conf.ModelType)
	}

	return nil
}

// onStart handles the start button click
func (ui *TranslationUI) onStart() {
	ui.mutex.Lock()
	defer ui.mutex.Unlock()

	if ui.running {
		return
	}

	// Validate device selections
	if ui.selectedMic == "" && (ui.mode == ModeMicrophone || ui.mode == ModeBidirectional || ui.mode == ModeZH2Target || ui.mode == ModeSource2ZH) {
		dialog.ShowError(fmt.Errorf("请选择麦克风设备"), ui.window)
		return
	}
	if ui.selectedSpeaker == "" && (ui.mode == ModeSpeaker || ui.mode == ModeBidirectional || ui.mode == ModeZH2Target || ui.mode == ModeSource2ZH) {
		dialog.ShowError(fmt.Errorf("请选择扬声器设备"), ui.window)
		return
	}
	if err := ui.validateModelCredentials(); err != nil {
		dialog.ShowError(err, ui.window)
		return
	}

	ui.running = true
	ui.startButton.Disable()
	ui.stopButton.Enable()
	ui.micSelect.Disable()
	ui.speakerSelect.Disable()
	ui.modeSelect.Disable()
	ui.modelSelect.Disable()
	ui.sourceLangSelect.Disable()
	ui.targetLangSelect.Disable()
	ui.voiceSelect.Disable()

	ctx, cancel := context.WithCancel(context.Background())
	ui.runSerial++
	runSerial := ui.runSerial
	ui.activeRunSerial = runSerial
	ui.bidirectionalReady.Store(false)
	ui.cancelFunc = cancel
	ui.updateLanguageSelectorsState()

	switch ui.mode {
	case ModeMicrophone:
		ui.addStatus("启动单麦克风模式...")
		go ui.runMicrophoneMode(ctx, runSerial)
	case ModeSpeaker:
		ui.addStatus("启动单扬声器模式...")
		go ui.runSpeakerMode(ctx, runSerial)
	case ModeBidirectional:
		ui.addStatus("启动双向翻译模式...")
		ui.micRunning = true
		ui.speakerRunning = true
		ui.prepareBidirectionalControlsForStart()
		ui.updateControlButtons()
		go ui.runBidirectionalMode(ctx, runSerial)
	case ModeZH2Target:
		ui.addStatus("启动本机中译英模式...")
		go ui.runTestMode(ctx, runSerial)
	case ModeSource2ZH:
		ui.addStatus("启动本机英译中模式...")
		go ui.runLocalSource2ZHMode(ctx, runSerial)
	}
}

// onStop handles the stop button click
func (ui *TranslationUI) onStop() {
	ui.mutex.Lock()
	defer ui.mutex.Unlock()

	if !ui.running {
		return
	}

	ui.addStatus("停止翻译...")
	if ui.cancelFunc != nil {
		ui.cancelFunc()
		ui.cancelFunc = nil
	}
	if ui.micCancelFunc != nil {
		ui.micCancelFunc()
		ui.micCancelFunc = nil
	}
	if ui.speakerCancelFunc != nil {
		ui.speakerCancelFunc()
		ui.speakerCancelFunc = nil
	}
	ui.mainContext = nil
	ui.bidirectionalReady.Store(false)
	ui.runSerial++
	ui.activeRunSerial = ui.runSerial

	ui.running = false
	ui.micRunning = false
	ui.speakerRunning = false
	ui.startButton.Enable()
	ui.stopButton.Disable()
	ui.micSelect.Enable()
	ui.speakerSelect.Enable()
	ui.modeSelect.Enable()
	ui.modelSelect.Enable()
	ui.updateLanguageSelectorsState()
	ui.updateControlButtons()
}

// onMicToggle handles the microphone toggle button click
func (ui *TranslationUI) onMicToggle() {
	ui.mutex.Lock()
	defer ui.mutex.Unlock()

	if ui.mode == ModeBidirectional && !ui.bidirectionalReady.Load() {
		ui.addStatus("双向组件初始化中，请稍候")
		return
	}

	if ui.micRunning {
		ui.addStatus("停止麦克风...")
		if ui.micCancelFunc != nil {
			ui.micCancelFunc()
		}
		ui.micRunning = false
		ui.micToggleButton.SetText("启动麦克风")
	} else {
		ui.addStatus("启动麦克风...")
		// 如果在双向翻译模式下，从主 context 创建子 context
		if ui.mainContext == nil {
			ui.addStatus("双向主会话未就绪，无法启动麦克风")
			return
		}
		ctx, cancel := context.WithCancel(ui.mainContext)
		ui.micCancelFunc = cancel
		ui.micRunning = true
		ui.micToggleButton.SetText("停止麦克风")
		go ui.runMicrophoneComponent(ctx)
	}
}

// onSpeakerToggle handles the speaker toggle button click
func (ui *TranslationUI) onSpeakerToggle() {
	ui.mutex.Lock()
	defer ui.mutex.Unlock()

	if ui.mode == ModeBidirectional && !ui.bidirectionalReady.Load() {
		ui.addStatus("双向组件初始化中，请稍候")
		return
	}

	if ui.speakerRunning {
		ui.addStatus("停止扬声器...")
		if ui.speakerCancelFunc != nil {
			ui.speakerCancelFunc()
		}
		ui.speakerRunning = false
		ui.speakerToggleButton.SetText("启动扬声器")
	} else {
		ui.addStatus("启动扬声器...")
		// 如果在双向翻译模式下，从主 context 创建子 context
		if ui.mainContext == nil {
			ui.addStatus("双向主会话未就绪，无法启动扬声器")
			return
		}
		ctx, cancel := context.WithCancel(ui.mainContext)
		ui.speakerCancelFunc = cancel
		ui.speakerRunning = true
		ui.speakerToggleButton.SetText("停止扬声器")
		go ui.runSpeakerComponent(ctx)
	}
}

// addStatus adds a status message to the status display
// This function is thread-safe and can be called from any goroutine
func (ui *TranslationUI) addStatus(message string) {
	sanitizedMessage := sanitizeUserVisibleLog(message)
	safeInfo(sanitizedMessage)

	// Safety check in case statusString is not initialized yet
	if ui.statusString == nil {
		return
	}

	ui.statusMu.Lock()
	defer ui.statusMu.Unlock()

	if ui.statusBuffer != "" {
		ui.statusBuffer += "\n"
	}
	ui.statusBuffer += sanitizedMessage
	if err := ui.statusString.Set(ui.statusBuffer); err != nil {
		safeWarningf("Failed to update status binding: %v", err)
	}
}

// runOnUIThread ensures the provided function executes on the UI thread.
func (ui *TranslationUI) runOnUIThread(fn func()) {
	if fn == nil {
		return
	}
	fyneDo(fn)
}

// appendSourceText appends text to the source text display
// This function is thread-safe and can be called from any goroutine
func (ui *TranslationUI) appendSourceText(text string) {
	if ui.sourceText == nil || ui.sourceString == nil {
		return
	}

	ui.sourceTextMu.Lock()
	if ui.sourceBuffer != "" {
		ui.sourceBuffer += "\n"
	}
	ui.sourceBuffer += text
	current := ui.sourceBuffer
	if err := ui.sourceString.Set(current); err != nil {
		safeWarningf("Failed to update source binding: %v", err)
	}
	ui.sourceTextMu.Unlock()

	// Update RichText content - must be done on UI thread
	ui.runOnUIThread(func() {
		ui.sourceText.ParseMarkdown(current)

		// Auto-scroll to bottom
		if ui.sourceScroll != nil {
			ui.sourceScroll.ScrollToBottom()
		}
	})
}

// appendTranslationText appends text to the translation text display with color
// This function is thread-safe and can be called from any goroutine
func (ui *TranslationUI) appendTranslationText(text string) {
	if ui.translationText == nil || ui.translationString == nil {
		return
	}

	ui.translationTextMu.Lock()
	if ui.translationBuffer != "" {
		ui.translationBuffer += "\n"
	}
	ui.translationBuffer += text
	current := ui.translationBuffer
	if err := ui.translationString.Set(current); err != nil {
		safeWarningf("Failed to update translation binding: %v", err)
	}
	ui.translationTextMu.Unlock()

	// Update RichText content - must be done on UI thread
	ui.runOnUIThread(func() {
		ui.translationText.ParseMarkdown(current)

		// Auto-scroll to bottom
		if ui.translationScroll != nil {
			ui.translationScroll.ScrollToBottom()
		}
	})
}

// runMicrophoneMode runs the single microphone mode (physical mic -> virtual mic)
func (ui *TranslationUI) runMicrophoneMode(ctx context.Context, runSerial uint64) {
	if ui.shouldAbortRun(ctx, runSerial) {
		return
	}

	defer func() {
		if !ui.finalizeRunIfCurrent(runSerial, false) {
			return
		}

		fyne.Do(func() {
			ui.startButton.Enable()
			ui.stopButton.Disable()
			ui.micSelect.Enable()
			ui.speakerSelect.Enable()
			ui.modeSelect.Enable()
			ui.modelSelect.Enable()
			ui.updateLanguageSelectorsState()
		})

		ui.addStatus("单麦克风模式已停止")
	}()

	ui.addStatus("单麦克风模式运行中...")
	// Physical mic -> virtual mic (translation output to virtual microphone)
	// Create callback for text updates
	textCallback := func(sourceText, translationText string) {
		if sourceText != "" {
			ui.appendSourceText(sourceText)
		}
		if translationText != "" {
			ui.appendTranslationText(translationText)
		}
	}
	// Create callback for error notifications
	errorCallback := func(err error) {
		// 如果是 context 取消（用户主动停止），只记录日志不弹窗
		if errors.Is(err, context.Canceled) {
			ui.addStatus("✓ 麦克风翻译已停止")
			return
		}
		// 真正的错误才显示详细信息
		ui.addStatus(fmt.Sprintf("❌ 麦克风翻译服务中断: %v", err))
		fyne.Do(func() {
			dialog.ShowError(fmt.Errorf("麦克风翻译服务已中断\n\n可能原因：\n• 网络连接中断\n• 翻译额度已用完\n• 服务器暂时不可用\n\n解决方案：\n1. 检查网络连接是否正常\n2. 点击'兑换码'按钮查看剩余额度\n3. 如问题持续，请联系技术支持"), ui.window)
		})
	}

	// 根据模型类型选择使用不同的实现
	if ui.shouldAbortRun(ctx, runSerial) {
		return
	}
	switch ui.conf.ModelType {
	case ModelDoubao:
		ui.addStatus("使用模型一 (Protobuf协议)")
		physicalMicToVirtualMicWithDevicesAndCallback(ui.conf, ui.selectedMic, ctx, textCallback, errorCallback)
	case ModelQwen:
		ui.addStatus("使用模型二 (JSON协议)")
		qwenPhysicalMicToVirtualMic(ui.conf, ui.selectedMic, ui.getSelectedSourceLanguage(), ui.getSelectedTargetLanguage(), ui.getSelectedVoice(), ctx, textCallback, errorCallback)
	default:
		ui.addStatus(fmt.Sprintf("未知的模型类型: %v", ui.conf.ModelType))
	}
}

func (ui *TranslationUI) runSpeakerMode(ctx context.Context, runSerial uint64) {
	if ui.shouldAbortRun(ctx, runSerial) {
		return
	}

	defer func() {
		if !ui.finalizeRunIfCurrent(runSerial, false) {
			return
		}

		fyne.Do(func() {
			ui.startButton.Enable()
			ui.stopButton.Disable()
			ui.micSelect.Enable()
			ui.speakerSelect.Enable()
			ui.modeSelect.Enable()
			ui.modelSelect.Enable()
		})

		ui.addStatus("单扬声器模式已停止")
	}()

	ui.addStatus("单扬声器模式运行中...")
	// Create callback for text updates
	textCallback := func(sourceText, translationText string) {
		if sourceText != "" {
			ui.appendSourceText(sourceText)
		}
		if translationText != "" {
			ui.appendTranslationText(translationText)
		}
	}
	// Create callback for error notifications
	errorCallback := func(err error) {
		// 如果是 context 取消（用户主动停止），只记录日志不弹窗
		if errors.Is(err, context.Canceled) {
			ui.addStatus("✓ 扬声器翻译已停止")
			return
		}
		// 真正的错误才显示详细信息
		ui.addStatus(fmt.Sprintf("❌ 扬声器翻译服务中断: %v", err))
		fyne.Do(func() {
			dialog.ShowError(fmt.Errorf("扬声器翻译服务已中断\n\n可能原因：\n• 网络连接中断\n• 翻译额度已用完\n• 服务器暂时不可用\n\n解决方案：\n1. 检查网络连接是否正常\n2. 点击'兑换码'按钮查看剩余额度\n3. 确认扬声器设备正常工作\n4. 如问题持续，请联系技术支持"), ui.window)
		})
	}

	// 根据模型类型选择使用不同的实现
	if ui.shouldAbortRun(ctx, runSerial) {
		return
	}
	switch ui.conf.ModelType {
	case ModelDoubao:
		ui.addStatus("使用模型一 (Protobuf协议)")
		virtualSpeakerToPhysicalSpeakerWithDevicesAndCallback(ui.conf, ui.selectedSpeaker, ctx, textCallback, errorCallback)
	case ModelQwen:
		ui.addStatus("使用模型二 (JSON协议)")
		speakerSource, speakerTarget := ui.getQwenSpeakerLanguagePair()
		qwenVirtualSpeakerToPhysicalSpeaker(ui.conf, ui.selectedSpeaker, speakerSource, speakerTarget, ui.getSelectedVoice(), ctx, textCallback, errorCallback)
	default:
		ui.addStatus(fmt.Sprintf("未知的模型类型: %v", ui.conf.ModelType))
	}
}

// runTestMode runs the test mode (physical mic -> physical speaker)
func (ui *TranslationUI) runTestMode(ctx context.Context, runSerial uint64) {
	if ui.shouldAbortRun(ctx, runSerial) {
		return
	}

	defer func() {
		if !ui.finalizeRunIfCurrent(runSerial, false) {
			return
		}

		fyne.Do(func() {
			ui.startButton.Enable()
			ui.stopButton.Disable()
			ui.micSelect.Enable()
			ui.speakerSelect.Enable()
			ui.modeSelect.Enable()
			ui.modelSelect.Enable()
		})

		ui.addStatus("本机中译英模式已停止")
	}()

	ui.addStatus("本机中译英模式运行中...")
	// Create callback for text updates
	textCallback := func(sourceText, translationText string) {
		if sourceText != "" {
			ui.appendSourceText(sourceText)
		}
		if translationText != "" {
			ui.appendTranslationText(translationText)
		}
	}
	// Create callback for error notifications
	errorCallback := func(err error) {
		// 如果是 context 取消（用户主动停止），只记录日志不弹窗
		if errors.Is(err, context.Canceled) {
			ui.addStatus("✓ 中译英翻译已停止")
			return
		}
		// 真正的错误才显示详细信息
		ui.addStatus(fmt.Sprintf("❌ 中译英翻译服务中断: %v", err))
		fyne.Do(func() {
			dialog.ShowError(fmt.Errorf("中译英翻译服务已中断\n\n可能原因：\n• 网络连接中断\n• 翻译额度已用完\n• 服务器暂时不可用\n\n解决方案：\n1. 检查网络连接是否正常\n2. 点击'兑换码'按钮查看剩余额度\n3. 确认麦克风和扬声器设备正常工作\n4. 如问题持续，请联系技术支持"), ui.window)
		})
	}

	// 根据模型类型选择使用不同的实现
	if ui.shouldAbortRun(ctx, runSerial) {
		return
	}
	switch ui.conf.ModelType {
	case ModelDoubao:
		ui.addStatus("使用模型一 (Protobuf协议)")
		streamSTSV4WithDevicesAndLanguages(ui.conf, ui.selectedMic, ui.selectedSpeaker, "zh", "en", ctx, textCallback, errorCallback)
	case ModelQwen:
		ui.addStatus("使用模型二 (JSON协议)")
		qwenStreamSTSWithDevicesAndLanguages(ui.conf, ui.selectedMic, ui.selectedSpeaker, ui.getSelectedSourceLanguage(), ui.getSelectedTargetLanguage(), ui.getSelectedVoice(), ctx, textCallback, errorCallback)
	default:
		ui.addStatus(fmt.Sprintf("未知的模型类型: %v", ui.conf.ModelType))
	}
}

// runLocalSource2ZHMode runs the local EN->ZH mode (physical mic -> physical speaker)
func (ui *TranslationUI) runLocalSource2ZHMode(ctx context.Context, runSerial uint64) {
	if ui.shouldAbortRun(ctx, runSerial) {
		return
	}

	defer func() {
		if !ui.finalizeRunIfCurrent(runSerial, false) {
			return
		}

		fyne.Do(func() {
			ui.startButton.Enable()
			ui.stopButton.Disable()
			ui.micSelect.Enable()
			ui.speakerSelect.Enable()
			ui.modeSelect.Enable()
			ui.modelSelect.Enable()
		})

		ui.addStatus("本机英译中模式已停止")
	}()

	ui.addStatus("本机英译中模式运行中...")
	// Create callback for text updates
	textCallback := func(sourceText, translationText string) {
		if sourceText != "" {
			ui.appendSourceText(sourceText)
		}
		if translationText != "" {
			ui.appendTranslationText(translationText)
		}
	}
	// Create callback for error notifications
	errorCallback := func(err error) {
		// 如果是 context 取消（用户主动停止），只记录日志不弹窗
		if errors.Is(err, context.Canceled) {
			ui.addStatus("✓ 英译中翻译已停止")
			return
		}
		// 真正的错误才显示详细信息
		ui.addStatus(fmt.Sprintf("❌ 英译中翻译服务中断: %v", err))
		fyne.Do(func() {
			dialog.ShowError(fmt.Errorf("英译中翻译服务已中断\n\n可能原因：\n• 网络连接中断\n• 翻译额度已用完\n• 服务器暂时不可用\n\n解决方案：\n1. 检查网络连接是否正常\n2. 点击'兑换码'按钮查看剩余额度\n3. 确认麦克风和扬声器设备正常工作\n4. 如问题持续，请联系技术支持"), ui.window)
		})
	}

	// 根据模型类型选择使用不同的实现
	if ui.shouldAbortRun(ctx, runSerial) {
		return
	}
	switch ui.conf.ModelType {
	case ModelDoubao:
		ui.addStatus("使用模型一 (Protobuf协议)")
		streamSTSV4WithDevicesAndLanguages(ui.conf, ui.selectedMic, ui.selectedSpeaker, "en", "zh", ctx, textCallback, errorCallback)
	case ModelQwen:
		ui.addStatus("使用模型二 (JSON协议)")
		qwenStreamSTSWithDevicesAndLanguages(ui.conf, ui.selectedMic, ui.selectedSpeaker, ui.getSelectedSourceLanguage(), ui.getSelectedTargetLanguage(), ui.getSelectedVoice(), ctx, textCallback, errorCallback)
	default:
		ui.addStatus(fmt.Sprintf("未知的模型类型: %v", ui.conf.ModelType))
	}
}

// runBidirectionalMode runs the bidirectional translation mode
func (ui *TranslationUI) runBidirectionalMode(ctx context.Context, runSerial uint64) {
	if ui.shouldAbortRun(ctx, runSerial) {
		return
	}

	defer func() {
		if !ui.finalizeRunIfCurrent(runSerial, true) {
			return
		}

		fyne.Do(func() {
			ui.startButton.Enable()
			ui.stopButton.Disable()
			ui.micSelect.Enable()
			ui.speakerSelect.Enable()
			ui.modeSelect.Enable()
			ui.modelSelect.Enable()
			ui.updateControlButtons()
			ui.updateLanguageSelectorsState()
		})

		ui.addStatus("双向翻译模式已停止")
	}()

	ui.addStatus("双向翻译模式运行中...")
	if ui.shouldAbortRun(ctx, runSerial) {
		return
	}

	// Create separate contexts for mic and speaker
	micCtx, micCancel := context.WithCancel(ctx)
	speakerCtx, speakerCancel := context.WithCancel(ctx)

	ui.mutex.Lock()
	if ui.activeRunSerial != runSerial {
		ui.mutex.Unlock()
		micCancel()
		speakerCancel()
		return
	}
	ui.mainContext = ctx
	ui.micCancelFunc = micCancel
	ui.speakerCancelFunc = speakerCancel
	ui.mutex.Unlock()
	ui.bidirectionalReady.Store(true)
	ui.runOnUIThread(func() {
		ui.updateControlButtons()
	})

	var wg sync.WaitGroup
	wg.Add(2)

	// Run microphone component
	go func() {
		defer wg.Done()
		ui.runMicrophoneComponent(micCtx)
	}()

	// Run speaker component
	go func() {
		defer wg.Done()
		ui.runSpeakerComponent(speakerCtx)
	}()

	wg.Wait()
}

// runMicrophoneComponent runs the microphone component (physical mic -> virtual mic)
func (ui *TranslationUI) runMicrophoneComponent(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	ui.addStatus("麦克风组件启动...")
	// Create callback for text updates
	textCallback := func(sourceText, translationText string) {
		if sourceText != "" {
			ui.appendSourceText(sourceText)
		}
		if translationText != "" {
			ui.appendTranslationText(translationText)
		}
	}
	// Create callback for error notifications
	errorCallback := func(err error) {
		// 如果是 context 取消（用户主动停止），只记录日志不弹窗
		if errors.Is(err, context.Canceled) {
			ui.addStatus("✓ 麦克风组件已停止")
			return
		}
		// 真正的错误才显示详细信息
		ui.addStatus(fmt.Sprintf("❌ 麦克风翻译组件中断: %v", err))
		fyne.Do(func() {
			dialog.ShowError(fmt.Errorf("麦克风翻译组件已中断\n\n可能原因：\n• 网络连接中断\n• 翻译额度已用完\n• 麦克风设备异常\n\n解决方案：\n1. 检查网络连接是否正常\n2. 点击'兑换码'按钮查看剩余额度\n3. 检查麦克风权限和设备连接\n4. 可以尝试重新启动麦克风组件\n5. 如问题持续，请联系技术支持"), ui.window)
		})
	}

	// 根据模型类型选择使用不同的实现
	switch ui.conf.ModelType {
	case ModelDoubao:
		physicalMicToVirtualMicWithDevicesAndCallback(ui.conf, ui.selectedMic, ctx, textCallback, errorCallback)
	case ModelQwen:
		qwenPhysicalMicToVirtualMic(ui.conf, ui.selectedMic, ui.getSelectedSourceLanguage(), ui.getSelectedTargetLanguage(), ui.getSelectedVoice(), ctx, textCallback, errorCallback)
	}
	ui.addStatus("麦克风组件停止")
}

// runSpeakerComponent runs the speaker component (virtual speaker -> physical speaker)
func (ui *TranslationUI) runSpeakerComponent(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	ui.addStatus("扬声器组件启动...")
	// Create callback for text updates
	textCallback := func(sourceText, translationText string) {
		if sourceText != "" {
			ui.appendSourceText(sourceText)
		}
		if translationText != "" {
			ui.appendTranslationText(translationText)
		}
	}
	// Create callback for error notifications
	errorCallback := func(err error) {
		// 如果是 context 取消（用户主动停止），只记录日志不弹窗
		if errors.Is(err, context.Canceled) {
			ui.addStatus("✓ 扬声器组件已停止")
			return
		}
		// 真正的错误才显示详细信息
		ui.addStatus(fmt.Sprintf("❌ 扬声器翻译组件中断: %v", err))
		fyne.Do(func() {
			dialog.ShowError(fmt.Errorf("扬声器翻译组件已中断\n\n可能原因：\n• 网络连接中断\n• 翻译额度已用完\n• 扬声器设备异常\n\n解决方案：\n1. 检查网络连接是否正常\n2. 点击'兑换码'按钮查看剩余额度\n3. 检查扬声器设备连接\n4. 可以尝试重新启动扬声器组件\n5. 如问题持续，请联系技术支持"), ui.window)
		})
	}

	// 根据模型类型选择使用不同的实现
	switch ui.conf.ModelType {
	case ModelDoubao:
		virtualSpeakerToPhysicalSpeakerWithDevicesAndCallback(ui.conf, ui.selectedSpeaker, ctx, textCallback, errorCallback)
	case ModelQwen:
		speakerSource, speakerTarget := ui.getQwenSpeakerLanguagePair()
		qwenVirtualSpeakerToPhysicalSpeaker(ui.conf, ui.selectedSpeaker, speakerSource, speakerTarget, ui.getSelectedVoice(), ctx, textCallback, errorCallback)
	}
	ui.addStatus("扬声器组件停止")
}
