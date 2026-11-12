// Package availabilityversion adapta la consulta de AgendaVersion de
// PostgreSQL al contrato de availabilitycache.
//
// Mantiene availabilitycache libre de dependencias SQL: la caché solo conoce
// la interfaz availabilitycache.AgendaVersionResolver; este paquete provee
// la implementación concreta para producción.
package availabilityversion

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"medicitas/api/internal/availabilitycache"
)

// PostgresResolver implementa availabilitycache.AgendaVersionResolver sobre
// una conexión *sql.DB a PostgreSQL.
type PostgresResolver struct {
	db *sql.DB
}

// NewPostgresResolver construye el adaptador. El *sql.DB debe estar
// inicializado; si es nil, Resolve devolverá error.
func NewPostgresResolver(db *sql.DB) availabilitycache.AgendaVersionResolver {
	return &PostgresResolver{db: db}
}

// Resolve consulta app."AgendaVersion" para la clínica y profesional dados.
//
// clinicaIdent es el identificador humano de la clínica (el mismo valor que
// usa DisponibilidadStore para consultar la zona horaria y otros datos). El
// resolver lo resuelve a UUID internamente mediante un JOIN con "Clinica",
// de modo que availabilitycache no necesita conocer dos formas de referirse a
// la misma clínica.
//
// Si la fila no existe, devuelve 0 y nil (la agenda nunca fue registrada).
// Los errores de PostgreSQL se propagan envueltos para no perder contexto.
func (r *PostgresResolver) Resolve(ctx context.Context, clinicaIdent, profesionalID string) (int64, error) {
	if r.db == nil {
		return 0, fmt.Errorf("availabilityversion: db is nil")
	}

	var version int64
	err := r.db.QueryRowContext(ctx,
		`SELECT av."version"
		 FROM "AgendaVersion" av
		 JOIN "Clinica" c ON c."id" = av."clinicaId"
		 WHERE c."identificador" = $1 AND av."profesionalId" = $2::uuid`,
		clinicaIdent, profesionalID,
	).Scan(&version)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("availabilityversion: query agenda version: %w", err)
	}
	return version, nil
}
