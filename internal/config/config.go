// Package config loads and validates papercast's YAML configuration.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that unmarshals from YAML strings like "60m".
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// Std returns d as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

type Config struct {
	Instapaper InstapaperConfig `yaml:"instapaper"`
	Database   DatabaseConfig   `yaml:"database"`
	Processing ProcessingConfig `yaml:"processing"`
	LLM        LLMConfig        `yaml:"llm"`
	TTS        TTSConfig        `yaml:"tts"`
	Feed       FeedConfig       `yaml:"feed"`
	Archiver   ArchiverConfig   `yaml:"archiver"`
	Output     OutputConfig     `yaml:"output"`
}

type InstapaperConfig struct {
	ConsumerKey     string `yaml:"consumer_key"`
	ConsumerSecret  string `yaml:"consumer_secret"`
	CredentialsPath string `yaml:"credentials_path"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

type ProcessingConfig struct {
	MinWords      int      `yaml:"min_words"`
	RetryInterval Duration `yaml:"retry_interval"`
	MaxAttempts   int      `yaml:"max_attempts"`
}

type LLMConfig struct {
	Enabled       bool     `yaml:"enabled"`
	Endpoint      string   `yaml:"endpoint"`
	APIKey        string   `yaml:"api_key"`
	Model         string   `yaml:"model"`
	Timeout       Duration `yaml:"timeout"`
	MaxInputChars int      `yaml:"max_input_chars"`
}

type TTSConfig struct {
	Voices                      []string `yaml:"voices"`
	Language                    string   `yaml:"language"`
	SSML                        bool     `yaml:"ssml"`
	Speed                       float64  `yaml:"speed"`
	MaxChunkBytes               int      `yaml:"max_chunk_bytes"`
	Intro                       *bool    `yaml:"intro"`
	GoogleServiceAccountKeyPath string   `yaml:"google_service_account_key_path"`
}

// IntroEnabled reports whether the per-article spoken introduction is enabled
// (the default when unset).
func (t TTSConfig) IntroEnabled() bool {
	return t.Intro == nil || *t.Intro
}

type FeedConfig struct {
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	Language    string `yaml:"language"`
	Author      string `yaml:"author"`
	CoverArtURL string `yaml:"cover_art_url"`
	BaseURL     string `yaml:"base_url"`
}

// ArchiverConfig configures the optional papercast-archiver integration,
// enabled by setting BaseURL.
type ArchiverConfig struct {
	BaseURL string `yaml:"base_url"`
}

type OutputConfig struct {
	Dir          string `yaml:"dir"`
	FeedFilename string `yaml:"feed_filename"`
}

const (
	DefaultMinWords      = 200
	DefaultRetryInterval = 60 * time.Minute
	DefaultMaxAttempts   = 3
	DefaultLLMTimeout    = 60 * time.Second
	DefaultMaxInputChars = 60000
	DefaultMaxChunkBytes = 4500
	DefaultSpeed         = 1.0
	DefaultLanguage      = "en-US"
	DefaultFeedLanguage  = "en-us"
	DefaultFeedFilename  = "feed.xml"
)

// Load reads, defaults, and validates the config file at path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Processing.MinWords == 0 {
		c.Processing.MinWords = DefaultMinWords
	}
	if c.Processing.RetryInterval == 0 {
		c.Processing.RetryInterval = Duration(DefaultRetryInterval)
	}
	if c.Processing.MaxAttempts == 0 {
		c.Processing.MaxAttempts = DefaultMaxAttempts
	}
	if c.LLM.Timeout == 0 {
		c.LLM.Timeout = Duration(DefaultLLMTimeout)
	}
	if c.LLM.MaxInputChars == 0 {
		c.LLM.MaxInputChars = DefaultMaxInputChars
	}
	if c.TTS.Language == "" {
		c.TTS.Language = DefaultLanguage
	}
	if c.TTS.MaxChunkBytes == 0 {
		c.TTS.MaxChunkBytes = DefaultMaxChunkBytes
	}
	if c.TTS.Speed == 0 {
		c.TTS.Speed = DefaultSpeed
	}
	if c.Feed.Language == "" {
		c.Feed.Language = DefaultFeedLanguage
	}
	if c.Feed.Description == "" {
		c.Feed.Description = c.Feed.Title
	}
	c.Archiver.BaseURL = strings.TrimRight(c.Archiver.BaseURL, "/")
	if c.Output.FeedFilename == "" {
		c.Output.FeedFilename = DefaultFeedFilename
	}
}

func (c *Config) validate() error {
	var errs []string
	req := func(v, name string) {
		if strings.TrimSpace(v) == "" {
			errs = append(errs, name+" is required")
		}
	}

	req(c.Instapaper.ConsumerKey, "instapaper.consumer_key")
	req(c.Instapaper.ConsumerSecret, "instapaper.consumer_secret")
	req(c.Instapaper.CredentialsPath, "instapaper.credentials_path")
	req(c.Database.Path, "database.path")
	req(c.Output.Dir, "output.dir")
	req(c.Feed.Title, "feed.title")
	req(c.Feed.BaseURL, "feed.base_url")

	if c.Feed.BaseURL != "" {
		u, err := url.Parse(c.Feed.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, "feed.base_url must be an absolute http(s) URL")
		}
	}

	if c.Archiver.BaseURL != "" {
		u, err := url.Parse(c.Archiver.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
			errs = append(errs, "archiver.base_url must be an absolute http(s) URL without query or fragment")
		}
	}

	if c.Processing.MinWords < 1 {
		errs = append(errs, "processing.min_words must be >= 1")
	}
	if c.Processing.MaxAttempts < 1 {
		errs = append(errs, "processing.max_attempts must be >= 1")
	}
	if c.Processing.RetryInterval < 0 {
		errs = append(errs, "processing.retry_interval must not be negative")
	}

	if len(c.TTS.Voices) == 0 {
		errs = append(errs, "tts.voices must list at least one voice")
	}
	for _, v := range c.TTS.Voices {
		if !strings.HasPrefix(strings.ToLower(v), strings.ToLower(c.TTS.Language)+"-") {
			errs = append(errs, fmt.Sprintf("tts voice %q does not match language %q", v, c.TTS.Language))
		}
	}
	if c.TTS.MaxChunkBytes < 100 || c.TTS.MaxChunkBytes > 5000 {
		errs = append(errs, "tts.max_chunk_bytes must be between 100 and 5000")
	}
	if c.TTS.Speed < 0.25 || c.TTS.Speed > 2.0 {
		errs = append(errs, "tts.speed must be between 0.25 and 2.0")
	}
	if c.TTS.GoogleServiceAccountKeyPath == "" && os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") == "" {
		errs = append(errs, "tts.google_service_account_key_path is required (or set GOOGLE_APPLICATION_CREDENTIALS)")
	}

	if c.LLM.Enabled {
		req(c.LLM.Endpoint, "llm.endpoint")
		req(c.LLM.Model, "llm.model")
		if c.LLM.Timeout <= 0 {
			errs = append(errs, "llm.timeout must be positive")
		}
		if c.LLM.MaxInputChars < 1 {
			errs = append(errs, "llm.max_input_chars must be >= 1")
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}
