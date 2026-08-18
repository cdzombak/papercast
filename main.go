// papercast creates an RSS podcast feed from unread Instapaper articles,
// narrated by Google Chirp 3 HD text-to-speech.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/cdzombak/papercast/internal/app"
	"github.com/cdzombak/papercast/internal/audio"
	"github.com/cdzombak/papercast/internal/config"
	"github.com/cdzombak/papercast/internal/instapaper"
	"github.com/cdzombak/papercast/internal/llm"
	"github.com/cdzombak/papercast/internal/store"
	"github.com/cdzombak/papercast/internal/tts"
)

var version = "<dev>"

func main() {
	os.Exit(run())
}

func run() int {
	configPath := flag.String("config", "./config.yaml", "path to the config YAML file")
	debugID := flag.String("debug", "", "run debug mode for the given article (Instapaper bookmark) ID")
	listArticles := flag.Bool("list-articles", false, "sync with Instapaper, list known articles, and exit")
	login := flag.Bool("instapaper-login", false, "interactively authenticate with Instapaper and save credentials")
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn, or error")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("papercast " + version)
		return app.ExitSuccess
	}

	cfg, logger, ok := initCommand(*configPath, *logLevel)
	if !ok {
		return app.ExitFailure
	}

	if *login {
		// Deliberately not using signal.NotifyContext here: it intercepts
		// SIGINT and turns off the default kill-the-process behavior, but
		// nothing in the blocking stdin prompts below would ever observe
		// context cancellation, so Ctrl-C would appear to do nothing.
		if err := runLogin(context.Background(), cfg); err != nil {
			logger.Error("instapaper login failed", "error", err)
			return app.ExitFailure
		}
		return app.ExitSuccess
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, closeStore, ok := buildApp(cfg, logger)
	if !ok {
		return app.ExitFailure
	}
	defer closeStore()

	if *listArticles {
		if err := a.ListArticles(ctx, os.Stdout); err != nil {
			logger.Error("list articles failed", "error", err)
			return app.ExitFailure
		}
		return app.ExitSuccess
	}

	closeTTS, ok := addProcessing(ctx, cfg, logger, a)
	if !ok {
		return app.ExitFailure
	}
	defer closeTTS()

	if *debugID != "" {
		id, err := strconv.ParseInt(*debugID, 10, 64)
		if err != nil {
			logger.Error("invalid -debug article ID", "value", *debugID)
			return app.ExitFailure
		}
		if err := a.Debug(ctx, id); err != nil {
			logger.Error("debug run failed", "error", err)
			return app.ExitFailure
		}
		return app.ExitSuccess
	}

	return a.Run(ctx)
}

// initCommand builds the logger and loads the config. It reports its own
// errors; on failure the caller should return app.ExitFailure.
func initCommand(configPath, logLevel string) (*config.Config, *slog.Logger, bool) {
	logger, err := newLogger(logLevel)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return nil, nil, false
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		logger.Error("configuration error", "error", err)
		return nil, nil, false
	}
	return cfg, logger, true
}

// buildApp loads Instapaper credentials, opens the store, and assembles the
// core App (no TTS/audio/LLM). It logs its own errors. The returned cleanup
// closes the store and must be deferred when ok is true.
func buildApp(cfg *config.Config, logger *slog.Logger) (a *app.App, cleanup func(), ok bool) {
	creds, err := instapaper.LoadCredentials(cfg.Instapaper.CredentialsPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			logger.Error(`instapaper credentials not found; run "papercast instapaper-login" first`,
				"path", cfg.Instapaper.CredentialsPath)
		} else {
			logger.Error("load instapaper credentials", "error", err)
		}
		return nil, nil, false
	}
	ipClient := instapaper.NewClient(cfg.Instapaper.ConsumerKey, cfg.Instapaper.ConsumerSecret, creds)

	st, err := store.Open(cfg.Database.Path, nil)
	if err != nil {
		logger.Error("open database", "error", err)
		return nil, nil, false
	}

	a = &app.App{
		Cfg:        cfg,
		Store:      st,
		Instapaper: ipClient,
		Log:        logger,
		Version:    version,
	}
	return a, func() { _ = st.Close() }, true
}

// addProcessing attaches the TTS synthesizer, ffmpeg assembler, and optional
// LLM describer to a, and creates the output directory. It logs its own
// errors. The returned cleanup closes the TTS client and must be deferred
// when ok is true.
func addProcessing(ctx context.Context, cfg *config.Config, logger *slog.Logger, a *app.App) (cleanup func(), ok bool) {
	if cfg.TTS.GoogleServiceAccountKeyPath != "" && os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") == "" {
		if err := os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", cfg.TTS.GoogleServiceAccountKeyPath); err != nil {
			logger.Error("set GOOGLE_APPLICATION_CREDENTIALS", "error", err)
			return nil, false
		}
	}
	synth, err := tts.NewGoogleSynthesizer(ctx)
	if err != nil {
		logger.Error("create text-to-speech client", "error", err)
		return nil, false
	}
	a.Synth = tts.WithRetry(synth, tts.RetryOptions{})
	a.Assembler = audio.NewFFmpegAssembler("", "")
	if cfg.LLM.Enabled {
		a.Describer = llm.NewDescriber(cfg.LLM.Endpoint, cfg.LLM.APIKey, cfg.LLM.Model,
			cfg.LLM.Timeout.Std(), cfg.LLM.MaxInputChars)
	}

	if err := os.MkdirAll(cfg.Output.Dir, 0o755); err != nil {
		logger.Error("create output directory", "error", err)
		_ = synth.Close()
		return nil, false
	}
	return func() { _ = synth.Close() }, true
}

func newLogger(level string) (*slog.Logger, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("invalid -log-level %q (use debug, info, warn, or error)", level)
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l})), nil
}

// runLogin performs the interactive xAuth flow and saves the resulting token.
func runLogin(ctx context.Context, cfg *config.Config) error {
	fmt.Print("Instapaper username (email): ")
	reader := bufio.NewReader(os.Stdin)
	username, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read username: %w", err)
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return errors.New("username must not be empty")
	}

	fmt.Print("Instapaper password: ")
	passwordBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}

	client := instapaper.NewClient(cfg.Instapaper.ConsumerKey, cfg.Instapaper.ConsumerSecret, nil)
	creds, err := client.RequestAccessToken(ctx, username, string(passwordBytes))
	if err != nil {
		return err
	}
	if err := instapaper.SaveCredentials(cfg.Instapaper.CredentialsPath, creds); err != nil {
		return err
	}
	fmt.Printf("Instapaper credentials saved to %s\n", cfg.Instapaper.CredentialsPath)
	return nil
}
