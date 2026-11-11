package main

// RF5: horario de atención semanal del profesional (varios bloques por día).
//
// La clínica sale del Usuario de dominio (I5). adm-clinica gestiona cualquier
// profesional de su clínica; profesional solo el propio (Profesional.usuarioId).
// Al mutar horarios, RF7 deberá invalidar la caché de disponibilidad.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

var (
	errHorarioSolapado         = errors.New("el bloque de horario se solapa con otro vigente del profesional")
	errHorarioFueraDeClinica   = errors.New("horario no encontrado en la clínica")
	errProfesionalAgendaFuera  = errors.New("profesional no encontrado en la clínica")
	errAgendaSinPermisoPropio  = errors.New("solo puede gestionar la agenda propia")
	errHorarioDatosInvalidos   = errors.New("datos de horario inválidos")
	horaRe                     = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d(:[0-5]\d)?$`)
	fechaRe                    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// Schedule es la proyección de "HorarioAtencion".
type Schedule struct {
	ID             string  `json:"id"`
	ProfesionalID  string  `json:"profesionalId"`
	DiaSemana      int     `json:"diaSemana"` // 1=lunes … 7=domingo
	HoraInicio     string  `json:"horaInicio"`
	HoraFin        string  `json:"horaFin"`
	VigenteDesde   string  `json:"vigenteDesde"`
	VigenteHasta   *string `json:"vigenteHasta"`
	Activo         bool    `json:"activo"`
}

// SchedulePatch son campos opcionales de PATCH /horarios/:id.
type SchedulePatch struct {
	DiaSemana    *int    `json:"diaSemana"`
	HoraInicio   *string `json:"horaInicio"`
	HoraFin      *string `json:"horaFin"`
	VigenteDesde *string `json:"vigenteDesde"`
	VigenteHasta *string `json:"vigenteHasta"` // "" o omit → sin cambio; JSON null limpia
	ClearHasta   bool    `json:"-"`
	Activo       *bool   `json:"activo"`
}

// ScheduleStore abstrae persistencia de horarios (tests sin Postgres).
type ScheduleStore interface {
	// ProfesionalUsuarioID devuelve el usuarioId del profesional en la clínica, o errProfesionalAgendaFuera.
	ProfesionalUsuarioID(ctx context.Context, clinicaIdent, profesionalID string) (string, error)
	ListHorarios(ctx context.Context, clinicaIdent, profesionalID string, soloActivos bool) ([]Schedule, error)
	CreateHorario(ctx context.Context, clinicaIdent, profesionalID string, in Schedule) (Schedule, error)
	UpdateHorario(ctx context.Context, clinicaIdent, horarioID string, patch SchedulePatch) (Schedule, error)
	// HorarioProfesionalID resuelve a qué profesional pertenece un horario (para ownership en PATCH).
	HorarioProfesionalID(ctx context.Context, clinicaIdent, horarioID string) (string, error)
}

type pgScheduleStore struct {
	db *sql.DB
}

func newPGScheduleStore(database *sql.DB) *pgScheduleStore {
	return &pgScheduleStore{db: database}
}

func (s *pgScheduleStore) ProfesionalUsuarioID(ctx context.Context, clinicaIdent, profesionalID string) (string, error) {
	var usuarioID string
	err := s.db.QueryRowContext(ctx, `
SELECT p."usuarioId"::text
FROM "Profesional" p
JOIN "Clinica" c ON c."id" = p."clinicaId"
WHERE c."identificador" = $1 AND p."id" = $2`,
		clinicaIdent, profesionalID,
	).Scan(&usuarioID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errProfesionalAgendaFuera
	}
	if err != nil {
		return "", fmt.Errorf("consultar profesional: %w", err)
	}
	return usuarioID, nil
}

func (s *pgScheduleStore) HorarioProfesionalID(ctx context.Context, clinicaIdent, horarioID string) (string, error) {
	var profesionalID string
	err := s.db.QueryRowContext(ctx, `
SELECT h."profesionalId"::text
FROM "HorarioAtencion" h
JOIN "Clinica" c ON c."id" = h."clinicaId"
WHERE c."identificador" = $1 AND h."id" = $2`,
		clinicaIdent, horarioID,
	).Scan(&profesionalID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errHorarioFueraDeClinica
	}
	if err != nil {
		return "", fmt.Errorf("consultar horario: %w", err)
	}
	return profesionalID, nil
}

func scanSchedule(scanner interface {
	Scan(dest ...any) error
}) (Schedule, error) {
	var h Schedule
	var hasta sql.NullString
	var inicio, fin time.Time
	err := scanner.Scan(
		&h.ID, &h.ProfesionalID, &h.DiaSemana,
		&inicio, &fin, &h.VigenteDesde, &hasta, &h.Activo,
	)
	if err != nil {
		return Schedule{}, err
	}
	h.HoraInicio = inicio.Format("15:04:05")
	h.HoraFin = fin.Format("15:04:05")
	if hasta.Valid {
		s := hasta.String
		h.VigenteHasta = &s
	}
	return h, nil
}

func (s *pgScheduleStore) ListHorarios(ctx context.Context, clinicaIdent, profesionalID string, soloActivos bool) ([]Schedule, error) {
	q := `
SELECT h."id"::text, h."profesionalId"::text, h."diaSemana",
       h."horaInicio", h."horaFin", h."vigenteDesde"::text, h."vigenteHasta"::text, h."activo"
FROM "HorarioAtencion" h
JOIN "Clinica" c ON c."id" = h."clinicaId"
WHERE c."identificador" = $1 AND h."profesionalId" = $2`
	if soloActivos {
		q += ` AND h."activo" = true`
	}
	q += ` ORDER BY h."diaSemana", h."horaInicio"`

	rows, err := s.db.QueryContext(ctx, q, clinicaIdent, profesionalID)
	if err != nil {
		return nil, fmt.Errorf("listar horarios: %w", err)
	}
	defer rows.Close()

	out := make([]Schedule, 0)
	for rows.Next() {
		h, err := scanSchedule(rows)
		if err != nil {
			return nil, fmt.Errorf("leer horario: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *pgScheduleStore) CreateHorario(ctx context.Context, clinicaIdent, profesionalID string, in Schedule) (Schedule, error) {
	if _, err := s.ProfesionalUsuarioID(ctx, clinicaIdent, profesionalID); err != nil {
		return Schedule{}, err
	}
	var hasta any
	if in.VigenteHasta != nil {
		hasta = *in.VigenteHasta
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO "HorarioAtencion" (
  "clinicaId", "profesionalId", "diaSemana",
  "horaInicio", "horaFin", "vigenteDesde", "vigenteHasta", "activo"
) VALUES (
  (SELECT "id" FROM "Clinica" WHERE "identificador" = $1),
  $2, $3, $4::time, $5::time, $6::date, $7::date, $8
)
RETURNING "id"::text, "profesionalId"::text, "diaSemana",
          "horaInicio", "horaFin", "vigenteDesde"::text, "vigenteHasta"::text, "activo"`,
		clinicaIdent, profesionalID, in.DiaSemana,
		in.HoraInicio, in.HoraFin, in.VigenteDesde, hasta, in.Activo,
	)
	h, err := scanSchedule(row)
	if isExclusionViolation(err) {
		return Schedule{}, errHorarioSolapado
	}
	if isCheckViolation(err) || isNotNullViolation(err) {
		return Schedule{}, errHorarioDatosInvalidos
	}
	if isForeignKeyViolation(err) {
		return Schedule{}, errProfesionalAgendaFuera
	}
	if err != nil {
		return Schedule{}, fmt.Errorf("crear horario: %w", err)
	}
	return h, nil
}

func (s *pgScheduleStore) UpdateHorario(ctx context.Context, clinicaIdent, horarioID string, patch SchedulePatch) (Schedule, error) {
	// Leer actual y aplicar patch en memoria, luego UPDATE completo de campos mutables.
	row := s.db.QueryRowContext(ctx, `
SELECT h."id"::text, h."profesionalId"::text, h."diaSemana",
       h."horaInicio", h."horaFin", h."vigenteDesde"::text, h."vigenteHasta"::text, h."activo"
FROM "HorarioAtencion" h
JOIN "Clinica" c ON c."id" = h."clinicaId"
WHERE c."identificador" = $1 AND h."id" = $2`,
		clinicaIdent, horarioID,
	)
	cur, err := scanSchedule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Schedule{}, errHorarioFueraDeClinica
	}
	if err != nil {
		return Schedule{}, fmt.Errorf("leer horario: %w", err)
	}

	if patch.DiaSemana != nil {
		cur.DiaSemana = *patch.DiaSemana
	}
	if patch.HoraInicio != nil {
		cur.HoraInicio = *patch.HoraInicio
	}
	if patch.HoraFin != nil {
		cur.HoraFin = *patch.HoraFin
	}
	if patch.VigenteDesde != nil {
		cur.VigenteDesde = *patch.VigenteDesde
	}
	if patch.ClearHasta {
		cur.VigenteHasta = nil
	} else if patch.VigenteHasta != nil {
		cur.VigenteHasta = patch.VigenteHasta
	}
	if patch.Activo != nil {
		cur.Activo = *patch.Activo
	}

	var hasta any
	if cur.VigenteHasta != nil {
		hasta = *cur.VigenteHasta
	}
	row = s.db.QueryRowContext(ctx, `
UPDATE "HorarioAtencion" h SET
  "diaSemana" = $3,
  "horaInicio" = $4::time,
  "horaFin" = $5::time,
  "vigenteDesde" = $6::date,
  "vigenteHasta" = $7::date,
  "activo" = $8,
  "actualizadoEn" = now()
FROM "Clinica" c
WHERE c."id" = h."clinicaId" AND c."identificador" = $1 AND h."id" = $2
RETURNING h."id"::text, h."profesionalId"::text, h."diaSemana",
          h."horaInicio", h."horaFin", h."vigenteDesde"::text, h."vigenteHasta"::text, h."activo"`,
		clinicaIdent, horarioID, cur.DiaSemana,
		cur.HoraInicio, cur.HoraFin, cur.VigenteDesde, hasta, cur.Activo,
	)
	h, err := scanSchedule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Schedule{}, errHorarioFueraDeClinica
	}
	if isExclusionViolation(err) {
		return Schedule{}, errHorarioSolapado
	}
	if isCheckViolation(err) || isNotNullViolation(err) {
		return Schedule{}, errHorarioDatosInvalidos
	}
	if err != nil {
		return Schedule{}, fmt.Errorf("actualizar horario: %w", err)
	}
	return h, nil
}

var scheduleStore ScheduleStore

func identityHasRole(id Identity, role string) bool {
	for _, r := range id.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// assertPuedeGestionarAgenda comprueba tenant + ownership. Devuelve false si ya respondió error.
func assertPuedeGestionarAgenda(c *gin.Context, clinicaIdent, profesionalID string) bool {
	if scheduleStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de agenda no disponible"})
		return false
	}
	usuarioProf, err := scheduleStore.ProfesionalUsuarioID(c.Request.Context(), clinicaIdent, profesionalID)
	if errors.Is(err, errProfesionalAgendaFuera) {
		c.JSON(http.StatusNotFound, gin.H{"error": errProfesionalAgendaFuera.Error()})
		return false
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo verificar el profesional"})
		return false
	}
	id, ok := identityFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "token inválido o ausente"})
		return false
	}
	if identityHasRole(id, RoleAdmClinica) {
		return true
	}
	du, ok := domainUserFromContext(c)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "usuario de dominio no disponible"})
		return false
	}
	if usuarioProf != du.UsuarioID {
		c.JSON(http.StatusForbidden, gin.H{"error": errAgendaSinPermisoPropio.Error()})
		return false
	}
	return true
}

type createHorarioRequest struct {
	DiaSemana    int     `json:"diaSemana"`
	HoraInicio   string  `json:"horaInicio"`
	HoraFin      string  `json:"horaFin"`
	VigenteDesde string  `json:"vigenteDesde"`
	VigenteHasta *string `json:"vigenteHasta"`
	Activo       *bool   `json:"activo"`
}

func (r *createHorarioRequest) validate() []string {
	var errs []string
	if r.DiaSemana < 1 || r.DiaSemana > 7 {
		errs = append(errs, "diaSemana debe estar entre 1 (lunes) y 7 (domingo)")
	}
	if !horaRe.MatchString(r.HoraInicio) {
		errs = append(errs, "horaInicio debe ser HH:MM o HH:MM:SS")
	}
	if !horaRe.MatchString(r.HoraFin) {
		errs = append(errs, "horaFin debe ser HH:MM o HH:MM:SS")
	}
	if r.VigenteDesde == "" {
		r.VigenteDesde = time.Now().Format("2006-01-02")
	}
	if !fechaRe.MatchString(r.VigenteDesde) {
		errs = append(errs, "vigenteDesde debe ser YYYY-MM-DD")
	}
	if r.VigenteHasta != nil && *r.VigenteHasta != "" && !fechaRe.MatchString(*r.VigenteHasta) {
		errs = append(errs, "vigenteHasta debe ser YYYY-MM-DD")
	}
	return errs
}

func normalizeHora(h string) string {
	if len(h) == 5 {
		return h + ":00"
	}
	return h
}

func listHorarios(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, scheduleStore == nil, "servicio de agenda no disponible")
	if !ok {
		return
	}
	profesionalID := c.Param("id")
	if !assertPuedeGestionarAgenda(c, clinica, profesionalID) {
		return
	}
	soloActivos := c.Query("activo") != "false"
	list, err := scheduleStore.ListHorarios(c.Request.Context(), clinica, profesionalID, soloActivos)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudieron listar los horarios"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"clinicaIdentificador": clinica,
		"profesionalId":        profesionalID,
		"horarios":             list,
	})
}

func createHorario(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, scheduleStore == nil, "servicio de agenda no disponible")
	if !ok {
		return
	}
	profesionalID := c.Param("id")
	if !assertPuedeGestionarAgenda(c, clinica, profesionalID) {
		return
	}
	var req createHorarioRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cuerpo JSON inválido", "detalles": []string{err.Error()}})
		return
	}
	if errs := req.validate(); len(errs) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida", "detalles": errs})
		return
	}
	activo := true
	if req.Activo != nil {
		activo = *req.Activo
	}
	in := Schedule{
		DiaSemana:    req.DiaSemana,
		HoraInicio:   normalizeHora(req.HoraInicio),
		HoraFin:      normalizeHora(req.HoraFin),
		VigenteDesde: req.VigenteDesde,
		VigenteHasta: req.VigenteHasta,
		Activo:       activo,
	}
	if in.VigenteHasta != nil && strings.TrimSpace(*in.VigenteHasta) == "" {
		in.VigenteHasta = nil
	}
	h, err := scheduleStore.CreateHorario(c.Request.Context(), clinica, profesionalID, in)
	switch {
	case errors.Is(err, errProfesionalAgendaFuera):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, errHorarioSolapado):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, errHorarioDatosInvalidos):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo crear el horario"})
	default:
		c.JSON(http.StatusCreated, gin.H{
			"clinicaIdentificador": clinica,
			"horario":              h,
		})
	}
}

func updateHorario(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, scheduleStore == nil, "servicio de agenda no disponible")
	if !ok {
		return
	}
	horarioID := c.Param("id")
	profesionalID, err := scheduleStore.HorarioProfesionalID(c.Request.Context(), clinica, horarioID)
	if errors.Is(err, errHorarioFueraDeClinica) {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo resolver el horario"})
		return
	}
	if !assertPuedeGestionarAgenda(c, clinica, profesionalID) {
		return
	}

	var raw map[string]any
	if err := c.ShouldBindJSON(&raw); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cuerpo JSON inválido", "detalles": []string{err.Error()}})
		return
	}
	var patch SchedulePatch
	var errs []string
	if v, ok := raw["diaSemana"]; ok {
		n, ok := asInt(v)
		if !ok || n < 1 || n > 7 {
			errs = append(errs, "diaSemana debe estar entre 1 y 7")
		} else {
			patch.DiaSemana = &n
		}
	}
	if v, ok := raw["horaInicio"]; ok {
		s, ok := v.(string)
		if !ok || !horaRe.MatchString(s) {
			errs = append(errs, "horaInicio debe ser HH:MM o HH:MM:SS")
		} else {
			s = normalizeHora(s)
			patch.HoraInicio = &s
		}
	}
	if v, ok := raw["horaFin"]; ok {
		s, ok := v.(string)
		if !ok || !horaRe.MatchString(s) {
			errs = append(errs, "horaFin debe ser HH:MM o HH:MM:SS")
		} else {
			s = normalizeHora(s)
			patch.HoraFin = &s
		}
	}
	if v, ok := raw["vigenteDesde"]; ok {
		s, ok := v.(string)
		if !ok || !fechaRe.MatchString(s) {
			errs = append(errs, "vigenteDesde debe ser YYYY-MM-DD")
		} else {
			patch.VigenteDesde = &s
		}
	}
	if v, exists := raw["vigenteHasta"]; exists {
		if v == nil {
			patch.ClearHasta = true
		} else if s, ok := v.(string); ok {
			if s != "" && !fechaRe.MatchString(s) {
				errs = append(errs, "vigenteHasta debe ser YYYY-MM-DD")
			} else if s == "" {
				patch.ClearHasta = true
			} else {
				patch.VigenteHasta = &s
			}
		} else {
			errs = append(errs, "vigenteHasta inválido")
		}
	}
	if v, ok := raw["activo"]; ok {
		b, ok := v.(bool)
		if !ok {
			errs = append(errs, "activo debe ser boolean")
		} else {
			patch.Activo = &b
		}
	}
	if len(errs) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida", "detalles": errs})
		return
	}
	if patch.DiaSemana == nil && patch.HoraInicio == nil && patch.HoraFin == nil &&
		patch.VigenteDesde == nil && !patch.ClearHasta && patch.VigenteHasta == nil && patch.Activo == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida", "detalles": []string{"debe indicar al menos un campo"}})
		return
	}

	h, err := scheduleStore.UpdateHorario(c.Request.Context(), clinica, horarioID, patch)
	switch {
	case errors.Is(err, errHorarioFueraDeClinica):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, errHorarioSolapado):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, errHorarioDatosInvalidos):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo actualizar el horario"})
	default:
		c.JSON(http.StatusOK, gin.H{
			"clinicaIdentificador": clinica,
			"horario":              h,
		})
	}
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), n == float64(int(n))
	case int:
		return n, true
	default:
		return 0, false
	}
}
