package main

import (
	"testing"

	"fyne.io/fyne/v2/widget"
)

func TestApplyQwenLanguageDefaultsForMode_SpeakerUsesEnToZh(t *testing.T) {
	ui := &TranslationUI{
		conf: Config{
			ModelType: ModelQwen,
		},
		mode: ModeSpeaker,
	}
	ui.sourceLangSelect = widget.NewSelect([]string{"中文", "英语"}, func(value string) {
		if code := getOptionCode(qwenLanguageOptions, value); code != "" {
			ui.selectedSourceLang = code
		}
	})
	ui.targetLangSelect = widget.NewSelect([]string{"中文", "英语"}, func(value string) {
		if code := getOptionCode(qwenLanguageOptions, value); code != "" {
			ui.selectedTargetLang = code
		}
	})

	ui.sourceLangSelect.SetSelected("中文")
	ui.targetLangSelect.SetSelected("英语")

	ui.applyQwenLanguageDefaultsForMode()

	if ui.getSelectedSourceLanguage() != "en" {
		t.Fatalf("expected qwen speaker default source to be en, got %q", ui.getSelectedSourceLanguage())
	}
	if ui.getSelectedTargetLanguage() != "zh" {
		t.Fatalf("expected qwen speaker default target to be zh, got %q", ui.getSelectedTargetLanguage())
	}
}

func TestGetQwenSpeakerLanguagePair_BidirectionalUsesReversePair(t *testing.T) {
	ui := &TranslationUI{
		mode:               ModeBidirectional,
		selectedSourceLang: "zh",
		selectedTargetLang: "en",
	}

	source, target := ui.getQwenSpeakerLanguagePair()

	if source != "en" {
		t.Fatalf("expected bidirectional speaker source to be en, got %q", source)
	}
	if target != "zh" {
		t.Fatalf("expected bidirectional speaker target to be zh, got %q", target)
	}
}

func TestGetQwenSpeakerLanguagePair_SpeakerModeUsesDirectPair(t *testing.T) {
	ui := &TranslationUI{
		mode:               ModeSpeaker,
		selectedSourceLang: "en",
		selectedTargetLang: "zh",
	}

	source, target := ui.getQwenSpeakerLanguagePair()

	if source != "en" || target != "zh" {
		t.Fatalf("expected speaker mode to use direct pair en->zh, got %s->%s", source, target)
	}
}
