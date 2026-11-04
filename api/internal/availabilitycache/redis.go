package availabilitycache

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisConfig agrupa la configuración mínima de conexión a Redis.
type RedisConfig struct {
	Addr         string
	Password     string
	DB           int
	DialTimeout  time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

func (c RedisConfig) validate() error {
	if strings.TrimSpace(c.Addr) == "" {
		return fmt.Errorf("%w: Redis address is empty", ErrInvalidConfig)
	}
	if c.DB < 0 {
		return fmt.Errorf("%w: Redis DB cannot be negative", ErrInvalidConfig)
	}
	if c.DialTimeout < 0 || c.ReadTimeout < 0 || c.WriteTimeout < 0 {
		return fmt.Errorf("%w: Redis timeouts cannot be negative", ErrInvalidConfig)
	}
	return nil
}

// RedisStore implementa Store con go-redis.
//
// El cliente de go-redis es seguro para uso concurrente. Esta implementación
// no interpreta el contenido de los valores: solo almacena y recupera texto.
type RedisStore struct {
	client *redis.Client
}

// NewRedisStore crea un cliente de Redis. La conexión se comprueba con Ping.
func NewRedisStore(cfg RedisConfig) (*RedisStore, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	client := redis.NewClient(&redis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	})

	return &RedisStore{client: client}, nil
}

func (s *RedisStore) Get(ctx context.Context, key string) (string, error) {
	value, err := s.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrMiss
	}
	if err != nil {
		return "", fmt.Errorf("redis get %q: %w", key, err)
	}
	return value, nil
}

func (s *RedisStore) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	if err := s.client.Set(ctx, key, value, ttl).Err(); err != nil {
		return fmt.Errorf("redis set %q: %w", key, err)
	}
	return nil
}

func (s *RedisStore) SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	created, err := s.client.SetNX(ctx, key, value, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("redis setnx %q: %w", key, err)
	}
	return created, nil
}

// Ping comprueba la conectividad con Redis.
func (s *RedisStore) Ping(ctx context.Context) error {
	if err := s.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis ping: %w", err)
	}
	return nil
}

// Close libera el pool de conexiones.
func (s *RedisStore) Close() error {
	return s.client.Close()
}
