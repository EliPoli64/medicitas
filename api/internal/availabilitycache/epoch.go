package availabilitycache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

const epochInitializationAttempts = 5

// newEpochToken genera un identificador opaco de 128 bits.
//
// No se usa un contador porque un token perdido o expirado se puede regenerar
// sin riesgo de volver a hacer alcanzable una entrada vieja.
func newEpochToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate epoch token: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// currentEpoch lee el epoch vigente o lo inicializa con SETNX.
//
// La inicialización puede competir con otros lectores. Si SETNX no crea la
// clave, se relee el token ganador en lugar de inventar otro.
func (c *Cache) currentEpoch(ctx context.Context, clinicaID, profesionalID string) (string, error) {
	key, err := c.keys.epochKey(clinicaID, profesionalID)
	if err != nil {
		return "", err
	}

	for attempt := 0; attempt < epochInitializationAttempts; attempt++ {
		raw, err := c.getKey(ctx, key)
		if err == nil {
			if epochPattern.MatchString(raw) {
				return raw, nil
			}
			// Un token corrupto se reemplaza; las entradas que lo usaban
			// quedan inalcanzables.
			return c.rotateEpoch(ctx, key)
		}
		if !errors.Is(err, ErrMiss) {
			return "", err
		}

		token, err := c.newEpoch()
		if err != nil {
			return "", fmt.Errorf("%w: %w", ErrCacheWrite, err)
		}

		opCtx, cancel := c.opContext(ctx)
		created, err := c.store.SetNX(opCtx, key, token, c.cfg.EpochTTL)
		cancel()
		if err != nil {
			return "", fmt.Errorf("%w: %w", ErrCacheWrite, err)
		}
		if created {
			return token, nil
		}
	}

	return "", fmt.Errorf("%w: could not initialize epoch after %d attempts", ErrCacheRead, epochInitializationAttempts)
}

// rotateEpoch publica un token nuevo y devuelve el token publicado.
func (c *Cache) rotateEpoch(ctx context.Context, key string) (string, error) {
	token, err := c.newEpoch()
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrCacheWrite, err)
	}

	opCtx, cancel := c.opContext(ctx)
	defer cancel()

	if err := c.store.Set(opCtx, key, token, c.cfg.EpochTTL); err != nil {
		return "", fmt.Errorf("%w: %w", ErrCacheWrite, err)
	}
	return token, nil
}
