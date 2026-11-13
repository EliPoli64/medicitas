package main

// RF9–RF13: ciclo de vida de la cita.
//
// POST /citas                     OpReservarReprogramarCancelar  (RF9)
// POST /citas/:id/reprogramar     OpReservarReprogramarCancelar  (RF10)
// POST /citas/:id/cancelar        OpReservarReprogramarCancelar  (RF11)
// POST /citas/:id/desenlace       OpRegistrarDesenlace           (RF12)
//
// RF13: transiciones ilegales las rechaza validarTransicionCita (+ CHECK).
// Duración/fin: trigger prepararCita (I4). Solapes: EXCLUDE (I1/I2) → 409.
// Tras mutación exitosa: invalidarDisponibilidadDeAgenda (I7/I8).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	errCitaDatosInvalidos     = errors.New("datos de cita inválidos")
	errCitaConflicto          = errors.New("el espacio ya está ocupado")
	errCitaNegocio            = errors.New("la cita no cumple las reglas de agenda")
	errCitaRecursoFuera       = errors.New("paciente, profesional o servicio no encontrado en la clínica")
	errCitaClinicaInexistente = errors.New("clínica no encontrada")
	errCitaFueraDeClinica     = errors.New("cita no encontrada en la clínica")
	errCitaSinPermisoPropio   = errors.New("solo puede gestionar el desenlace de sus propias citas")
	errCitaSinProfesional     = errors.New("el usuario no está registrado como profesional")
)

// Appointment es la proyección de una cita.
type Appointment struct {
	ID              string `json:"id"`
	PacienteID      string `json:"pacienteId"`
	ProfesionalID   string `json:"profesionalId"`
	ServicioID      string `json:"servicioId"`
	Inicio          string `json:"inicio"`
	Fin             string `json:"fin"`
	DuracionMinutos int    `json:"duracionMinutos"`
	Estado          string `json:"estado"`
	CitaOrigenID    string `json:"citaOrigenId,omitempty"`
}

// ReprogramacionResult agrupa origen + nueva cita (RF10).
type ReprogramacionResult struct {
	CitaAnteriorID        string
	ProfesionalAnteriorID string
	CitaNueva             Appointment
}

// AppointmentStore abstrae el ciclo de cita (tests sin Postgres).
type AppointmentStore interface {
	Reservar(ctx context.Context, clinicaIdent, pacienteID, profesionalID, servicioID string, inicio time.Time) (Appointment, error)
	GetCita(ctx context.Context, clinicaIdent, citaID string) (Appointment, error)
	Reprogramar(ctx context.Context, clinicaIdent, citaID string, nuevoInicio time.Time, nuevoProfesionalID, nuevoServicioID *string) (ReprogramacionResult, error)
	Cancelar(ctx context.Context, clinicaIdent, citaID, usuarioID, motivo string) (Appointment, error)
	RegistrarDesenlace(ctx context.Context, clinicaIdent, citaID, estado string) (Appointment, error)
	ProfesionalIDByUsuarioID(ctx context.Context, clinicaIdent, usuarioID string) (string, error)
}

type pgAppointmentStore struct {
	db *sql.DB
}

func newPGAppointmentStore(database *sql.DB) *pgAppointmentStore {
	return &pgAppointmentStore{db: database}
}

const citaSelectCols = `
"id"::text, "pacienteId"::text, "profesionalId"::text, "servicioId"::text,
"inicio", "fin", "duracionMinutos", "estado"::text,
COALESCE("citaOrigenId"::text, '')`

func scanAppointment(scanner interface {
	Scan(dest ...any) error
}) (Appointment, error) {
	var a Appointment
	var start, end time.Time
	var origen string
	if err := scanner.Scan(
		&a.ID, &a.PacienteID, &a.ProfesionalID, &a.ServicioID,
		&start, &end, &a.DuracionMinutos, &a.Estado, &origen,
	); err != nil {
		return Appointment{}, err
	}
	a.Inicio = start.UTC().Format(time.RFC3339)
	a.Fin = end.UTC().Format(time.RFC3339)
	a.CitaOrigenID = origen
	return a, nil
}

func (s *pgAppointmentStore) Reservar(ctx context.Context, clinicaIdent, pacienteID, profesionalID, servicioID string, inicio time.Time) (Appointment, error) {
	const q = `
INSERT INTO "Cita" ("clinicaId", "pacienteId", "profesionalId", "servicioId", "inicio")
SELECT c."id", $2::uuid, $3::uuid, $4::uuid, $5
FROM "Clinica" c
WHERE c."identificador" = $1
RETURNING ` + citaSelectCols

	a, err := scanAppointment(s.db.QueryRowContext(ctx, q, clinicaIdent, pacienteID, profesionalID, servicioID, inicio.UTC()))
	if errors.Is(err, sql.ErrNoRows) {
		return Appointment{}, errCitaClinicaInexistente
	}
	if err != nil {
		return Appointment{}, mapCitaMutacionError(err, "reservar cita")
	}
	return a, nil
}

func (s *pgAppointmentStore) GetCita(ctx context.Context, clinicaIdent, citaID string) (Appointment, error) {
	a, err := scanAppointment(s.db.QueryRowContext(ctx, `
SELECT ci."id"::text, ci."pacienteId"::text, ci."profesionalId"::text, ci."servicioId"::text,
       ci."inicio", ci."fin", ci."duracionMinutos", ci."estado"::text,
       COALESCE(ci."citaOrigenId"::text, '')
FROM "Cita" ci
JOIN "Clinica" c ON c."id" = ci."clinicaId"
WHERE c."identificador" = $1 AND ci."id" = $2`, clinicaIdent, citaID))
	if errors.Is(err, sql.ErrNoRows) {
		return Appointment{}, errCitaFueraDeClinica
	}
	if err != nil {
		return Appointment{}, fmt.Errorf("consultar cita: %w", err)
	}
	return a, nil
}

func (s *pgAppointmentStore) ProfesionalIDByUsuarioID(ctx context.Context, clinicaIdent, usuarioID string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `
SELECT p."id"::text
FROM "Profesional" p
JOIN "Clinica" c ON c."id" = p."clinicaId"
WHERE c."identificador" = $1 AND p."usuarioId" = $2`,
		clinicaIdent, usuarioID,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errCitaSinProfesional
	}
	if err != nil {
		return "", fmt.Errorf("resolver profesional: %w", err)
	}
	return id, nil
}

func (s *pgAppointmentStore) Reprogramar(ctx context.Context, clinicaIdent, citaID string, nuevoInicio time.Time, nuevoProfesionalID, nuevoServicioID *string) (ReprogramacionResult, error) {
	origen, err := s.GetCita(ctx, clinicaIdent, citaID)
	if err != nil {
		return ReprogramacionResult{}, err
	}

	var clinicaUUID string
	if err := s.db.QueryRowContext(ctx, `
SELECT "id"::text FROM "Clinica" WHERE "identificador" = $1`, clinicaIdent,
	).Scan(&clinicaUUID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ReprogramacionResult{}, errCitaClinicaInexistente
		}
		return ReprogramacionResult{}, fmt.Errorf("clínica: %w", err)
	}

	var nuevoID string
	err = s.db.QueryRowContext(ctx, `
SELECT app."reprogramarCita"($1::uuid, $2::uuid, $3::timestamptz, $4::uuid, $5::uuid)::text`,
		citaID, clinicaUUID, nuevoInicio.UTC(), nullableUUID(nuevoProfesionalID), nullableUUID(nuevoServicioID),
	).Scan(&nuevoID)
	if err != nil {
		return ReprogramacionResult{}, mapCitaMutacionError(err, "reprogramar cita")
	}

	nueva, err := s.GetCita(ctx, clinicaIdent, nuevoID)
	if err != nil {
		return ReprogramacionResult{}, err
	}
	return ReprogramacionResult{
		CitaAnteriorID:        origen.ID,
		ProfesionalAnteriorID: origen.ProfesionalID,
		CitaNueva:             nueva,
	}, nil
}

func nullableUUID(p *string) any {
	if p == nil || strings.TrimSpace(*p) == "" {
		return nil
	}
	return strings.TrimSpace(*p)
}

func (s *pgAppointmentStore) Cancelar(ctx context.Context, clinicaIdent, citaID, usuarioID, motivo string) (Appointment, error) {
	a, err := scanAppointment(s.db.QueryRowContext(ctx, `
UPDATE "Cita" ci
SET "estado" = 'cancelada',
    "canceladaEn" = now(),
    "canceladaPorUsuarioId" = $3::uuid,
    "motivoCancelacion" = $4
FROM "Clinica" c
WHERE c."id" = ci."clinicaId"
  AND c."identificador" = $1
  AND ci."id" = $2
  AND ci."estado" = 'reservada'
RETURNING ci."id"::text, ci."pacienteId"::text, ci."profesionalId"::text, ci."servicioId"::text,
          ci."inicio", ci."fin", ci."duracionMinutos", ci."estado"::text,
          COALESCE(ci."citaOrigenId"::text, '')`,
		clinicaIdent, citaID, usuarioID, motivo,
	))
	if errors.Is(err, sql.ErrNoRows) {
		// Distinguir: no existe vs ya no reservada.
		if _, getErr := s.GetCita(ctx, clinicaIdent, citaID); errors.Is(getErr, errCitaFueraDeClinica) {
			return Appointment{}, errCitaFueraDeClinica
		} else if getErr != nil {
			return Appointment{}, getErr
		}
		return Appointment{}, fmt.Errorf("%w: solo se puede cancelar una cita reservada", errCitaNegocio)
	}
	if err != nil {
		return Appointment{}, mapCitaMutacionError(err, "cancelar cita")
	}
	return a, nil
}

func (s *pgAppointmentStore) RegistrarDesenlace(ctx context.Context, clinicaIdent, citaID, estado string) (Appointment, error) {
	a, err := scanAppointment(s.db.QueryRowContext(ctx, `
UPDATE "Cita" ci
SET "estado" = $3::"EstadoCita"
FROM "Clinica" c
WHERE c."id" = ci."clinicaId"
  AND c."identificador" = $1
  AND ci."id" = $2
  AND ci."estado" = 'reservada'
RETURNING ci."id"::text, ci."pacienteId"::text, ci."profesionalId"::text, ci."servicioId"::text,
          ci."inicio", ci."fin", ci."duracionMinutos", ci."estado"::text,
          COALESCE(ci."citaOrigenId"::text, '')`,
		clinicaIdent, citaID, estado,
	))
	if errors.Is(err, sql.ErrNoRows) {
		if _, getErr := s.GetCita(ctx, clinicaIdent, citaID); errors.Is(getErr, errCitaFueraDeClinica) {
			return Appointment{}, errCitaFueraDeClinica
		} else if getErr != nil {
			return Appointment{}, getErr
		}
		return Appointment{}, fmt.Errorf("%w: solo se puede registrar desenlace de una cita reservada", errCitaNegocio)
	}
	if err != nil {
		return Appointment{}, mapCitaMutacionError(err, "registrar desenlace")
	}
	return a, nil
}

func mapCitaMutacionError(err error, op string) error {
	if isExclusionViolation(err) {
		return fmt.Errorf("%w: %s", errCitaConflicto, exclusionConstraintHint(err))
	}
	if isForeignKeyViolation(err) {
		return errCitaRecursoFuera
	}
	if isCheckViolation(err) {
		return fmt.Errorf("%w: %s", errCitaNegocio, pgErrorMessage(err))
	}
	if isRaiseException(err) {
		msg := pgErrorMessage(err)
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "no existe") || strings.Contains(lower, "no está activo") {
			return fmt.Errorf("%w: %s", errCitaRecursoFuera, msg)
		}
		return fmt.Errorf("%w: %s", errCitaNegocio, msg)
	}
	return fmt.Errorf("%s: %w", op, err)
}

func exclusionConstraintHint(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName != "" {
		return pgErr.ConstraintName
	}
	return "solape"
}

func pgErrorMessage(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Message != "" {
		return pgErr.Message
	}
	return err.Error()
}

var appointmentStore AppointmentStore

func respondCitaError(c *gin.Context, err error, fallback500 string) {
	switch {
	case errors.Is(err, errCitaConflicto):
		c.JSON(http.StatusConflict, gin.H{"error": errCitaConflicto.Error()})
	case errors.Is(err, errCitaFueraDeClinica), errors.Is(err, errCitaRecursoFuera), errors.Is(err, errCitaClinicaInexistente):
		c.JSON(http.StatusNotFound, gin.H{"error": unwrapMsg(err, errCitaFueraDeClinica.Error())})
	case errors.Is(err, errCitaSinPermisoPropio), errors.Is(err, errCitaSinProfesional):
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, errCitaNegocio), errors.Is(err, errCitaDatosInvalidos):
		c.JSON(http.StatusBadRequest, gin.H{"error": unwrapMsg(err, err.Error())})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": fallback500})
	}
}

type createCitaRequest struct {
	PacienteID    string `json:"pacienteId"`
	ProfesionalID string `json:"profesionalId"`
	ServicioID    string `json:"servicioId"`
	Inicio        string `json:"inicio"`
}

func createCita(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, appointmentStore == nil, "servicio de citas no disponible")
	if !ok {
		return
	}

	var req createCitaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cuerpo JSON inválido", "detalles": []string{err.Error()}})
		return
	}

	var errs []string
	if strings.TrimSpace(req.PacienteID) == "" {
		errs = append(errs, "pacienteId es obligatorio")
	}
	if strings.TrimSpace(req.ProfesionalID) == "" {
		errs = append(errs, "profesionalId es obligatorio")
	}
	if strings.TrimSpace(req.ServicioID) == "" {
		errs = append(errs, "servicioId es obligatorio")
	}
	inicio, errInicio := parseRFC3339Flexible(req.Inicio)
	if errInicio != nil {
		errs = append(errs, "inicio debe ser timestamptz RFC3339")
	}
	if len(errs) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida", "detalles": errs})
		return
	}

	cita, err := appointmentStore.Reservar(
		c.Request.Context(),
		clinica,
		strings.TrimSpace(req.PacienteID),
		strings.TrimSpace(req.ProfesionalID),
		strings.TrimSpace(req.ServicioID),
		inicio,
	)
	if err != nil {
		respondCitaError(c, err, "no se pudo reservar la cita")
		return
	}
	registrarFalloInvalidacion(invalidarDisponibilidadDeAgenda(
		c.Request.Context(), clinica, cita.ProfesionalID,
	))
	c.JSON(http.StatusCreated, gin.H{
		"clinicaIdentificador": clinica,
		"cita":                 cita,
	})
}

type reprogramarCitaRequest struct {
	Inicio        string  `json:"inicio"`
	ProfesionalID *string `json:"profesionalId"`
	ServicioID    *string `json:"servicioId"`
}

func reprogramarCita(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, appointmentStore == nil, "servicio de citas no disponible")
	if !ok {
		return
	}
	var req reprogramarCitaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cuerpo JSON inválido", "detalles": []string{err.Error()}})
		return
	}
	inicio, err := parseRFC3339Flexible(req.Inicio)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida", "detalles": []string{"inicio debe ser timestamptz RFC3339"}})
		return
	}

	res, err := appointmentStore.Reprogramar(
		c.Request.Context(), clinica, c.Param("id"), inicio, req.ProfesionalID, req.ServicioID,
	)
	if err != nil {
		respondCitaError(c, err, "no se pudo reprogramar la cita")
		return
	}
	registrarFalloInvalidacion(invalidarDisponibilidadDeAgenda(
		c.Request.Context(), clinica, res.ProfesionalAnteriorID,
	))
	if res.CitaNueva.ProfesionalID != res.ProfesionalAnteriorID {
		registrarFalloInvalidacion(invalidarDisponibilidadDeAgenda(
			c.Request.Context(), clinica, res.CitaNueva.ProfesionalID,
		))
	}
	c.JSON(http.StatusOK, gin.H{
		"clinicaIdentificador": clinica,
		"citaAnteriorId":       res.CitaAnteriorID,
		"cita":                 res.CitaNueva,
	})
}

type cancelarCitaRequest struct {
	Motivo string `json:"motivo"`
}

func cancelarCita(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, appointmentStore == nil, "servicio de citas no disponible")
	if !ok {
		return
	}
	du, ok := domainUserFromContext(c)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "usuario de dominio no disponible"})
		return
	}
	var req cancelarCitaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cuerpo JSON inválido", "detalles": []string{err.Error()}})
		return
	}
	motivo := strings.TrimSpace(req.Motivo)
	if motivo == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida", "detalles": []string{"motivo es obligatorio"}})
		return
	}

	cita, err := appointmentStore.Cancelar(c.Request.Context(), clinica, c.Param("id"), du.UsuarioID, motivo)
	if err != nil {
		respondCitaError(c, err, "no se pudo cancelar la cita")
		return
	}
	registrarFalloInvalidacion(invalidarDisponibilidadDeAgenda(
		c.Request.Context(), clinica, cita.ProfesionalID,
	))
	c.JSON(http.StatusOK, gin.H{
		"clinicaIdentificador": clinica,
		"cita":                 cita,
	})
}

type desenlaceCitaRequest struct {
	Estado string `json:"estado"`
}

func registrarDesenlace(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, appointmentStore == nil, "servicio de citas no disponible")
	if !ok {
		return
	}
	var req desenlaceCitaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cuerpo JSON inválido", "detalles": []string{err.Error()}})
		return
	}
	estado := strings.TrimSpace(req.Estado)
	if estado != "atendida" && estado != "ausente" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida", "detalles": []string{"estado debe ser atendida o ausente"}})
		return
	}

	citaID := c.Param("id")
	if !assertPuedeRegistrarDesenlace(c, clinica, citaID) {
		return
	}

	cita, err := appointmentStore.RegistrarDesenlace(c.Request.Context(), clinica, citaID, estado)
	if err != nil {
		respondCitaError(c, err, "no se pudo registrar el desenlace")
		return
	}
	registrarFalloInvalidacion(invalidarDisponibilidadDeAgenda(
		c.Request.Context(), clinica, cita.ProfesionalID,
	))
	c.JSON(http.StatusOK, gin.H{
		"clinicaIdentificador": clinica,
		"cita":                 cita,
	})
}

// assertPuedeRegistrarDesenlace: adm/recepción cualquier cita de la clínica;
// profesional solo la propia. Devuelve false si ya respondió error.
func assertPuedeRegistrarDesenlace(c *gin.Context, clinica, citaID string) bool {
	id, ok := identityFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "token inválido o ausente"})
		return false
	}
	esSoloProf := identityHasRole(id, RoleProfesional) &&
		!identityHasRole(id, RoleAdmClinica) &&
		!identityHasRole(id, RoleRecepcion)
	if !esSoloProf {
		return true
	}
	du, ok := domainUserFromContext(c)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "usuario de dominio no disponible"})
		return false
	}
	propio, err := appointmentStore.ProfesionalIDByUsuarioID(c.Request.Context(), clinica, du.UsuarioID)
	if errors.Is(err, errCitaSinProfesional) {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return false
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo resolver el profesional"})
		return false
	}
	cita, err := appointmentStore.GetCita(c.Request.Context(), clinica, citaID)
	if errors.Is(err, errCitaFueraDeClinica) {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return false
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo consultar la cita"})
		return false
	}
	if cita.ProfesionalID != propio {
		c.JSON(http.StatusForbidden, gin.H{"error": errCitaSinPermisoPropio.Error()})
		return false
	}
	return true
}

func unwrapMsg(err error, fallback string) string {
	msg := err.Error()
	if i := strings.Index(msg, ": "); i >= 0 && i+2 < len(msg) {
		detail := strings.TrimSpace(msg[i+2:])
		if detail != "" {
			return detail
		}
	}
	if fallback != "" {
		return fallback
	}
	return msg
}
