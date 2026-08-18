package main

import "testing"

// TestRunArgDispatch checks exit codes for the argument-dispatch layer. Every
// vector fails before any credentials, database, or network access; the
// "generate missing config" vector reaches config.Load and stops there.
func TestRunArgDispatch(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no args", nil, 2},
		{"unknown command", []string{"frobnicate"}, 2},
		{"unknown flag", []string{"-frobnicate"}, 2},
		{"old list-articles flag", []string{"-list-articles"}, 2},
		{"old instapaper-login flag", []string{"-instapaper-login"}, 2},
		{"old debug flag with equals", []string{"-debug=123"}, 2},
		{"old config flag", []string{"-config", "x.yaml"}, 2},
		{"version command", []string{"version"}, 0},
		{"version flag", []string{"--version"}, 0},
		{"top-level help flag", []string{"-h"}, 0},
		{"help command", []string{"help"}, 0},
		{"help generate", []string{"help", "generate"}, 0},
		{"help unknown", []string{"help", "frobnicate"}, 2},
		{"generate -h", []string{"generate", "-h"}, 0},
		{"generate bad flag", []string{"generate", "-nope"}, 2},
		{"generate positional arg", []string{"generate", "extra"}, 2},
		{"debug missing -id", []string{"debug"}, 2},
		{"debug non-numeric -id", []string{"debug", "-id", "abc"}, 2},
		{"generate missing config", []string{"generate", "-config", "/nonexistent/config.yaml"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(tc.args); got != tc.want {
				t.Errorf("run(%v) = %d, want %d", tc.args, got, tc.want)
			}
		})
	}
}
