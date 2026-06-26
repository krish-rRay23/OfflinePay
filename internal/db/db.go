package db

import (
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type DB struct {
	*sql.DB
}

func Connect(dbURL string) (*DB, error) {
	// Simple retry loop to handle cases where database is starting up in compose
	var conn *sql.DB
	var err error

	for i := 0; i < 10; i++ {
		conn, err = sql.Open("postgres", dbURL)
		if err == nil {
			err = conn.Ping()
			if err == nil {
				break
			}
		}
		slog.Warn("database connection failed, retrying in 2 seconds...", "attempt", i+1, "error", err)
		time.Sleep(2 * time.Second)
	}

	if err != nil {
		return nil, fmt.Errorf("could not connect to database after retries: %w", err)
	}

	// Set connection pool limits
	conn.SetMaxOpenConns(25)
	conn.SetMaxIdleConns(5)
	conn.SetConnMaxLifetime(5 * time.Minute)

	db := &DB{conn}

	// Automatically run migrations on start
	if err := db.runMigrations(); err != nil {
		return nil, fmt.Errorf("migration failure: %w", err)
	}

	slog.Info("database connected and migrations successfully applied")
	return db, nil
}

func (db *DB) runMigrations() error {
	slog.Info("running database migrations...")

	// Read all files from embedded migrations directory
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("failed to read embedded migrations directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		err := func() error {
			slog.Info("applying migration file", "name", entry.Name())
			migrationData, err := migrationFiles.ReadFile("migrations/" + entry.Name())
			if err != nil {
				return fmt.Errorf("failed to read migration file %s: %w", entry.Name(), err)
			}

			tx, err := db.Begin()
			if err != nil {
				return err
			}
			defer tx.Rollback()

			// Parse migration SQL script and split statements
			queries := string(migrationData)
			statements := strings.Split(queries, ";")

			for _, stmt := range statements {
				stmt = strings.TrimSpace(stmt)
				if stmt == "" {
					continue
				}
				if _, err := tx.Exec(stmt); err != nil {
					return fmt.Errorf("failed to execute migration statement: %w; stmt: %s", err, stmt)
				}
			}

			return tx.Commit()
		}()

		if err != nil {
			return fmt.Errorf("migration failure in %s: %w", entry.Name(), err)
		}
	}

	return nil
}
