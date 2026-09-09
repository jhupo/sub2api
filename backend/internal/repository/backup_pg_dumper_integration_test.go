//go:build integration

package repository

import (
	"context"
	"io"
	"os/exec"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestPgDumperSerializesWithPostgresMigrations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	observer, err := integrationDB.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = observer.Close() }()
	dumper, ok := NewPgDumper(&config.Config{}, integrationDB).(*PgDumper)
	require.True(t, ok)
	// The child only supplies stdout; lock ownership is tested against real PG.
	dumper.commandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "go", "version")
	}
	reader, err := dumper.Dump(ctx)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	var acquired bool
	require.NoError(t, observer.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", migrationsAdvisoryLockID).Scan(&acquired))
	defer func() { _ = pgAdvisoryUnlock(context.Background(), observer) }()
	require.False(t, acquired, "a backup must exclude concurrent schema migrations")
	_, err = io.Copy(io.Discard, reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.NoError(t, observer.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", migrationsAdvisoryLockID).Scan(&acquired))
	require.True(t, acquired, "migration lock must be available after the backup closes")
}
