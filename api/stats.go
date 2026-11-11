package main

// RF21: conteo de citas por estado en un rango de fechas (zona de la clínica).
//
// GET /citas/resumen?desde=YYYY-MM-DD&hasta=YYYY-MM-DD
// Permiso: OpConteoCitas. Tenant desde DomainUser (I5).
// Profesional: solo sus citas.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// estadosCitaOrdenados son las claves siempre presentes en porEstado (enum EstadoCita).
var estadosCitaOrdenados = []string{
	"reservada",
	"atendida",
	"ausente",
	"cancelada",
	"reprogramada",
}

// StatsStore abstrae agregaciones de citas.
type StatsStore interface {
	ZonaHoraria(ctx context.Context, clinicaIdent string) (string, error)
	ProfesionalUsuarioID(ctx context.Context, clinicaIdent, profesionalID string) (string, error)
	ProfesionalIDByUsuarioID(ctx context.Context, clinicaIdent, usuarioID string) (string, error)
	CountByEstado(ctx context.Context, clinicaIdent string, inicio, finExclusive time.Time, profesionalID *string) (map[string]int, error)
}

type pgStatsStore struct {
	db *sql.DB
}

func newPGStatsStore(database *sql.DB) *pgStatsStore {
	return &pgStatsStore{db: database}
}

func (s *pgStatsStore) ZonaHoraria(ctx context.Context, clinicaIdent string) (string, error) {
	var zona string
	err := s.db.QueryRowContext(ctx, `
SELECT "zonaHoraria" FROM "Clinica" WHERE "identificador" = $1`, clinicaIdent,
	).Scan(&zona)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("clínica no encontrada")
	}
	if err != nil {
		return "", fmt.Errorf("zona horaria: %w", err)
	}
	return zona, nil
}

func (s *pgStatsStore) ProfesionalUsuarioID(ctx context.Context, clinicaIdent, profesionalID string) (string, error) {
	var usuarioID string
	err := s.db.QueryRowContext(ctx, `
SELECT p."usuarioId"::text
FROM "Profesional" p
JOIN "Clinica" c ON c."id" = p."clinicaId"
WHERE c."identificador" = $1 AND p."id" = $2`,
		clinicaIdent, profesionalID,
	).Scan(&usuarioID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errAgendaProfesionalFuera
	}
	if err != nil {
		return "", fmt.Errorf("consultar profesional: %w", err)
	}
	return usuarioID, nil
}

func (s *pgStatsStore) ProfesionalIDByUsuarioID(ctx context.Context, clinicaIdent, usuarioID string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `
SELECT p."id"::text
FROM "Profesional" p
JOIN "Clinica" c ON c."id" = p."clinicaId"
WHERE c."identificador" = $1 AND p."usuarioId" = $2`,
		clinicaIdent, usuarioID,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errAgendaProfesionalFuera
	}
	if err != nil {
		return "", fmt.Errorf("resolver profesional: %w", err)
	}
	return id, nil
}

func (s *pgStatsStore) CountByEstado(ctx context.Context, clinicaIdent string, inicio, finExclusive time.Time, profesionalID *string) (map[string]int, error) {
	out := emptyEstadoCounts()
	const q = `
SELECT ci."estado"::text, COUNT(*)::int
FROM "Cita" ci
JOIN "Clinica" c ON c."id" = ci."clinicaId"
WHERE c."identificador" = $1
  AND ci."inicio" >= $2 AND ci."inicio" < $3
  AND ($4::uuid IS NULL OR ci."profesionalId" = $4)
GROUP BY ci."estado"`

	var prof any
	if profesionalID != nil {
		prof = *profesionalID
	}
	rows, err := s.db.QueryContext(ctx, q, clinicaIdent, inicio.UTC(), finExclusive.UTC(), prof)
	if err != nil {
		return nil, fmt.Errorf("contar citas: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var estado string
		var n int
		if err := rows.Scan(&estado, &n); err != nil {
			return nil, fmt.Errorf("leer conteo: %w", err)
		}
		out[estado] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func emptyEstadoCounts() map[string]int {
	out := make(map[string]int, len(estadosCitaOrdenados))
	for _, e := range estadosCitaOrdenados {
		out[e] = 0
	}
	return out
}

func totalEstados(m map[string]int) int {
	n := 0
	for _, e := range estadosCitaOrdenados {
		n += m[e]
	}
	return n
}

var statsStore StatsStore

func getCitasResumen(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, statsStore == nil, "servicio de resumen no disponible")
	if !ok {
		return
	}

	desdeStr := c.Query("desde")
	hastaStr := c.Query("hasta")
	if desdeStr == "" || hastaStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "desde y hasta son obligatorios (YYYY-MM-DD)"})
		return
	}
	desde, err := parseFechaCalendario(desdeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "desde: " + err.Error()})
		return
	}
	hasta, err := parseFechaCalendario(hastaStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "hasta: " + err.Error()})
		return
	}
	if hasta.Before(desde) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "hasta debe ser >= desde"})
		return
	}

	filtro, ok := resolveFiltroProfesional(c, clinica, c.Query("profesionalId"), statsStore)
	if !ok {
		return
	}

	zona, err := statsStore.ZonaHoraria(c.Request.Context(), clinica)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo obtener la zona horaria"})
		return
	}
	inicio, finExcl, err := rangoInclusiveEnZona(desde, hasta, zona)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	porEstado, err := statsStore.CountByEstado(c.Request.Context(), clinica, inicio, finExcl, filtro)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo calcular el resumen"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"clinicaIdentificador": clinica,
		"desde":                desdeStr,
		"hasta":                hastaStr,
		"porEstado":            porEstado,
		"total":                totalEstados(porEstado),
	})
}
