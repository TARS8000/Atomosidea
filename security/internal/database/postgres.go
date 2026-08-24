package database

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/atmosidea/sfsp/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB holds the database connection pool
var DB *pgxpool.Pool

// Connect initializes the database connection pool
func Connect(cfg config.Config) error {
	var err error
	connStr := cfg.DatabaseURL

	err = config.Retry(5, 2*time.Second, func() error {
		DB, err = pgxpool.New(context.Background(), connStr)
		if err != nil {
			return fmt.Errorf("unable to create connection pool: %w", err)
		}
		return DB.Ping(context.Background())
	})

	if err != nil {
		return fmt.Errorf("unable to connect to database after retries: %w", err)
	}

	if err := ensureTargetServiceColumn(); err != nil {
		return err
	}

	log.Println("Successfully connected to the database.")
	return nil
}

func ensureTargetServiceColumn() error {
	var exists bool
	if err := DB.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'files'
			  AND column_name = 'target_service'
		)
	`).Scan(&exists); err != nil {
		return fmt.Errorf("unable to check target_service column: %w", err)
	}

	if exists {
		return nil
	}

	if _, err := DB.Exec(context.Background(), `ALTER TABLE files ADD COLUMN target_service VARCHAR(50) NOT NULL DEFAULT 'other'`); err != nil {
		return fmt.Errorf("unable to add target_service column: %w", err)
	}

	log.Println("Added missing files.target_service column.")
	return nil
}

// Close closes the database connection pool
func Close() {
	if DB != nil {
		DB.Close()
	}
}
