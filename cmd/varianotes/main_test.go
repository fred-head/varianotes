package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapCommandReturnsSecretOnlyToOperatorOutput(t *testing.T) {
	t.Setenv("VARIANOTES_AUTH_MODE", "local")
	t.Setenv("VARIANOTES_AUTH_METADATA", filepath.Join(t.TempDir(), "auth.db"))
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalStdout := os.Stdout
	os.Stdout = write
	commandErr := runAuthCommand(context.Background(), logger, []string{"auth", "bootstrap-token"})
	os.Stdout = originalStdout
	if closeErr := write.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	output, readErr := io.ReadAll(read)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if commandErr != nil {
		t.Fatal(commandErr)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 3 || len(lines[1]) < 40 {
		t.Fatalf("unexpected operator output: %q", output)
	}
	if strings.Contains(logs.String(), lines[1]) {
		t.Fatal("bootstrap token was written to structured logs")
	}
}

func TestAuthCommandRejectsUnknownAndDisabledBootstrap(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := runAuthCommand(context.Background(), logger, []string{"auth", "unknown"}); err == nil || !strings.Contains(err.Error(), "usage: varianotes auth") {
		t.Fatal("unknown auth command was accepted")
	}
	t.Setenv("VARIANOTES_AUTH_MODE", "disabled")
	t.Setenv("VARIANOTES_AUTH_METADATA", filepath.Join(t.TempDir(), "auth.db"))
	if err := runAuthCommand(context.Background(), logger, []string{"auth", "bootstrap-token"}); err == nil {
		t.Fatal("bootstrap token was generated with authentication disabled")
	}
}

func TestHelpAndVersionOutputUseVariaNotesCLIName(t *testing.T) {
	var usage bytes.Buffer
	printUsage(&usage)
	if !strings.Contains(usage.String(), "varianotes auth") || !strings.Contains(usage.String(), "VariaNotes") {
		t.Fatalf("unexpected help output: %q", usage.String())
	}
	var currentVersion bytes.Buffer
	printVersion(&currentVersion, "0.1.0-alpha.2.security.4")
	if got, want := strings.TrimSpace(currentVersion.String()), "varianotes 0.1.0-alpha.2.security.4"; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}
