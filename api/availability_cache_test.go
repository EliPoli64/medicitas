package main

// Pruebas de configuración e inicialización de la caché de disponibilidad.
//
// No requieren Redis: verifican el parseo de variables de entorno y que una
// configuración inválida se distinga de una caída de conexión.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"medicitas/api/internal/availabilitycache"
)

func TestRedisConfigFromEnvDefaults(t *testing.T) {
	t.Setenv("REDIS_HOST", "")
	t.Setenv("REDIS_PORT", "")
	t.Setenv("REDIS_PASSWORD", "")
	t.Setenv("REDIS_DB", "")

	cfg, err := redisConfigFromEnv()
	if err != nil {
		t.Fatalf("redisConfigFromEnv() error = %v", err)
	}
	if cfg.Addr != "localhost:6379" {
		t.Fatalf("Addr = %q, want localhost:6379", cfg.Addr)
	}
	if cfg.DB != 0 {
		t.Fatalf("DB = %d, want 0", cfg.DB)
	}
	if cfg.Password != "" {
		t.Fatalf("Password = %q, want empty", cfg.Password)
	}
	if cfg.DialTimeout != redisDialTimeout || cfg.ReadTimeout != redisReadTimeout || cfg.WriteTimeout != redisWriteTimeout {
		t.Fatalf("timeouts = (%v, %v, %v)", cfg.DialTimeout, cfg.ReadTimeout, cfg.WriteTimeout)
	}
}

func TestRedisConfigFromEnvCustom(t *testing.T) {
	t.Setenv("REDIS_HOST", "  redis  ")
	t.Setenv("REDIS_PORT", " 6380 ")
	t.Setenv("REDIS_PASSWORD", "secreto")
	t.Setenv("REDIS_DB", "3")

	cfg, err := redisConfigFromEnv()
	if err != nil {
		t.Fatalf("redisConfigFromEnv() error = %v", err)
	}
	if cfg.Addr != "redis:6380" {
		t.Fatalf("Addr = %q, want redis:6380", cfg.Addr)
	}
	if cfg.DB != 3 {
		t.Fatalf("DB = %d, want 3", cfg.DB)
	}
	if cfg.Password != "secreto" {
		t.Fatalf("Password = %q, want secreto", cfg.Password)
	}
}

func TestRedisConfigFromEnvIPv6(t *testing.T) {
	t.Setenv("REDIS_HOST", "::1")
	t.Setenv("REDIS_PORT", "6379")

	cfg, err := redisConfigFromEnv()
	if err != nil {
		t.Fatalf("redisConfigFromEnv() error = %v", err)
	}
	if cfg.Addr != "[::1]:6379" {
		t.Fatalf("Addr = %q, want [::1]:6379", cfg.Addr)
	}
}

func TestRedisConfigFromEnvInvalid(t *testing.T) {
	tests := []struct {
		name  string
		env   map[string]string
		field string
	}{
		{
			name:  "puerto no numérico",
			env:   map[string]string{"REDIS_PORT": "abc"},
			field: "REDIS_PORT",
		},
		{
			name:  "puerto cero",
			env:   map[string]string{"REDIS_PORT": "0"},
			field: "REDIS_PORT",
		},
		{
			name:  "puerto fuera de rango",
			env:   map[string]string{"REDIS_PORT": "70000"},
			field: "REDIS_PORT",
		},
		{
			name:  "DB no numérica",
			env:   map[string]string{"REDIS_DB": "abc"},
			field: "REDIS_DB",
		},
		{
			name:  "DB negativa",
			env:   map[string]string{"REDIS_DB": "-1"},
			field: "REDIS_DB",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("REDIS_HOST", "localhost")
			t.Setenv("REDIS_PORT", "6379")
			t.Setenv("REDIS_PASSWORD", "")
			t.Setenv("REDIS_DB", "")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			_, err := redisConfigFromEnv()
			if !errors.Is(err, availabilitycache.ErrInvalidConfig) {
				t.Fatalf("error = %v, want ErrInvalidConfig", err)
			}
			if !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("error = %v, want it to mention %s", err, tt.field)
			}
		})
	}
}

func TestNewAvailabilityCacheFromEnvInvalidConfig(t *testing.T) {
	t.Setenv("REDIS_HOST", "localhost")
	t.Setenv("REDIS_PORT", "no-es-un-puerto")
	t.Setenv("REDIS_PASSWORD", "")
	t.Setenv("REDIS_DB", "")

	runtime, err := newAvailabilityCacheFromEnv(context.Background())
	if err == nil {
		_ = runtime.Close()
		t.Fatal("newAvailabilityCacheFromEnv() error = nil")
	}
	if runtime != nil {
		t.Fatal("runtime debe ser nil cuando la configuración es inválida")
	}
	if !isAvailabilityCacheConfigError(err) {
		t.Fatalf("error = %v, want classified as config error", err)
	}
}

func TestNewAvailabilityCacheFromEnvUnreachable(t *testing.T) {
	// Puerto 1 en loopback: conexión rechazada de inmediato.
	t.Setenv("REDIS_HOST", "127.0.0.1")
	t.Setenv("REDIS_PORT", "1")
	t.Setenv("REDIS_PASSWORD", "")
	t.Setenv("REDIS_DB", "0")

	start := time.Now()
	runtime, err := newAvailabilityCacheFromEnv(context.Background())
	if err == nil {
		_ = runtime.Close()
		t.Fatal("newAvailabilityCacheFromEnv() error = nil con Redis inaccesible")
	}
	if runtime != nil {
		t.Fatal("runtime debe ser nil cuando Redis no responde")
	}
	if isAvailabilityCacheConfigError(err) {
		t.Fatalf("una caída de Redis no debe clasificarse como error de configuración: %v", err)
	}
	if !strings.Contains(err.Error(), "redis no responde") {
		t.Fatalf("error = %v, want it to mention redis no responde", err)
	}
	// El timeout acotado evita que el arranque quede colgado.
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("el intento de conexión tardó %v, want <= 5s", elapsed)
	}
}

func TestAvailabilityCacheRuntimeCloseNil(t *testing.T) {
	var nilRuntime *availabilityCacheRuntime
	if err := nilRuntime.Close(); err != nil {
		t.Fatalf("Close() nil = %v", err)
	}
	if err := (&availabilityCacheRuntime{}).Close(); err != nil {
		t.Fatalf("Close() sin store = %v", err)
	}
}
