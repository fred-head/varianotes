package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// lockAuthJournal coordinates initialization and credential/envelope transitions
// across Service instances and processes. The separate, empty SQLite database
// is only a lock: holding auth.db's writer lock here would deadlock the durable
// journal writes that the owner must perform. Never unlink this file on release.
// SQLite releases ownership on connection close or process death; no persisted
// owner flag can strand recovery after a crash.
func lockAuthJournal(ctx context.Context, metadataPath string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Resolve existing database/parent aliases so they join the same lock. As
	// with SQLite's own WAL files, hard-link aliases of a database are unsupported.
	canonical, err := filepath.EvalSymlinks(metadataPath)
	if errors.Is(err, os.ErrNotExist) {
		parent, parentErr := filepath.EvalSymlinks(filepath.Dir(metadataPath))
		if parentErr != nil {
			return nil, fmt.Errorf("resolve auth journal lock directory: %w", parentErr)
		}
		canonical, err = filepath.Join(parent, filepath.Base(metadataPath)), nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve auth journal lock: %w", err)
	}
	filename := canonical + ".journal-lock"
	root, err := os.OpenRoot(filepath.Dir(filename))
	if err != nil {
		return nil, err
	}
	name := filepath.Base(filename)
	if info, err := root.Lstat(name); err == nil && !info.Mode().IsRegular() {
		_ = root.Close()
		return nil, errors.New("auth journal lock is not a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = root.Close()
		return nil, err
	}
	file, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o600)
	_ = root.Close()
	if err != nil {
		return nil, fmt.Errorf("open auth journal lock: %w", err)
	}
	err = file.Chmod(0o600)
	closeErr := file.Close()
	if err := errors.Join(err, closeErr); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filename)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	release := func() {
		// The connection must retain its lock through the owner's cleanup even
		// if the request was canceled. A canceled BeginTx context would release
		// a database/sql transaction while its callback might still be running.
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		_ = conn.Close()
		_ = db.Close()
	}
	for {
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		// Explicit SQL, rather than BeginTx(ctx), keeps cancellation from
		// asynchronously rolling back an acquired lock. Each attempt is
		// nonblocking; cancellation is checked again before granting ownership.
		_, err = conn.ExecContext(context.Background(), "BEGIN IMMEDIATE")
		if err == nil {
			if err := ctx.Err(); err != nil {
				release()
				return nil, err
			}
			return release, nil
		}
		if !isSQLiteBusy(err) {
			release()
			return nil, fmt.Errorf("acquire auth journal lock: %w", err)
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			release()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
