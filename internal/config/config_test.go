package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validYAML = `
instapaper:
  consumer_key: ck
  consumer_secret: cs
  credentials_path: /data/instapaper.json
database:
  path: /data/papercast.db
tts:
  voices:
    - en-US-Chirp3-HD-Aoede
    - en-US-Chirp3-HD-Puck
  google_service_account_key_path: /data/gcp.json
feed:
  title: My Articles
  base_url: https://example.com/papercast/
output:
  dir: /srv/papercast
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadValidAppliesDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, validYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Processing.MinWords != 200 {
		t.Errorf("MinWords = %d, want 200", cfg.Processing.MinWords)
	}
	if cfg.Processing.RetryInterval.Std() != 60*time.Minute {
		t.Errorf("RetryInterval = %v, want 60m", cfg.Processing.RetryInterval.Std())
	}
	if cfg.Processing.MaxAttempts != 3 {
		t.Errorf("MaxAttempts = %d, want 3", cfg.Processing.MaxAttempts)
	}
	if cfg.LLM.Enabled {
		t.Error("LLM should be disabled by default")
	}
	if cfg.LLM.Timeout.Std() != 60*time.Second {
		t.Errorf("LLM timeout = %v, want 60s", cfg.LLM.Timeout.Std())
	}
	if cfg.TTS.Language != "en-US" {
		t.Errorf("TTS language = %q, want en-US", cfg.TTS.Language)
	}
	if cfg.TTS.MaxChunkBytes != 4500 {
		t.Errorf("MaxChunkBytes = %d, want 4500", cfg.TTS.MaxChunkBytes)
	}
	if !cfg.TTS.IntroEnabled() {
		t.Error("intro should default to enabled")
	}
	if cfg.Feed.Description != "My Articles" {
		t.Errorf("feed description should default to title, got %q", cfg.Feed.Description)
	}
	if cfg.Output.FeedFilename != "feed.xml" {
		t.Errorf("FeedFilename = %q, want feed.xml", cfg.Output.FeedFilename)
	}
}

func TestLoadOverrides(t *testing.T) {
	yaml := validYAML + `
processing:
  min_words: 50
  retry_interval: 2h
  max_attempts: 5
llm:
  enabled: true
  endpoint: https://api.openai.com/v1
  api_key: sk-x
  model: gpt-5.6-luna
  timeout: 30s
  max_input_chars: 1000
`
	cfg, err := Load(writeConfig(t, yaml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Processing.MinWords != 50 || cfg.Processing.MaxAttempts != 5 {
		t.Errorf("processing overrides not applied: %+v", cfg.Processing)
	}
	if cfg.Processing.RetryInterval.Std() != 2*time.Hour {
		t.Errorf("RetryInterval = %v, want 2h", cfg.Processing.RetryInterval.Std())
	}
	if !cfg.LLM.Enabled || cfg.LLM.Timeout.Std() != 30*time.Second {
		t.Errorf("llm overrides not applied: %+v", cfg.LLM)
	}
}

func TestIntroDisable(t *testing.T) {
	yaml := strings.Replace(validYAML, "tts:\n", "tts:\n  intro: false\n", 1)
	cfg, err := Load(writeConfig(t, yaml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TTS.IntroEnabled() {
		t.Error("intro: false should disable the intro")
	}
}

func TestValidationErrors(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(string) string
		wantErr string
	}{
		{"missing consumer key", func(s string) string { return strings.Replace(s, "consumer_key: ck", "consumer_key: \"\"", 1) }, "instapaper.consumer_key"},
		{"missing db path", func(s string) string { return strings.Replace(s, "path: /data/papercast.db", "path: \"\"", 1) }, "database.path"},
		{"missing feed title", func(s string) string { return strings.Replace(s, "title: My Articles", "title: \"\"", 1) }, "feed.title"},
		{"bad base url", func(s string) string { return strings.Replace(s, "https://example.com/papercast/", "not a url", 1) }, "feed.base_url"},
		{"no voices", func(s string) string {
			return strings.Replace(s, "  voices:\n    - en-US-Chirp3-HD-Aoede\n    - en-US-Chirp3-HD-Puck\n", "  voices: []\n", 1)
		}, "tts.voices"},
		{"voice language mismatch", func(s string) string {
			return strings.Replace(s, "en-US-Chirp3-HD-Puck", "de-DE-Chirp3-HD-Puck", 1)
		}, "does not match language"},
		{"llm enabled without model", func(s string) string {
			return s + "\nllm:\n  enabled: true\n  endpoint: https://api.openai.com/v1\n"
		}, "llm.model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.mutate(validYAML)))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	_, err := Load(writeConfig(t, validYAML+"\nbogus_section:\n  x: 1\n"))
	if err == nil {
		t.Fatal("expected error for unknown config field")
	}
}

func TestGoogleKeyPathRequiredUnlessEnvSet(t *testing.T) {
	yaml := strings.Replace(validYAML, "  google_service_account_key_path: /data/gcp.json\n", "", 1)

	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	_ = os.Unsetenv("GOOGLE_APPLICATION_CREDENTIALS")
	if _, err := Load(writeConfig(t, yaml)); err == nil || !strings.Contains(err.Error(), "google_service_account_key_path") {
		t.Errorf("expected google_service_account_key_path error, got %v", err)
	}

	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/env/gcp.json")
	if _, err := Load(writeConfig(t, yaml)); err != nil {
		t.Errorf("expected valid config when env var set, got %v", err)
	}
}

func TestMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}
