package main

import "testing"

func TestGetModelConfig_UsesOfficialStandaloneEndpoints(t *testing.T) {
	t.Run("doubao", func(t *testing.T) {
		host, endpoint := GetModelConfig(ModelDoubao)
		if host != defaultDoubaoHost {
			t.Fatalf("unexpected doubao host: %q", host)
		}
		if endpoint != defaultDoubaoEndpoint {
			t.Fatalf("unexpected doubao endpoint: %q", endpoint)
		}
	})

	t.Run("qwen", func(t *testing.T) {
		host, endpoint := GetModelConfig(ModelQwen)
		if host != defaultQwenHost {
			t.Fatalf("unexpected qwen host: %q", host)
		}
		if endpoint != defaultQwenEndpoint {
			t.Fatalf("unexpected qwen endpoint: %q", endpoint)
		}
	})
}

func TestBuildHTTPHeader_UsesStandaloneDoubaoCredentials(t *testing.T) {
	header := buildHTTPHeader(Config{
		DoubaoAppID:      "app-id",
		DoubaoAccessKey:  "access-token",
		DoubaoResourceID: defaultDoubaoResourceID,
	}, "conn-1")

	if got := header.Get("X-Api-App-Key"); got != "app-id" {
		t.Fatalf("unexpected X-Api-App-Key: %q", got)
	}
	if got := header.Get("X-Api-Access-Key"); got != "access-token" {
		t.Fatalf("unexpected X-Api-Access-Key: %q", got)
	}
	if got := header.Get("X-Api-Resource-Id"); got != defaultDoubaoResourceID {
		t.Fatalf("unexpected X-Api-Resource-Id: %q", got)
	}
	if got := header.Get("Authorization"); got != "" {
		t.Fatalf("expected no Authorization header, got %q", got)
	}
}
