package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var (
	db           *sql.DB
	dbMaxRetries = 15
	dbRetryDelay = 2 * time.Second
	dbDriver     = "pgx"
)

func dbURL() string {
	host := os.Getenv("DB_HOST")
	if host == "" {
		host = "db"
	}
	port := os.Getenv("DB_PORT")
	if port == "" {
		port = "5432"
	}
	user := os.Getenv("DB_USER")
	if user == "" {
		user = os.Getenv("POSTGRES_USER")
	}
	pass := os.Getenv("DB_PASSWORD")
	if pass == "" {
		pass = os.Getenv("POSTGRES_PASSWORD")
	}
	name := os.Getenv("DB_NAME")
	if name == "" {
		name = os.Getenv("POSTGRES_DB")
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s", user, pass, host, port, name)
}

func initDB() error {
	var err error
	db, err = sql.Open(dbDriver, dbURL())
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)
	for i := 0; i < dbMaxRetries; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err = db.PingContext(ctx)
		cancel()
		if err == nil {
			return nil
		}
		time.Sleep(dbRetryDelay)
	}
	return err
}

// pingDBFn es sobreescribible en tests (sin Postgres real).
var pingDBFn = func(ctx context.Context) error {
	if db == nil {
		return fmt.Errorf("base de datos no inicializada")
	}
	return db.PingContext(ctx)
}

func pingDB(ctx context.Context) error {
	return pingDBFn(ctx)
}
