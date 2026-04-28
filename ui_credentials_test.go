package main

import (
	"testing"

	"fyne.io/fyne/v2/widget"
)

func TestValidateModelCredentials_RequiresDoubaoFields(t *testing.T) {
	ui := &TranslationUI{
		conf:                 Config{ModelType: ModelDoubao},
		doubaoAppIDEntry:     widget.NewEntry(),
		doubaoAccessKeyEntry: widget.NewPasswordEntry(),
		qwenAPIKeyEntry:      widget.NewPasswordEntry(),
	}

	if err := ui.validateModelCredentials(); err == nil {
		t.Fatal("expected missing doubao credentials to fail validation")
	}

	ui.doubaoAppIDEntry.SetText("app-id")
	ui.doubaoAccessKeyEntry.SetText("access-token")
	if err := ui.validateModelCredentials(); err != nil {
		t.Fatalf("expected doubao credentials to validate, got %v", err)
	}
}

func TestValidateModelCredentials_RequiresQwenAPIKey(t *testing.T) {
	ui := &TranslationUI{
		conf:                 Config{ModelType: ModelQwen},
		doubaoAppIDEntry:     widget.NewEntry(),
		doubaoAccessKeyEntry: widget.NewPasswordEntry(),
		qwenAPIKeyEntry:      widget.NewPasswordEntry(),
	}

	if err := ui.validateModelCredentials(); err == nil {
		t.Fatal("expected missing qwen api key to fail validation")
	}

	ui.qwenAPIKeyEntry.SetText("dashscope-key")
	if err := ui.validateModelCredentials(); err != nil {
		t.Fatalf("expected qwen credentials to validate, got %v", err)
	}
}
