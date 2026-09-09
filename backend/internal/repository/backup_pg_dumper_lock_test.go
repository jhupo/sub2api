//go:build !integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestPgDumpHelperProcess(t *testing.T) {
	mode := os.Getenv("SUB2API_TEST_PG_DUMP")
	if mode == "" {
		return
	}
	if mode == "wait" {
		time.Sleep(time.Minute)
	}
	if mode == "fail" {
		os.Exit(7)
	}
	fmt.Fprint(os.Stdout, "SELECT 1;\n")
	os.Exit(0)
}

func newLockTestDumper(t *testing.T, mode string) (*PgDumper, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	d, ok := NewPgDumper(&config.Config{}, db).(*PgDumper)
	require.True(t, ok)
	testExecutable, err := os.Executable()
	require.NoError(t, err)
	d.commandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, testExecutable, "-test.run=^TestPgDumpHelperProcess$")
		cmd.Env = append(cmd.Environ(), "SUB2API_TEST_PG_DUMP="+mode)
		return cmd
	}
	return d, mock
}

func expectDumpLock(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`SELECT pg_try_advisory_lock\(\$1\)`).
		WithArgs(migrationsAdvisoryLockID).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
}

func expectDumpUnlock(mock sqlmock.Sqlmock) {
	mock.ExpectExec(`SELECT pg_advisory_unlock\(\$1\)`).
		WithArgs(migrationsAdvisoryLockID).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func TestPgDumperHoldsMigrationLockUntilClose(t *testing.T) {
	d, mock := newLockTestDumper(t, "ok")
	expectDumpLock(mock)
	reader, err := d.Dump(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close() })
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, "SELECT 1;\n", string(data))
	require.NoError(t, mock.ExpectationsWereMet())
	// No unlock is expected before the process has been consumed and closed.
	expectDumpUnlock(mock)
	require.NoError(t, reader.Close())
	require.NoError(t, reader.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgDumperReleasesLockOnCommandFailures(t *testing.T) {
	for _, mode := range []string{"start", "pipe", "fail", "wait"} {
		t.Run(mode, func(t *testing.T) {
			d, mock := newLockTestDumper(t, mode)
			if mode == "start" || mode == "pipe" {
				d.commandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
					cmd := exec.CommandContext(ctx, filepath.Join(t.TempDir(), "no-such-pg-dump"))
					if mode == "pipe" {
						cmd.Stdout = io.Discard
					}
					return cmd
				}
			}
			expectDumpLock(mock)
			expectDumpUnlock(mock)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			reader, err := d.Dump(ctx)
			if mode == "start" || mode == "pipe" {
				require.Error(t, err)
				require.Nil(t, reader)
			} else {
				require.NoError(t, err)
				if mode == "fail" {
					_, err = io.ReadAll(reader)
					require.NoError(t, err)
				}
				start := time.Now()
				closeErr := reader.Close()
				require.Error(t, closeErr)
				require.Equal(t, closeErr, reader.Close(), "close must release only once")
				require.Less(t, time.Since(start), 5*time.Second, "an abandoned dump must be killed")
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPgDumperDiscardsConnectionIfUnlockFails(t *testing.T) {
	d, mock := newLockTestDumper(t, "ok")
	expectDumpLock(mock)
	mock.ExpectExec(`SELECT pg_advisory_unlock\(\$1\)`).
		WithArgs(migrationsAdvisoryLockID).
		WillReturnError(errors.New("connection lost"))
	mock.ExpectClose()
	reader, err := d.Dump(context.Background())
	require.NoError(t, err)
	_, err = io.ReadAll(reader)
	require.NoError(t, err)
	require.ErrorContains(t, reader.Close(), "release backup migration lock")
	require.Zero(t, d.db.Stats().OpenConnections)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgDumperDoesNotStartWhileMigrationLocked(t *testing.T) {
	d, mock := newLockTestDumper(t, "ok")
	d.commandContext = func(context.Context, string, ...string) *exec.Cmd {
		t.Fatal("pg_dump must not start before the migration lock is acquired")
		return nil
	}
	mock.ExpectQuery(`SELECT pg_try_advisory_lock\(\$1\)`).
		WithArgs(migrationsAdvisoryLockID).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(false))
	mock.ExpectClose()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	reader, err := d.Dump(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, reader)
	require.NoError(t, mock.ExpectationsWereMet())
}
