package availabilitycache

import (
	"errors"
	"testing"
	"time"
)

func TestRedisConfig_Validate(t *testing.T) {
	valid := RedisConfig{
		Addr:         "localhost:6379",
		DB:           15,
		DialTimeout:  time.Second,
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
	}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid RedisConfig.validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*RedisConfig)
	}{
		{"empty address", func(c *RedisConfig) { c.Addr = "" }},
		{"blank address", func(c *RedisConfig) { c.Addr = "   " }},
		{"negative DB", func(c *RedisConfig) { c.DB = -1 }},
		{"negative dial timeout", func(c *RedisConfig) { c.DialTimeout = -time.Second }},
		{"negative read timeout", func(c *RedisConfig) { c.ReadTimeout = -time.Second }},
		{"negative write timeout", func(c *RedisConfig) { c.WriteTimeout = -time.Second }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)
			if err := cfg.validate(); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("validate() error = %v, want ErrInvalidConfig", err)
			}
		})
	}
}
