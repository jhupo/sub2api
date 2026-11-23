//go:build integration

package repository

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/alicebob/miniredis/v2"
	redisclient "github.com/redis/go-redis/v9"
)

// This mode is for a disposable local database. The suite truncates its tables.
func runExternalPostgresIntegration(m *testing.M, dsn string) (int, error) {
	if err := validateExternalPostgresIntegrationDSN(dsn); err != nil {
		return 1, err
	}
	ctx := context.Background()
	var err error
	integrationDB, err = openSQLWithRetry(ctx, dsn, 30*time.Second)
	if err != nil {
		return 1, err
	}
	defer func() { _ = integrationDB.Close() }()
	if err = ApplyMigrations(ctx, integrationDB); err != nil {
		return 1, err
	}
	integrationDSN = dsn
	integrationEntClient = dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, integrationDB)))
	defer func() { _ = integrationEntClient.Close() }()
	redisServer, err := miniredis.Run()
	if err != nil {
		return 1, err
	}
	defer redisServer.Close()
	integrationRedis = redisclient.NewClient(&redisclient.Options{Addr: redisServer.Addr()})
	defer func() { _ = integrationRedis.Close() }()
	return m.Run(), nil
}

func validateExternalPostgresIntegrationDSN(dsn string) error {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("invalid external integration DSN: %w", err)
	}
	if strings.Contains(dsn, "#") {
		return fmt.Errorf("external integration DSN must not contain a fragment")
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || (host != "localhost" && (ip == nil || !ip.IsLoopback())) || !strings.HasPrefix(strings.TrimPrefix(parsed.Path, "/"), "sub2api_test") {
		return fmt.Errorf("external integration DSN must target a local sub2api_test database")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return fmt.Errorf("invalid external integration DSN query: %w", err)
	}
	for key, values := range query {
		// lib/pq lets URL query options override the authority and database path.
		if key != "sslmode" && key != "TimeZone" {
			return fmt.Errorf("external integration DSN query option %q is not allowed", key)
		}
		if len(values) != 1 || values[0] == "" {
			return fmt.Errorf("external integration DSN query option %q must have one nonempty value", key)
		}
	}
	return nil
}

func TestValidateExternalPostgresIntegrationDSN(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dsn     string
		wantErr bool
	}{
		{name: "localhost", dsn: "postgres://localhost/sub2api_test"},
		{name: "ipv4 loopback", dsn: "postgresql://postgres:password@127.0.0.1:55437/sub2api_test_upstream?sslmode=disable&TimeZone=Asia%2FShanghai"},
		{name: "ipv6 loopback", dsn: "postgres://[::1]:55437/sub2api_test_upstream?TimeZone=UTC"},
		{name: "wrong scheme", dsn: "http://localhost/sub2api_test", wantErr: true},
		{name: "remote host", dsn: "postgres://example.com/sub2api_test", wantErr: true},
		{name: "nonloopback ip", dsn: "postgres://192.0.2.1/sub2api_test", wantErr: true},
		{name: "missing host", dsn: "postgres:///sub2api_test", wantErr: true},
		{name: "non-test database", dsn: "postgres://localhost/sub2api", wantErr: true},
		{name: "host override", dsn: "postgres://localhost/sub2api_test?host=z.remote.example", wantErr: true},
		{name: "encoded host override", dsn: "postgres://localhost/sub2api_test?%68ost=z.remote.example", wantErr: true},
		{name: "database override", dsn: "postgres://localhost/sub2api_test?dbname=zproduction", wantErr: true},
		{name: "port override", dsn: "postgres://localhost/sub2api_test?port=5433", wantErr: true},
		{name: "user override", dsn: "postgres://localhost/sub2api_test?user=postgres", wantErr: true},
		{name: "password override", dsn: "postgres://localhost/sub2api_test?password=password", wantErr: true},
		{name: "options override", dsn: "postgres://localhost/sub2api_test?options=-csearch_path%3Dpublic", wantErr: true},
		{name: "search path override", dsn: "postgres://localhost/sub2api_test?search_path=public", wantErr: true},
		{name: "unknown option", dsn: "postgres://localhost/sub2api_test?connect_timeout=1", wantErr: true},
		{name: "duplicate option", dsn: "postgres://localhost/sub2api_test?sslmode=disable&sslmode=require", wantErr: true},
		{name: "empty option", dsn: "postgres://localhost/sub2api_test?TimeZone=", wantErr: true},
		{name: "malformed query escape", dsn: "postgres://localhost/sub2api_test?sslmode=%ZZ", wantErr: true},
		{name: "malformed query separator", dsn: "postgres://localhost/sub2api_test?sslmode=disable;host=z.remote.example", wantErr: true},
		{name: "malformed path escape", dsn: "postgres://localhost/sub2api_test%ZZ", wantErr: true},
		{name: "fragment", dsn: "postgres://localhost/sub2api_test#ignored", wantErr: true},
		{name: "empty fragment", dsn: "postgres://localhost/sub2api_test#", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateExternalPostgresIntegrationDSN(tc.dsn)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateExternalPostgresIntegrationDSN() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}
