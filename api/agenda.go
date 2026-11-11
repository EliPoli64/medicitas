package main

// RF20: agenda del día calendario en la zona horaria de la clínica.
//
// GET /agenda?fecha=YYYY-MM-DD[&profesionalId=…]
// Permiso: OpAgendaDia. Tenant desde DomainUser (I5).
// Profesional: solo la propia; otro profesionalId → 403.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"
	_ "time/tzdata"

	"github.com/gin-gonic/gin"
)

var (
	errAgendaProfesionalFuera = errors.New("profesional no encontrado en la clínica")
	errAgendaSinPermisoOtro   = errors.New("no puede consultar la agenda de otro profesional")
	errAgendaFechaInvalida    = errors.New("fecha inválida; use YYYY-MM-DD")
	errAgendaZonaInvalida     = errors.New("zona horaria de la clínica no válida")
)

// AgendaCita es una fila de la agenda del día.
type AgendaCita struct {
	ID              string `json:"id"`
	Inicio          string `json:"inicio"`
	Fin             string `json:"fin"`
	Estado          string `json:"estado"`
	PacienteID      string `json:"pacienteId"`
	PacienteNombre  string `json:"pacienteNombre"`
	ProfesionalID   string `json:"profesionalId"`
	ServicioID      string `json:"servicioId"`
	ServicioNombre  string `json:"servicioNombre"`
	DuracionMinutos int    `json:"duracionMinutos"`
}

// AgendaStore abstrae lecturas de agenda del día.
type AgendaStore interface {
	ZonaHoraria(ctx context.Context, clinicaIdent string) (string, error)
	ProfesionalUsuarioID(ctx context.Context, clinicaIdent, profesionalID string) (string, error)
	ProfesionalIDByUsuarioID(ctx context.Context, clinicaIdent, usuarioID string) (string, error)
	ListAgendaDia(ctx context.Context, clinicaIdent string, inicio, fin time.Time, profesionalID *string) ([]AgendaCita, error)
}

type pgAgendaStore struct {
	db *sql.DB
}

func newPGAgendaStore(database *sql.DB) *pgAgendaStore {
	return &pgAgendaStore{db: database}
}

func (s *pgAgendaStore) ZonaHoraria(ctx context.Context, clinicaIdent string) (string, error) {
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

func (s *pgAgendaStore) ProfesionalUsuarioID(ctx context.Context, clinicaIdent, profesionalID string) (string, error) {
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

func (s *pgAgendaStore) ProfesionalIDByUsuarioID(ctx context.Context, clinicaIdent, usuarioID string) (string, error) {
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

func (s *pgAgendaStore) ListAgendaDia(ctx context.Context, clinicaIdent string, inicio, fin time.Time, profesionalID *string) ([]AgendaCita, error) {
	const q = `
SELECT ci."id"::text, ci."inicio", ci."fin", ci."estado"::text,
       ci."pacienteId"::text, pa."nombre",
       ci."profesionalId"::text,
       ci."servicioId"::text, sv."nombre",
       ci."duracionMinutos"
FROM "Cita" ci
JOIN "Clinica" c ON c."id" = ci."clinicaId"
JOIN "Paciente" pa ON pa."id" = ci."pacienteId" AND pa."clinicaId" = ci."clinicaId"
JOIN "Servicio" sv ON sv."id" = ci."servicioId" AND sv."clinicaId" = ci."clinicaId"
WHERE c."identificador" = $1
  AND ci."inicio" >= $2 AND ci."inicio" < $3
  AND ($4::uuid IS NULL OR ci."profesionalId" = $4)
ORDER BY ci."inicio"`

	var prof any
	if profesionalID != nil {
		prof = *profesionalID
	}
	rows, err := s.db.QueryContext(ctx, q, clinicaIdent, inicio.UTC(), fin.UTC(), prof)
	if err != nil {
		return nil, fmt.Errorf("listar agenda: %w", err)
	}
	defer rows.Close()

	out := make([]AgendaCita, 0)
	for rows.Next() {
		var a AgendaCita
		var start, end time.Time
		if err := rows.Scan(
			&a.ID, &start, &end, &a.Estado,
			&a.PacienteID, &a.PacienteNombre,
			&a.ProfesionalID,
			&a.ServicioID, &a.ServicioNombre,
			&a.DuracionMinutos,
		); err != nil {
			return nil, fmt.Errorf("leer cita agenda: %w", err)
		}
		a.Inicio = start.UTC().Format(time.RFC3339)
		a.Fin = end.UTC().Format(time.RFC3339)
		out = append(out, a)
	}
	return out, rows.Err()
}

var agendaStore AgendaStore

// parseFechaCalendario interpreta YYYY-MM-DD.
func parseFechaCalendario(s string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, errAgendaFechaInvalida
	}
	return t, nil
}

// diaEnZona convierte una fecha calendario a [inicio, fin) en la zona IANA.
func diaEnZona(fecha time.Time, zona string) (time.Time, time.Time, error) {
	loc, err := time.LoadLocation(zona)
	if err != nil {
		return time.Time{}, time.Time{}, errAgendaZonaInvalida
	}
	y, m, d := fecha.Date()
	inicio := time.Date(y, m, d, 0, 0, 0, 0, loc)
	return inicio, inicio.AddDate(0, 0, 1), nil
}

// rangoInclusiveEnZona: desde/hasta inclusive → [inicio, finExclusive).
func rangoInclusiveEnZona(desde, hasta time.Time, zona string) (time.Time, time.Time, error) {
	loc, err := time.LoadLocation(zona)
	if err != nil {
		return time.Time{}, time.Time{}, errAgendaZonaInvalida
	}
	y1, m1, d1 := desde.Date()
	y2, m2, d2 := hasta.Date()
	inicio := time.Date(y1, m1, d1, 0, 0, 0, 0, loc)
	finExcl := time.Date(y2, m2, d2, 0, 0, 0, 0, loc).AddDate(0, 0, 1)
	return inicio, finExcl, nil
}

// resolveFiltroProfesional aplica reglas de ownership para agenda/conteo.
// Devuelve el filtro (nil = toda la clínica) o responde error y false.
type profesionalLookup interface {
	ProfesionalUsuarioID(ctx context.Context, clinicaIdent, profesionalID string) (string, error)
	ProfesionalIDByUsuarioID(ctx context.Context, clinicaIdent, usuarioID string) (string, error)
}

func resolveFiltroProfesional(c *gin.Context, clinica, queryProf string, store profesionalLookup) (*string, bool) {
	id, ok := identityFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "token inválido o ausente"})
		return nil, false
	}
	du, ok := domainUserFromContext(c)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "usuario de dominio no disponible"})
		return nil, false
	}

	esProf := identityHasRole(id, RoleProfesional) &&
		!identityHasRole(id, RoleAdmClinica) &&
		!identityHasRole(id, RoleRecepcion)

	if esProf {
		propio, err := store.ProfesionalIDByUsuarioID(c.Request.Context(), clinica, du.UsuarioID)
		if errors.Is(err, errAgendaProfesionalFuera) {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return nil, false
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo resolver el profesional"})
			return nil, false
		}
		if queryProf != "" && queryProf != propio {
			c.JSON(http.StatusForbidden, gin.H{"error": errAgendaSinPermisoOtro.Error()})
			return nil, false
		}
		return &propio, true
	}

	if queryProf == "" {
		return nil, true
	}
	if _, err := store.ProfesionalUsuarioID(c.Request.Context(), clinica, queryProf); errors.Is(err, errAgendaProfesionalFuera) {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return nil, false
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo verificar el profesional"})
		return nil, false
	}
	return &queryProf, true
}

func getAgenda(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, agendaStore == nil, "servicio de agenda no disponible")
	if !ok {
		return
	}

	fechaStr := c.Query("fecha")
	if fechaStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "fecha es obligatoria (YYYY-MM-DD)"})
		return
	}
	fecha, err := parseFechaCalendario(fechaStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	filtro, ok := resolveFiltroProfesional(c, clinica, c.Query("profesionalId"), agendaStore)
	if !ok {
		return
	}

	zona, err := agendaStore.ZonaHoraria(c.Request.Context(), clinica)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo obtener la zona horaria"})
		return
	}
	inicio, fin, err := diaEnZona(fecha, zona)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	citas, err := agendaStore.ListAgendaDia(c.Request.Context(), clinica, inicio, fin, filtro)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo listar la agenda"})
		return
	}
	if citas == nil {
		citas = []AgendaCita{}
	}

	var profOut any
	if filtro != nil {
		profOut = *filtro
	} else {
		profOut = nil
	}
	c.JSON(http.StatusOK, gin.H{
		"clinicaIdentificador": clinica,
		"fecha":                fechaStr,
		"zonaHoraria":          zona,
		"profesionalId":        profOut,
		"citas":                citas,
	})
}
