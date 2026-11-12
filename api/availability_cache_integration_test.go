//go:build integration

package main

// Prueba de inicialización contra un Redis real.
//
// Reutiliza las variables del resto de las pruebas de integración:
//
//	REDIS_ADDR      host:puerto (por defecto localhost:6379)
//	REDIS_PASSWORD  password opcional
//	REDIS_DB        base lógica (por defecto 15)
//
// Solo comprueba la construcción y el Ping; no escribe claves.

import (
	"context"
	"net"
	"os"
	"strconv"
	"testing"
)

func TestNewAvailabilityCacheFromEnvIntegration(t *testing.T) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("REDIS_ADDR = %q no es host:puerto: %v", addr, err)
	}

	db := 15
	if raw := os.Getenv("REDIS_DB"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("REDIS_DB = %q no es un número", raw)
		}
		db = parsed
	}

	t.Setenv("REDIS_HOST", host)
	t.Setenv("REDIS_PORT", port)
	t.Setenv("REDIS_PASSWORD", os.Getenv("REDIS_PASSWORD"))
	t.Setenv("REDIS_DB", strconv.Itoa(db))

	runtime, err := newAvailabilityCacheFromEnv(context.Background(), nil)
	if err != nil {
		t.Fatalf("newAvailabilityCacheFromEnv() error = %v", err)
	}
	if runtime.Cache == nil {
		t.Fatal("Cache = nil")
	}
	if runtime.Addr != addr {
		t.Fatalf("Addr = %q, want %q", runtime.Addr, addr)
	}
	if runtime.DB != db {
		t.Fatalf("DB = %d, want %d", runtime.DB, db)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
