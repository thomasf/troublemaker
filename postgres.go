package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// DefaultPostgresTimeout caps how long the startup PostgreSQL connectivity
// check may run.
const DefaultPostgresTimeout = 10 * time.Second

// Configured reports whether a PostgreSQL DSN or any individual POSTGRES_*
// connection setting was provided, which is the signal to attempt a connection
// at startup.
func (f Flags) postgresConfigured() bool {
	return f.PostgresDSN != "" ||
		f.PostgresHost != "" ||
		f.PostgresPort != "" ||
		f.PostgresUser != "" ||
		f.PostgresPassword != "" ||
		f.PostgresDBName != "" ||
		f.PostgresSSLMode != ""
}

// applySSLMode overrides the TLS configuration of cfg according to a libpq
// sslmode value. Empty mode leaves whatever the DSN already produced untouched.
func applySSLMode(cfg *pgx.ConnConfig, mode string) {
	switch mode {
	case "disable":
		cfg.TLSConfig = nil
	case "allow", "prefer", "require":
		// Encrypt but do not verify the server certificate.
		cfg.TLSConfig = &tls.Config{ServerName: cfg.Host, InsecureSkipVerify: true}
	case "verify-ca", "verify-full":
		cfg.TLSConfig = &tls.Config{ServerName: cfg.Host}
	}
}

// postgresConfig builds the effective pgx connection config. The DSN (URL or
// libpq keyword/value form) is the base; any individual POSTGRES_* setting that
// is provided overrides the corresponding part of the DSN. The DSN may be empty,
// in which case the connection is described entirely by the individual settings
// (and pgx defaults / PG* env vars).
func (f Flags) postgresConfig() (*pgx.ConnConfig, error) {
	cfg, err := pgx.ParseConfig(string(f.PostgresDSN))
	if err != nil {
		return nil, err
	}
	if f.PostgresHost != "" {
		cfg.Host = f.PostgresHost
	}
	if f.PostgresPort != "" {
		p, err := strconv.ParseUint(f.PostgresPort, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("invalid postgres.port %q: %w", f.PostgresPort, err)
		}
		cfg.Port = uint16(p)
	}
	if f.PostgresUser != "" {
		cfg.User = f.PostgresUser
	}
	if f.PostgresPassword != "" {
		cfg.Password = string(f.PostgresPassword)
	}
	if f.PostgresDBName != "" {
		cfg.Database = f.PostgresDBName
	}
	if f.PostgresSSLMode != "" {
		applySSLMode(cfg, f.PostgresSSLMode)
	}
	return cfg, nil
}

// connectPostgres builds the connection config, connects to the server and
// verifies connectivity by running "select version()". Any error is returned to
// the caller. The DSN and password may contain secrets, so only the parsed
// host/port/user/dbname are logged, never the raw DSN.
func connectPostgres(ctx context.Context, flags Flags, logger zerolog.Logger) error {
	cfg, err := flags.postgresConfig()
	if err != nil {
		return err
	}

	logger.Info().
		Str("host", cfg.Host).
		Uint16("port", cfg.Port).
		Str("user", cfg.User).
		Str("dbname", cfg.Database).
		Msg("checking postgresql connectivity")

	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	var version string
	if err := conn.QueryRow(ctx, "select version()").Scan(&version); err != nil {
		return err
	}

	logger.Info().
		Str("host", cfg.Host).
		Str("dbname", cfg.Database).
		Str("server_version", version).
		Msg("connected to postgresql")

	return nil
}

// checkPostgres runs the startup connectivity check and, if it fails and
// postgres.crash.on.error is set, crashes the process. This lets troublemaker
// simulate a service that cannot start because a required PostgreSQL dependency
// is unavailable.
func checkPostgres(flags Flags, logger zerolog.Logger) {
	if !flags.postgresConfigured() {
		if flags.PostgresCrashOnError {
			logger.Fatal().Msg("crashing on startup: postgres.crash.on.error is set but no POSTGRES_* credentials are configured")
		}
		return
	}

	timeout := flags.PostgresTimeout
	if timeout <= 0 {
		timeout = DefaultPostgresTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := connectPostgres(ctx, flags, logger); err != nil {
		logger.Error().Err(err).Msg("postgresql connection failed")
		if flags.PostgresCrashOnError {
			logger.Fatal().Err(err).Msg("crashing on startup: postgresql unreachable and postgres.crash.on.error is set")
		}
		return
	}

	logger.Info().Msg("postgresql connection ok")
}
