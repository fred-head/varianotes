package app

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/fred-head/varianotes/internal/auth"
	"github.com/fred-head/varianotes/internal/encryption"
)

// Keep the prior inode open: a durable rewrite of identical target bytes must
// replace it, rather than treating visible equality as a completed replay.
func requireEnvelopeRewrite(t *testing.T, filename string, replay func() error) {
	t.Helper()
	file, err := os.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := replay(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("R3: matching visible envelope skipped durable rewrite")
	}
}

func TestEnvelopeReplayRewritesMatchingTarget(t *testing.T) {
	metadataPath, entries, _ := passwordChangeFixture(t, 1, false)
	coordinator := NewPasswordChangeCoordinator(metadataPath)
	encoded, err := coordinator.PreparePasswordChange(t.Context(), "original vault password", "replacement vault password")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := decodePasswordChangePlan(encoded)
	if err != nil {
		t.Fatal(err)
	}
	state, err := inspectVaultProtection(entries[0], metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forward := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "forward"}[forward], func(t *testing.T) {
			store, err := encryption.NewVaultKeyEnvelopeStore(state.envelopePath)
			if err != nil {
				t.Fatal(err)
			}
			target := plan.Updates[0].OldEnvelope
			if forward {
				target = plan.Updates[0].NewEnvelope
			}
			if err := store.Save(mustDecodeEnvelope(t, target)); err != nil {
				t.Fatal(err)
			}
			replay := func() error {
				if forward {
					return coordinator.ApplyPasswordChange(t.Context(), encoded)
				}
				return coordinator.RollbackPasswordChange(t.Context(), encoded)
			}
			requireEnvelopeRewrite(t, state.envelopePath, replay)
			requireEnvelopeRewrite(t, state.envelopePath, func() error {
				return applyRecoveryEnvelope(state.envelopePath, plan.Updates[0].OldEnvelope, plan.Updates[0].NewEnvelope, true, true, forward)
			})
		})
	}
}

func TestEnvelopeRollbackProcessRestart(t *testing.T) {
	if filename := os.Getenv("VNOTES_ENVELOPE_RENAME_TARGET"); filename != "" {
		// Reproduce the exact on-disk boundary: synced old-envelope temporary,
		// visible rename, process exit before parent sync. No defers/cleanup run.
		data, err := os.ReadFile(os.Getenv("VNOTES_ENVELOPE_OLD_BYTES"))
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.CreateTemp(filepath.Dir(filename), ".interrupted-envelope-")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := file.Sync(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(file.Name(), filename); err != nil {
			t.Fatal(err)
		}
		os.Exit(86)
	}
	metadataPath, entries, content := passwordChangeFixture(t, 1, true)
	coordinator := NewPasswordChangeCoordinator(metadataPath)
	authPath := filepath.Join(filepath.Dir(metadataPath), "auth.db")
	config := auth.Config{Mode: auth.ModeLocal, MetadataPath: authPath, PasswordCoordinator: coordinator}
	service, err := auth.Open(t.Context(), config, passwordChangeLogger())
	if err != nil {
		t.Fatal(err)
	}
	token, err := service.CreateBootstrapToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteSetup(t.Context(), token.Value, "original vault password"); err != nil {
		t.Fatal(err)
	}
	encoded, err := coordinator.PreparePasswordChange(t.Context(), "original vault password", "interrupted replacement password")
	if err != nil {
		t.Fatal(err)
	}
	if err := insertPasswordChangeJournal(t, authPath, "prepared", encoded); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.ApplyPasswordChange(t.Context(), encoded); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	plan, err := decodePasswordChangePlan(encoded)
	if err != nil {
		t.Fatal(err)
	}
	state, err := inspectVaultProtection(entries[0], metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	oldFile := filepath.Join(t.TempDir(), "old-envelope.json")
	if err := os.WriteFile(oldFile, plan.Updates[0].OldEnvelope, 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		command := exec.Command(os.Args[0], "-test.run=^TestEnvelopeRollbackProcessRestart$")
		command.Env = append(os.Environ(), "VNOTES_ENVELOPE_RENAME_TARGET="+state.envelopePath, "VNOTES_ENVELOPE_OLD_BYTES="+oldFile)
		output, err := command.CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 86 {
			t.Fatalf("no interrupted rename: %v %s", err, output)
		}
	}
	var recovered *auth.Service
	requireEnvelopeRewrite(t, state.envelopePath, func() error {
		var err error
		recovered, err = auth.Open(t.Context(), config, passwordChangeLogger())
		if recovered != nil {
			t.Cleanup(func() { _ = recovered.Close() })
		}
		return err
	})
	if err := recovered.VerifyPassword(t.Context(), "original vault password"); err != nil {
		t.Fatal(err)
	}
	assertVaultPasswords(t, entries, metadataPath, "original vault password")
	database, err := sql.Open("sqlite", authPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM auth_password_change_journal").Scan(&count); err != nil || count != 0 {
		t.Fatalf("journal not completed: %d %v", count, err)
	}
	data, err := os.ReadFile(content[entries[0].ID])
	if err != nil || !bytes.Equal(data, []byte("ciphertext bytes stay untouched")) {
		t.Fatalf("content changed: %v", err)
	}
	// Existing recovery-envelope replay uses the same required durable rewrite.
	old, _ := json.Marshal(mustDecodeEnvelope(t, plan.Updates[0].OldEnvelope))
	newBytes, _ := json.Marshal(mustDecodeEnvelope(t, plan.Updates[0].NewEnvelope))
	requireEnvelopeRewrite(t, state.envelopePath, func() error { return applyRecoveryEnvelope(state.envelopePath, old, newBytes, true, true, false) })
}
