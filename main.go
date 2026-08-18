// papercast creates an RSS podcast feed from unread Instapaper articles,
// narrated by Google Chirp 3 HD text-to-speech.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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

// exitUsage is returned for invalid command lines (unknown command, bad flags).
const exitUsage = 2

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "papercast: a command is required")
		printUsage(os.Stderr)
		return exitUsage
	}
	switch args[0] {
	case "generate":
		return cmdGenerate(args[1:])
	case "list-articles":
		return cmdListArticles(args[1:])
	case "debug":
		return cmdDebug(args[1:])
	case "instapaper-login":
		return cmdLogin(args[1:])
	case "version", "-version", "--version":
		fmt.Println("papercast " + version)
		return app.ExitSuccess
	case "help", "-h", "-help", "--help":
		return cmdHelp(args[1:])
	default:
		return unknownCommand(args[0])
	}
}

// oldFlagHints maps former top-level mode/option flags to migration hints.
var oldFlagHints = map[string]string{
	"debug":            `use "papercast debug -id <bookmark-id>"`,
	"list-articles":    `use "papercast list-articles"`,
	"instapaper-login": `use "papercast instapaper-login"`,
	"config":           `flags now follow a command, e.g. "papercast generate -config config.yaml"`,
	"log-level":        `flags now follow a command, e.g. "papercast generate -log-level debug"`,
}

func unknownCommand(arg string) int {
	if strings.HasPrefix(arg, "-") {
		name := strings.TrimLeft(arg, "-")
		name, _, _ = strings.Cut(name, "=")
		if hint, ok := oldFlagHints[name]; ok {
			fmt.Fprintf(os.Stderr, "papercast: %s is no longer a top-level flag; %s\n", arg, hint)
		} else {
			fmt.Fprintf(os.Stderr, "papercast: unknown flag %s (papercast now uses subcommands)\n", arg)
		}
	} else {
		fmt.Fprintf(os.Stderr, "papercast: unknown command %q\n", arg)
	}
	fmt.Fprintln(os.Stderr, `Run "papercast help" for usage.`)
	return exitUsage
}

func cmdHelp(args []string) int {
	if len(args) == 0 {
		printUsage(os.Stdout)
		return app.ExitSuccess
	}
	switch args[0] {
	case "generate":
		return cmdGenerate([]string{"-h"})
	case "list-articles":
		return cmdListArticles([]string{"-h"})
	case "debug":
		return cmdDebug([]string{"-h"})
	case "instapaper-login":
		return cmdLogin([]string{"-h"})
	case "version":
		fmt.Println("Usage: papercast version\n\nPrint the papercast version and exit.")
		return app.ExitSuccess
	case "help":
		printUsage(os.Stdout)
		return app.ExitSuccess
	default:
		fmt.Fprintf(os.Stderr, "papercast help: unknown command %q\n", args[0])
		printUsage(os.Stderr)
		return exitUsage
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `papercast creates an RSS podcast feed from unread Instapaper articles.

Usage:

  papercast <command> [flags]

Commands:

  generate          sync with Instapaper, narrate new articles, and write the feed
  list-articles     sync with Instapaper, list known articles, and exit
  debug             reprocess one article and write debug output to the work directory
  instapaper-login  interactively authenticate with Instapaper and save credentials
  version           print the version and exit
  help [command]    print usage for papercast or a single command

Run "papercast help <command>" for details on a command's flags.
`)
}

// addCommonFlags registers the -config and -log-level flags shared by every command.
func addCommonFlags(fs *flag.FlagSet) (configPath, logLevel *string) {
	configPath = fs.String("config", "./config.yaml", "path to the config YAML file")
	logLevel = fs.String("log-level", "info", "log level: debug, info, warn, or error")
	return configPath, logLevel
}

// usageFor returns a usage printer for fs: synopsis, then the flag defaults.
func usageFor(fs *flag.FlagSet, synopsis string) func(io.Writer) {
	return func(w io.Writer) {
		fmt.Fprintf(w, "%s\n\nFlags:\n", synopsis)
		fs.SetOutput(w)
		fs.PrintDefaults()
		fs.SetOutput(os.Stderr)
	}
}

// parseFlags parses args into fs. It returns (exit, true) when the command
// should stop immediately: exit 0 after -h/--help (usage printed to stdout),
// exit 2 on a parse error or unexpected positional argument (error and usage
// printed to stderr). Returns (0, false) when parsing succeeded.
func parseFlags(fs *flag.FlagSet, args []string, usage func(io.Writer)) (int, bool) {
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {}
	err := fs.Parse(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		usage(os.Stdout)
		return app.ExitSuccess, true
	case err != nil:
		usage(os.Stderr)
		return exitUsage, true
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "papercast %s: unexpected argument %q\n", fs.Name(), fs.Arg(0))
		usage(os.Stderr)
		return exitUsage, true
	}
	return 0, false
}

func cmdGenerate(args []string) int {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	configPath, logLevel := addCommonFlags(fs)
	usage := usageFor(fs, `Usage: papercast generate [flags]

Sync with Instapaper, narrate new articles, and write the feed and MP3s to the
output directory.`)
	if exit, done := parseFlags(fs, args, usage); done {
		return exit
	}

	cfg, logger, ok := initCommand(*configPath, *logLevel)
	if !ok {
		return app.ExitFailure
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, closeStore, ok := buildApp(cfg, logger)
	if !ok {
		return app.ExitFailure
	}
	defer closeStore()

	closeTTS, ok := addProcessing(ctx, cfg, logger, a)
	if !ok {
		return app.ExitFailure
	}
	defer closeTTS()

	return a.Run(ctx)
}

func cmdListArticles(args []string) int {
	fs := flag.NewFlagSet("list-articles", flag.ContinueOnError)
	configPath, logLevel := addCommonFlags(fs)
	usage := usageFor(fs, `Usage: papercast list-articles [flags]

Sync with Instapaper, list each known article's ID, source, and title, and exit.`)
	if exit, done := parseFlags(fs, args, usage); done {
		return exit
	}

	cfg, logger, ok := initCommand(*configPath, *logLevel)
	if !ok {
		return app.ExitFailure
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, closeStore, ok := buildApp(cfg, logger)
	if !ok {
		return app.ExitFailure
	}
	defer closeStore()

	if err := a.ListArticles(ctx, os.Stdout); err != nil {
		logger.Error("list articles failed", "error", err)
		return app.ExitFailure
	}
	return app.ExitSuccess
}

func cmdDebug(args []string) int {
	fs := flag.NewFlagSet("debug", flag.ContinueOnError)
	configPath, logLevel := addCommonFlags(fs)
	idArg := fs.String("id", "", "Instapaper bookmark ID of the article to debug")
	usage := usageFor(fs, `Usage: papercast debug -id <bookmark-id> [flags]

Reprocess a single article and write a chunk-by-chunk debug HTML file plus the
assembled MP3 into the work directory. Nothing is published, and the run does
not count against the article's retry budget.`)
	if exit, done := parseFlags(fs, args, usage); done {
		return exit
	}
	if *idArg == "" {
		fmt.Fprintln(os.Stderr, "papercast debug: -id is required")
		usage(os.Stderr)
		return exitUsage
	}
	id, err := strconv.ParseInt(*idArg, 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "papercast debug: invalid -id %q (expected a numeric Instapaper bookmark ID)\n", *idArg)
		return exitUsage
	}

	cfg, logger, ok := initCommand(*configPath, *logLevel)
	if !ok {
		return app.ExitFailure
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, closeStore, ok := buildApp(cfg, logger)
	if !ok {
		return app.ExitFailure
	}
	defer closeStore()

	closeTTS, ok := addProcessing(ctx, cfg, logger, a)
	if !ok {
		return app.ExitFailure
	}
	defer closeTTS()

	if err := a.Debug(ctx, id); err != nil {
		logger.Error("debug run failed", "error", err)
		return app.ExitFailure
	}
	return app.ExitSuccess
}

func cmdLogin(args []string) int {
	fs := flag.NewFlagSet("instapaper-login", flag.ContinueOnError)
	configPath, logLevel := addCommonFlags(fs)
	usage := usageFor(fs, `Usage: papercast instapaper-login [flags]

Interactively authenticate with Instapaper and save the resulting OAuth token
to the path configured at instapaper.credentials_path.`)
	if exit, done := parseFlags(fs, args, usage); done {
		return exit
	}

	cfg, logger, ok := initCommand(*configPath, *logLevel)
	if !ok {
		return app.ExitFailure
	}

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
