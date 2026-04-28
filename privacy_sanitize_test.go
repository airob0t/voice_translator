package main

import (
	"strings"
	"testing"
)

func TestSanitizeUserVisibleLog_ReplacesModelNamesWithAliases(t *testing.T) {
	got := sanitizeUserVisibleLog("Using Qwen and Doubao models")

	if strings.Contains(strings.ToLower(got), "qwen") {
		t.Fatalf("expected Qwen to be replaced, got %q", got)
	}
	if strings.Contains(strings.ToLower(got), "doubao") {
		t.Fatalf("expected Doubao to be replaced, got %q", got)
	}
	if !strings.Contains(got, "模型二") {
		t.Fatalf("expected 模型二 alias, got %q", got)
	}
	if !strings.Contains(got, "模型一") {
		t.Fatalf("expected 模型一 alias, got %q", got)
	}
}

func TestSanitizeUserVisibleLog_ReplacesModelPhrasesWithoutDuplicateSuffix(t *testing.T) {
	got := sanitizeUserVisibleLog("使用Qwen模型和Doubao model")
	if strings.Contains(got, "模型二模型") {
		t.Fatalf("expected phrase to collapse to alias, got %q", got)
	}
	if strings.Contains(got, "模型一模型") {
		t.Fatalf("expected phrase to collapse to alias, got %q", got)
	}
	if !strings.Contains(got, "模型二") || !strings.Contains(got, "模型一") {
		t.Fatalf("expected both aliases, got %q", got)
	}
}

func TestSanitizeUserVisibleLog_RedactsSessionAndConnectionIDs(t *testing.T) {
	raw := "Session (ID=session-abc-123) started; session_id=raw-session-001, connection_id: conn-XYZ-999"
	got := sanitizeUserVisibleLog(raw)

	for _, secret := range []string{"session-abc-123", "raw-session-001", "conn-XYZ-999"} {
		if strings.Contains(got, secret) {
			t.Fatalf("expected %q to be redacted, got %q", secret, got)
		}
	}
	if !strings.Contains(got, "sess_") {
		t.Fatalf("expected redacted session marker, got %q", got)
	}
	if !strings.Contains(got, "conn_") {
		t.Fatalf("expected redacted connection marker, got %q", got)
	}
}

func TestSanitizeUserVisibleLog_RedactionIsStableForSameID(t *testing.T) {
	first := sanitizeUserVisibleLog("session_id=stable-id")
	second := sanitizeUserVisibleLog("session_id=stable-id")
	if first != second {
		t.Fatalf("expected stable redaction, first=%q second=%q", first, second)
	}
}

func TestSanitizeUserVisibleLog_IsIdempotent(t *testing.T) {
	raw := "Session (ID=session-abc-123) 使用Qwen模型, session_id=raw-session-001, connection_id: conn-XYZ-999"
	once := sanitizeUserVisibleLog(raw)
	twice := sanitizeUserVisibleLog(once)
	if once != twice {
		t.Fatalf("expected idempotent sanitization, once=%q twice=%q", once, twice)
	}
}
