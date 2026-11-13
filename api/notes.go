package main

// RF15: adjuntar una nota clínica a una cita atendida. Solo la escribe el
// profesional que atendió esa cita (matriz OpNotaClinica + chequeo de
// propiedad), y es el dato más sensible del sistema.
//
// El motor refuerza lo mismo: la FK compuesta NotaClinicaCitaProfesionalFk
// obliga a que el profesional sea el de la cita, y el trigger
// validarNotaClinica exige cita atendida. El handler pre-valida para
// responder con códigos claros; los triggers son la última línea.
//
// RF18: la escritura se registra en la MISMA transacción que la nota; la
// lectura se registra antes de responder, y sin rastro no se sirve.
//
// Rutas (el historial RF16 no incluye notas a propósito: el contenido solo se
// expone por aquí):
//   POST /citas/:id/nota   — adjuntar (201)
//   GET  /citas/:id/nota   — leer (200)

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

var (
	errNotaCitaFuera      = errors.New("cita no encontrada en la clínica")
	errNotaSoloPropia     = errors.New("solo el profesional que atendió la cita puede gestionar su nota")
	errNotaSinProfesional = errors.New("el usuario no está registrado como profesional")
	errCitaNoAtendida     = errors.New("solo se puede adjuntar nota clínica a una cita atendida")
	errNotaYaExiste       = errors.New("ya existe una nota clínica para esta cita")
	errNotaContenidoVacio = errors.New("contenido es obligatorio")
	errNotaNoExiste       = errors.New("la cita no tiene nota clínica")
)

// NotaClinica es la proyección de "NotaClinica".
type NotaClinica struct {
	ID            string    `json:"id"`
	CitaID        string    `json:"citaId"`
	ProfesionalID string    `json:"profesionalId"`
	Contenido     string    `json:"contenido"`
	CreadoEn      time.Time `json:"creadoEn"`
}

// CitaNotaInfo es lo que RF15 necesita saber de la cita.
type CitaNotaInfo struct {
	ID            string
	ProfesionalID string
	PacienteID    string
	Estado        string
}

// NotaStore abstrae notas clínicas (tests sin Postgres).
type NotaStore interface {
	// CitaParaNota resuelve la cita dentro de la clínica (errNotaCitaFuera si no).
	CitaParaNota(ctx context.Context, clinicaIdent, citaID string) (CitaNotaInfo, error)
	// ProfesionalDeUsuario resuelve el Profesional del usuario autenticado.
	ProfesionalDeUsuario(ctx context.Context, clinicaIdent, usuarioID string) (string, error)
	// CreateNota inserta la nota Y su evento de escritura en la misma
	// transacción (errNotaYaExiste si la cita ya tiene nota).
	CreateNota(ctx context.Context, clinicaIdent, citaID, profesionalID, contenido string, actor AuditActor) (NotaClinica, error)
	// GetNota devuelve la nota de la cita (errNotaNoExiste si no tiene).
	GetNota(ctx context.Context, clinicaIdent, citaID string) (NotaClinica, error)
}

type pgNotaStore struct {
	db *sql.DB
}

func newPGNotaStore(database *sql.DB) *pgNotaStore {
	return &pgNotaStore{db: database}
}

func (s *pgNotaStore) CitaParaNota(ctx context.Context, clinicaIdent, citaID string) (CitaNotaInfo, error) {
	var info CitaNotaInfo
	err := dbq(ctx, s.db).QueryRowContext(ctx, `
SELECT ci."id"::text, ci."profesionalId"::text, ci."pacienteId"::text, ci."estado"::text
FROM "Cita" ci
JOIN "Clinica" c ON c."id" = ci."clinicaId"
WHERE c."identificador" = $1 AND ci."id" = $2`,
		clinicaIdent, citaID,
	).Scan(&info.ID, &info.ProfesionalID, &info.PacienteID, &info.Estado)
	if errors.Is(err, sql.ErrNoRows) {
		return CitaNotaInfo{}, errNotaCitaFuera
	}
	if err != nil {
		return CitaNotaInfo{}, fmt.Errorf("consultar cita: %w", err)
	}
	return info, nil
}

func (s *pgNotaStore) ProfesionalDeUsuario(ctx context.Context, clinicaIdent, usuarioID string) (string, error) {
	var profesionalID string
	err := dbq(ctx, s.db).QueryRowContext(ctx, `
SELECT p."id"::text
FROM "Profesional" p
JOIN "Clinica" c ON c."id" = p."clinicaId"
WHERE c."identificador" = $1 AND p."usuarioId" = $2`,
		clinicaIdent, usuarioID,
	).Scan(&profesionalID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errNotaSinProfesional
	}
	if err != nil {
		return "", fmt.Errorf("consultar profesional: %w", err)
	}
	return profesionalID, nil
}

func (s *pgNotaStore) CreateNota(ctx context.Context, clinicaIdent, citaID, profesionalID, contenido string, actor AuditActor) (NotaClinica, error) {
	tx, err := beginDB(ctx, s.db)
	if err != nil {
		return NotaClinica{}, fmt.Errorf("abrir transacción: %w", err)
	}
	defer tx.Rollback()

	var n NotaClinica
	err = tx.QueryRowContext(ctx, `
INSERT INTO "NotaClinica" ("clinicaId", "citaId", "profesionalId", "contenido")
VALUES ((SELECT "id" FROM "Clinica" WHERE "identificador" = $1), $2, $3, $4)
RETURNING "id"::text, "citaId"::text, "profesionalId"::text, "contenido", "creadoEn"`,
		clinicaIdent, citaID, profesionalID, contenido,
	).Scan(&n.ID, &n.CitaID, &n.ProfesionalID, &n.Contenido, &n.CreadoEn)
	switch {
	case isUniqueViolation(err, "NotaClinicaCitaUq"):
		return NotaClinica{}, errNotaYaExiste
	case isCheckViolation(err) || isNotNullViolation(err):
		return NotaClinica{}, errNotaContenidoVacio
	case isForeignKeyViolation(err):
		return NotaClinica{}, errNotaCitaFuera
	case err != nil:
		return NotaClinica{}, fmt.Errorf("crear nota: %w", err)
	}

	// RF18: la escritura y su rastro hacen commit juntos.
	var pacienteID string
	if err := tx.QueryRowContext(ctx,
		`SELECT "pacienteId"::text FROM "Cita" WHERE "id" = $1`, citaID,
	).Scan(&pacienteID); err != nil {
		return NotaClinica{}, fmt.Errorf("resolver paciente de la cita: %w", err)
	}
	if err := insertAuditoria(ctx, tx, clinicaIdent, actor, AuditEvento{
		Accion:     accionEscritura,
		Entidad:    entidadNotaClinica,
		EntidadID:  n.ID,
		PacienteID: pacienteID,
		Metadata:   map[string]any{"citaId": citaID},
	}); err != nil {
		return NotaClinica{}, err
	}

	if err := tx.Commit(); err != nil {
		return NotaClinica{}, fmt.Errorf("commit nota: %w", err)
	}
	return n, nil
}

func (s *pgNotaStore) GetNota(ctx context.Context, clinicaIdent, citaID string) (NotaClinica, error) {
	var n NotaClinica
	err := dbq(ctx, s.db).QueryRowContext(ctx, `
SELECT n."id"::text, n."citaId"::text, n."profesionalId"::text, n."contenido", n."creadoEn"
FROM "NotaClinica" n
JOIN "Clinica" c ON c."id" = n."clinicaId"
WHERE c."identificador" = $1 AND n."citaId" = $2`,
		clinicaIdent, citaID,
	).Scan(&n.ID, &n.CitaID, &n.ProfesionalID, &n.Contenido, &n.CreadoEn)
	if errors.Is(err, sql.ErrNoRows) {
		return NotaClinica{}, errNotaNoExiste
	}
	if err != nil {
		return NotaClinica{}, fmt.Errorf("consultar nota: %w", err)
	}
	return n, nil
}

// notaStore es el store global (inyectable en tests).
var notaStore NotaStore

// --- Handlers ---

type createNotaRequest struct {
	Contenido string `json:"contenido"`
}

// resolverNotaOwnership valida existencia de la cita y que el usuario sea el
// profesional que la atendió. Responde y devuelve ok=false si rechaza.
func resolverNotaOwnership(c *gin.Context, clinica, citaID string) (CitaNotaInfo, string, bool) {
	du, ok := domainUserFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "token inválido o ausente"})
		return CitaNotaInfo{}, "", false
	}
	cita, err := notaStore.CitaParaNota(c.Request.Context(), clinica, citaID)
	switch {
	case errors.Is(err, errNotaCitaFuera):
		c.JSON(http.StatusNotFound, gin.H{"error": errNotaCitaFuera.Error()})
		return CitaNotaInfo{}, "", false
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo consultar la cita"})
		return CitaNotaInfo{}, "", false
	}
	profesionalID, err := notaStore.ProfesionalDeUsuario(c.Request.Context(), clinica, du.UsuarioID)
	switch {
	case errors.Is(err, errNotaSinProfesional):
		c.JSON(http.StatusForbidden, gin.H{"error": errNotaSinProfesional.Error()})
		return CitaNotaInfo{}, "", false
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo resolver el profesional"})
		return CitaNotaInfo{}, "", false
	}
	if cita.ProfesionalID != profesionalID {
		c.JSON(http.StatusForbidden, gin.H{"error": errNotaSoloPropia.Error()})
		return CitaNotaInfo{}, "", false
	}
	return cita, profesionalID, true
}

// createNota maneja POST /citas/:id/nota (RF15).
func createNota(c *gin.Context) {
	if notaStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "notas clínicas no disponibles"})
		return
	}
	clinica, ok := clinicaFromContext(c, false, "")
	if !ok {
		return
	}
	var req createNotaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cuerpo JSON inválido", "detalles": []string{err.Error()}})
		return
	}
	req.Contenido = strings.TrimSpace(req.Contenido)
	if req.Contenido == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": errNotaContenidoVacio.Error()})
		return
	}

	cita, profesionalID, ok := resolverNotaOwnership(c, clinica, c.Param("id"))
	if !ok {
		return
	}
	if cita.Estado != "atendida" {
		c.JSON(http.StatusConflict, gin.H{"error": errCitaNoAtendida.Error()})
		return
	}

	nota, err := notaStore.CreateNota(c.Request.Context(), clinica, cita.ID, profesionalID, req.Contenido, auditActorFromContext(c))
	switch {
	case errors.Is(err, errNotaYaExiste):
		c.JSON(http.StatusConflict, gin.H{"error": errNotaYaExiste.Error()})
	case errors.Is(err, errNotaContenidoVacio):
		c.JSON(http.StatusBadRequest, gin.H{"error": errNotaContenidoVacio.Error()})
	case errors.Is(err, errNotaCitaFuera):
		c.JSON(http.StatusNotFound, gin.H{"error": errNotaCitaFuera.Error()})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo crear la nota"})
	default:
		c.JSON(http.StatusCreated, gin.H{"clinicaIdentificador": clinica, "nota": nota})
	}
}

// getNota maneja GET /citas/:id/nota (RF15).
//
// RF18: la lectura se registra antes de responder; si no queda rastro, no se
// sirve el dato.
func getNota(c *gin.Context) {
	if notaStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "notas clínicas no disponibles"})
		return
	}
	if auditStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "bitácora no disponible"})
		return
	}
	clinica, ok := clinicaFromContext(c, false, "")
	if !ok {
		return
	}
	cita, _, ok := resolverNotaOwnership(c, clinica, c.Param("id"))
	if !ok {
		return
	}
	nota, err := notaStore.GetNota(c.Request.Context(), clinica, cita.ID)
	switch {
	case errors.Is(err, errNotaNoExiste):
		c.JSON(http.StatusNotFound, gin.H{"error": errNotaNoExiste.Error()})
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo consultar la nota"})
		return
	}

	if err := auditStore.Registrar(c.Request.Context(), clinica, auditActorFromContext(c), AuditEvento{
		Accion:     accionLectura,
		Entidad:    entidadNotaClinica,
		EntidadID:  nota.ID,
		PacienteID: cita.PacienteID,
		Metadata:   map[string]any{"citaId": cita.ID},
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo registrar el acceso en la bitácora"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"clinicaIdentificador": clinica, "nota": nota})
}
