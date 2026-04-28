package main

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2/data/binding"
)

func TestAddStatus_SanitizesModelAndSessionDetails(t *testing.T) {
	ui := &TranslationUI{statusString: binding.NewString()}

	ui.addStatus("使用Qwen模型 (JSON协议), session_id=abc123")

	got, err := ui.statusString.Get()
	if err != nil {
		t.Fatalf("unexpected get error: %v", err)
	}
	if strings.Contains(strings.ToLower(got), "qwen") {
		t.Fatalf("expected model name to be sanitized, got %q", got)
	}
	if strings.Contains(got, "abc123") {
		t.Fatalf("expected session id to be redacted, got %q", got)
	}
	if !strings.Contains(got, "模型二") {
		t.Fatalf("expected model alias in status, got %q", got)
	}
	if strings.Contains(got, "模型二模型") {
		t.Fatalf("expected normalized model alias text, got %q", got)
	}
	if !strings.Contains(got, "sess_") {
		t.Fatalf("expected redacted session marker in status, got %q", got)
	}
}
