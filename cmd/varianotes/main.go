package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fred-head/varianotes/internal/app"
	"github.com/fred-head/varianotes/internal/auth"
	"golang.org/x/term"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-h", "--help", "help":
			printUsage(os.Stdout)
			return
		case "-v", "--version", "version":
			printVersion(os.Stdout, version)
			return
		}
		if err := runAuthCommand(context.Background(), logger, os.Args[1:]); err != nil {
			logger.Error("authentication command failed", "error", err)
			os.Exit(1)
		}
		return
	}
	authConfig, err := auth.ConfigFromEnvironment(os.LookupEnv)
	if err != nil {
		logger.Error("configure authentication", "error", err)
		os.Exit(1)
	}
	authConfig.PasswordCoordinator = app.NewPasswordChangeCoordinator(os.Getenv("VARIANOTES_NOTEBOOK_METADATA"))
	authConfig.RecoveryCoordinator = app.NewRecoveryCodeCoordinator(os.Getenv("VARIANOTES_NOTEBOOK_METADATA"))
	authService, err := auth.Open(context.Background(), authConfig, logger)
	if err != nil {
		logger.Error("initialize authentication metadata", "error", err)
		os.Exit(1)
	}
	defer authService.Close()
	authState, err := authService.State(context.Background())
	if err != nil {
		logger.Error("read authentication state", "error", err)
		os.Exit(1)
	}
	logger.Info("authentication metadata ready", "mode", authState.Mode, "modeExplicit", authState.ModeExplicit, "setupCompleted", authState.SetupCompleted, "schemaVersion", authState.SchemaVersion)
	if authState.Mode == auth.ModeDisabled {
		logger.Warn("authentication is explicitly disabled; restrict access with a trusted network or external protection")
	}
	devVaultPath := os.Getenv("VARIANOTES_DEV_VAULT_PATH")
	handler, err := app.NewHandlerWithAuth(logger, devVaultPath, authService, version)
	if err != nil {
		logger.Error("configure application", "error", err)
		os.Exit(1)
	}
	server := &http.Server{
		Addr:              ":8080",
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      6 * time.Minute,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	if address := os.Getenv("VARIANOTES_ADDR"); address != "" {
		server.Addr = address
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("varianotes started", "address", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped unexpectedly", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
	logger.Info("varianotes stopped")
}

func runAuthCommand(ctx context.Context, logger *slog.Logger, arguments []string) error {
	if len(arguments) != 2 || arguments[0] != "auth" || (arguments[1] != "bootstrap-token" && arguments[1] != "reset-password" && arguments[1] != "reset-mfa") {
		return errors.New("usage: varianotes auth <bootstrap-token|reset-password|reset-mfa>")
	}
	config, err := auth.ConfigFromEnvironment(os.LookupEnv)
	if err != nil {
		return fmt.Errorf("configure authentication: %w", err)
	}
	config.PasswordCoordinator = app.NewPasswordChangeCoordinator(os.Getenv("VARIANOTES_NOTEBOOK_METADATA"))
	config.RecoveryCoordinator = app.NewRecoveryCodeCoordinator(os.Getenv("VARIANOTES_NOTEBOOK_METADATA"))
	if arguments[1] == "reset-mfa" {
		config.AllowMFAKeyRecovery = true
	}
	service, err := auth.Open(ctx, config, logger)
	if err != nil {
		return fmt.Errorf("initialize authentication metadata: %w", err)
	}
	defer service.Close()
	if arguments[1] == "reset-password" {
		return resetOwnerPassword(ctx, service)
	}
	if arguments[1] == "reset-mfa" {
		if err := service.ResetMFA(ctx); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "Owner MFA reset. All browser sessions were revoked; the owner password was not changed.")
		return nil
	}
	token, err := service.CreateBootstrapToken(ctx)
	if err != nil {
		return err
	}
	// This is deliberate command output to the operator's terminal, not a log
	// record. The plaintext token is returned once and is never persisted.
	fmt.Fprintln(os.Stdout, "Use this one-time setup token in VariaNotes within 15 minutes:")
	fmt.Fprintln(os.Stdout, token.Value)
	fmt.Fprintf(os.Stdout, "Expires at: %s\n", token.ExpiresAt.Format(time.RFC3339))
	return nil
}

func resetOwnerPassword(ctx context.Context, service *auth.Service) error {
	input := os.Stdin
	if !term.IsTerminal(int(input.Fd())) {
		return errors.New("password recovery requires an interactive terminal")
	}
	fmt.Fprint(os.Stderr, "New owner password: ")
	first, err := term.ReadPassword(int(input.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return fmt.Errorf("read new password: %w", err)
	}
	defer clear(first)
	fmt.Fprint(os.Stderr, "Confirm new owner password: ")
	second, err := term.ReadPassword(int(input.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return fmt.Errorf("confirm new password: %w", err)
	}
	defer clear(second)
	if string(first) != string(second) {
		return errors.New("passwords do not match")
	}
	if err := service.RecoverPassword(ctx, string(first)); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "Owner password reset. All browser sessions were revoked; MFA configuration was not changed.")
	return nil
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, "VariaNotes — self-hosted Markdown notes with full control over your data")
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "Usage:")
	fmt.Fprintln(writer, "  varianotes [--help|--version]")
	fmt.Fprintln(writer, "  varianotes auth <bootstrap-token|reset-password|reset-mfa>")
}

func printVersion(writer io.Writer, currentVersion string) {
	fmt.Fprintf(writer, "varianotes %s\n", currentVersion)
}
