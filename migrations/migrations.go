// Package migrations applies the versioned OmniRelay PostgreSQL schema.
package migrations

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.sql
var files embed.FS

const migrationLock int64 = 0x4f6d6e6952656c61

type migration struct {
	version int
	digest  string
	sql     string
}

func readMigrations() ([]migration, error) {
	entries, err := fs.Glob(files, "*.sql")
	if err != nil || len(entries) == 0 {
		return nil, errors.New("no embedded schema migrations")
	}
	sort.Strings(entries)
	result := make([]migration, 0, len(entries))
	for i, path := range entries {
		prefix, _, found := strings.Cut(path, "_")
		version, err := strconv.Atoi(prefix)
		if !found || err != nil || version != i+1 {
			return nil, fmt.Errorf("invalid migration sequence at %s", path)
		}
		data, err := files.ReadFile(path)
		if err != nil || len(data) == 0 {
			return nil, fmt.Errorf("migration %d is empty or unreadable", version)
		}
		sum := sha256.Sum256(data)
		result = append(result, migration{version, hex.EncodeToString(sum[:]), string(data)})
	}
	return result, nil
}

// Apply runs transaction-safe migrations with a transaction-scoped database lock.
func Apply(ctx context.Context, pool *pgxpool.Pool) error {
	changes, err := readMigrations()
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '5s'"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLock); err != nil {
			return fmt.Errorf("acquire migration lock: %w", err)
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS omnirelay_schema_migrations (
			version integer PRIMARY KEY CHECK (version > 0),
			checksum text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
			return err
		}
		var latest int
		if err := tx.QueryRow(ctx, "SELECT COALESCE(max(version), 0) FROM omnirelay_schema_migrations").Scan(&latest); err != nil {
			return err
		}
		if latest > len(changes) {
			return errors.New("database already has migrations newer than this release")
		}
		for _, change := range changes {
			if change.version <= latest {
				if err := checkMigration(ctx, tx, change); err != nil {
					return err
				}
				continue
			}
			if _, err := tx.Exec(ctx, change.sql); err != nil {
				return fmt.Errorf("apply schema migration %d: %w", change.version, err)
			}
			if _, err := tx.Exec(ctx, "INSERT INTO omnirelay_schema_migrations (version, checksum) VALUES ($1, $2)",
				change.version, change.digest); err != nil {
				return err
			}
		}
		return nil
	})
}

// Check requires every migration used by this binary. Compatible newer migrations
// can coexist with the old binary during a rolling upgrade.
func Check(ctx context.Context, pool *pgxpool.Pool) error {
	changes, err := readMigrations()
	if err != nil {
		return err
	}
	for _, change := range changes {
		if err := checkMigration(ctx, pool, change); err != nil {
			return err
		}
	}
	return nil
}

func checkMigration(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, change migration) error {
	var digest string
	if err := db.QueryRow(ctx, "SELECT checksum FROM omnirelay_schema_migrations WHERE version=$1", change.version).Scan(&digest); err != nil {
		return fmt.Errorf("required schema migration %d cannot be verified: %w", change.version, err)
	}
	if digest != change.digest {
		return fmt.Errorf("schema migration %d checksum mismatch", change.version)
	}
	return nil
}
