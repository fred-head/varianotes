package app

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fred-head/varianotes/internal/auth"
	"github.com/fred-head/varianotes/internal/encryption"
	"github.com/fred-head/varianotes/internal/files"
)

// Model an already unlocked second session saving with the unchanged data key
// after envelope validation / the first envelope update in another session.
func credentialConcurrentWriter(t *testing.T, registry string, entry notebookRecord) func(string) {
	t.Helper()
	state, err := inspectVaultProtection(entry, registry)
	if err != nil {
		t.Fatal(err)
	}
	envelopes, err := encryption.NewVaultKeyEnvelopeStore(state.envelopePath)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := envelopes.Load()
	if err != nil {
		t.Fatal(err)
	}
	key, err := encryption.DecryptVaultKey("original vault password", envelope)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clear(key) })
	store, err := encryption.NewCatalogStore(state.catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := store.Load(key, registeredCatalogID(entry.ID))
	if err != nil {
		t.Fatal(err)
	}
	catalog.FolderRules = append(catalog.FolderRules, encryption.FolderProtectionRule{Path: "private"})
	if err := store.Save(catalog, key); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(entry.LocalPath, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	repository, err := files.NewRepository(entry.LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := encryption.NewTransactionJournal(entry.LocalPath, store)
	if err != nil {
		t.Fatal(err)
	}
	create, _, err := repository.PrepareMarkdownCreateWithCatalog("private/Note.md", catalog, key, store, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := create.Commit(nil); err != nil {
		t.Fatal(err)
	}
	manager := encryption.NewKeyManager()
	t.Cleanup(func() { _ = manager.Close() })
	for _, sessionName := range []string{"credential session", "saving session"} {
		session := sha256.Sum256([]byte(sessionName))
		if err := manager.Unlock(entry.ID, session[:], key); err != nil {
			t.Fatal(err)
		}
	}
	return func(content string) {
		session := sha256.Sum256([]byte("saving session"))
		lease, err := manager.BeginUse(entry.ID, session[:])
		if err != nil {
			t.Fatal(err)
		}
		var operation *files.PreparedMarkdownOperation
		if err := lease.UseKey(func(key []byte) error {
			catalog, err := store.Load(key, registeredCatalogID(entry.ID))
			if err != nil {
				return err
			}
			note, _, err := repository.ReadMarkdownWithCatalog("private/Note.md", catalog, key)
			if err != nil {
				return err
			}
			operation, _, err = repository.PrepareMarkdownWriteWithCatalog("private/Note.md", content, note.Version, catalog, key, store, journal)
			return err
		}); err != nil {
			lease.Close()
			t.Fatal(err)
		}
		if err := lease.Commit(func() error { return operation.Commit(nil) }); err != nil {
			operation.Abort()
			t.Fatal(err)
		}
	}
}

func TestPasswordJournalRestartAllowsConcurrentCatalogSave(t *testing.T) {
	for _, phase := range []string{"prepared", "committed"} {
		t.Run(phase, func(t *testing.T) {
			registry, entries, _ := passwordChangeFixture(t, 2, false)
			write := credentialConcurrentWriter(t, registry, entries[0])
			coordinator := NewPasswordChangeCoordinator(registry)
			authPath := filepath.Join(filepath.Dir(registry), "auth.db")
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
			encoded, err := coordinator.PreparePasswordChange(t.Context(), "original vault password", "replacement vault password")
			if err != nil {
				t.Fatal(err)
			}
			plan, err := decodePasswordChangePlan(encoded)
			if err != nil {
				t.Fatal(err)
			}
			state, err := inspectVaultProtection(entries[0], registry)
			if err != nil {
				t.Fatal(err)
			}
			store, err := encryption.NewVaultKeyEnvelopeStore(state.envelopePath)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Save(mustDecodeEnvelope(t, plan.Updates[0].NewEnvelope)); err != nil {
				t.Fatal(err)
			}
			write("concurrent save survives password journal")
			contentBefore, err := os.ReadFile(filepath.Join(entries[0].LocalPath, "private", "Note.md.age"))
			if err != nil {
				t.Fatal(err)
			}
			if err := insertPasswordChangeJournal(t, authPath, phase, encoded); err != nil {
				t.Fatal(err)
			}
			if err := service.Close(); err != nil {
				t.Fatal(err)
			}
			restarted, err := auth.Open(t.Context(), config, passwordChangeLogger())
			if err != nil {
				t.Fatalf("catalog save permanently blocked auth startup: %v", err)
			}
			defer restarted.Close()
			password := "original vault password"
			if phase == "committed" {
				password = "replacement vault password"
			}
			assertVaultPasswords(t, entries, registry, password)
			contentAfter, err := os.ReadFile(filepath.Join(entries[0].LocalPath, "private", "Note.md.age"))
			if err != nil || !bytes.Equal(contentBefore, contentAfter) {
				t.Fatalf("envelope recovery rewrote content: %v", err)
			}
		})
	}
}

func TestRecoveryCodeJournalRestartAllowsConcurrentCatalogSave(t *testing.T) {
	for _, phase := range []string{"prepared", "committed"} {
		t.Run(phase, func(t *testing.T) {
			registry, entries, _ := passwordChangeFixture(t, 2, false)
			write := credentialConcurrentWriter(t, registry, entries[0])
			service := openRecoveryTestService(t, registry)
			coordinator := NewRecoveryCodeCoordinator(registry)
			code := "VNRC1-A1B2C3D4-A1B2C3D4-A1B2C3D4-A1B2C3D4-A1B2C3D4-A1B2C3D4-A1B2C3D4-A1B2C3D4"
			encoded, err := coordinator.PrepareRecoveryCode(t.Context(), "original vault password", code)
			if err != nil {
				t.Fatal(err)
			}
			if err := coordinator.VerifyRecoveryPlan(t.Context(), encoded, "", code); err != nil {
				t.Fatal(err)
			}
			plan, err := decodeVaultRecoveryPlan(encoded)
			if err != nil {
				t.Fatal(err)
			}
			state, err := inspectVaultProtection(entries[0], registry)
			if err != nil {
				t.Fatal(err)
			}
			first := plan.Updates[0]
			if err := applyRecoveryEnvelope(state.recoveryEnvelopePath, nil, first.NewRecoveryEnvelope, false, true, true); err != nil {
				t.Fatal(err)
			}
			write("save after last recovery code validation")
			before, err := os.ReadFile(filepath.Join(entries[0].LocalPath, "private", "Note.md.age"))
			if err != nil {
				t.Fatal(err)
			}
			payload, err := json.Marshal(map[string]any{"operation": "code", "plan": encoded})
			if err != nil {
				t.Fatal(err)
			}
			database, err := sql.Open("sqlite", filepath.Join(filepath.Dir(registry), "auth.db"))
			if err != nil {
				t.Fatal(err)
			}
			_, err = database.Exec(`INSERT INTO auth_encryption_recovery_journal(id,phase,payload,created_at) VALUES(1,?,?,?)`, phase, payload, time.Now().UTC().Format(time.RFC3339Nano))
			if err != nil {
				database.Close()
				t.Fatal(err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			if err := service.Close(); err != nil {
				t.Fatal(err)
			}
			service = openRecoveryTestService(t, registry)
			defer service.Close()
			if phase == "committed" {
				assertRecoveryCodeUnlocksVaults(t, entries, registry, code)
			} else {
				for _, entry := range entries {
					state, err := inspectVaultProtection(entry, registry)
					if err != nil || state.hasRecoveryEnvelope {
						t.Fatalf("unconfirmed code remained: %v", err)
					}
				}
			}
			assertVaultPasswords(t, entries, registry, "original vault password")
			after, err := os.ReadFile(filepath.Join(entries[0].LocalPath, "private", "Note.md.age"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("recovery rewrote saved content: %v", err)
			}
		})
	}
}
