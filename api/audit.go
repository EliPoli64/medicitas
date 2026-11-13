package main

// RF18: bitácora append-only de toda lectura y toda escritura de notas
// clínicas y de datos de contacto de pacientes (quién, qué, sobre cuál
// paciente y cuándo).
// RF19: consulta de la bitácora para adm-clinica con filtros.
//
// Tres decisiones sostienen este archivo:
//
//   - Append-only a nivel de motor: los triggers AuditoriaSinUpdate/SinDelete/
//     SinTruncate de db/migrations/01_schema.sql impiden mutar la tabla aunque
//     la aplicación se equivoque (§5). Aquí solo se hace INSERT.
//   - Escrituras atómicas: el evento de una escritura se inserta en la MISMA
//     transacción que el hecho (viaja con el *sql.Tx del store). O quedan el
//     hecho y su rastro, o no queda ninguno.
//   - Lecturas sin rastro no se sirven: Postgres no tiene triggers sobre
//     SELECT, así que la captura de lecturas es de la aplicación; si el INSERT
//     del evento falla, el handler responde error y NO entrega el dato.
//
// La metadata nunca contiene valores sensibles (ni valores de contacto ni
// contenido de notas): solo tipos, estados e ids. El dato sensible se consulta
// por su endpoint, nunca se duplica en la bitácora.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	accionLectura   = "lectura"
	accionEscritura = "escritura"

	entidadContactoPaciente = "contacto_paciente"
	entidadNotaClinica      = "nota_clinica"
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// AuditActor identifica al usuario auditable. SIEMPRE proviene del contexto
// autenticado (DomainUser + requestId), nunca del cuerpo de la petición.
type AuditActor struct {
	UsuarioID string
	IdpSub    string
	RequestID string
}

// AuditEvento es un hecho de RF18.
type AuditEvento struct {
	Accion     string         // lectura | escritura
	Entidad    string         // contacto_paciente | nota_clinica
	EntidadID  string         // id del objeto tocado (vacío si aplica a varios)
	PacienteID string         // "sobre cuál paciente"
	Metadata   map[string]any // nunca valores sensibles: tipos, estados, ids
}

// execer es *sql.DB (lecturas) o *sql.Tx (escrituras en la transacción del hecho).
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// insertAuditoria registra un evento en "Auditoria".
func insertAuditoria(ctx context.Context, ex execer, clinicaIdent string, actor AuditActor, ev AuditEvento) error {
	meta := map[string]any{}
	for k, v := range ev.Metadata {
		meta[k] = v
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("codificar metadata: %w", err)
	}
	_, err = ex.ExecContext(ctx, `
INSERT INTO "Auditoria" (
  "clinicaId", "usuarioId", "idpSub", "accion", "entidad",
  "entidadId", "pacienteId", "requestId", "metadata"
) VALUES (
  (SELECT "id" FROM "Clinica" WHERE "identificador" = $1),
  $2, $3, $4, $5, $6, $7, $8, $9::jsonb
)`,
		clinicaIdent,
		uuidOrNil(actor.UsuarioID),
		strings.TrimSpace(actor.IdpSub),
		ev.Accion,
		ev.Entidad,
		uuidOrNil(ev.EntidadID),
		uuidOrNil(ev.PacienteID),
		uuidOrNil(actor.RequestID),
		string(metaJSON),
	)
	if err != nil {
		return fmt.Errorf("registrar auditoría: %w", err)
	}
	return nil
}

func uuidOrNil(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return s
}

// BitacoraEntrada es una fila de la bitácora (RF19). Los campos vacíos
// significan "no aplica".
type BitacoraEntrada struct {
	ID         int64          `json:"id"`
	Accion     string         `json:"accion"`
	Entidad    string         `json:"entidad"`
	EntidadID  string         `json:"entidadId,omitempty"`
	UsuarioID  string         `json:"usuarioId,omitempty"`
	IdpSub     string         `json:"idpSub,omitempty"`
	PacienteID string         `json:"pacienteId,omitempty"`
	OcurridoEn time.Time      `json:"ocurridoEn"`
	RequestID  string         `json:"requestId,omitempty"`
	Metadata   map[string]any `json:"metadata"`
}

// BitacoraFiltro son los filtros de RF19. Las fechas son YYYY-MM-DD en la
// zona horaria de la clínica (el store las convierte a instantes).
type BitacoraFiltro struct {
	UsuarioID  string
	PacienteID string
	Desde      string
	Hasta      string
	Limit      int
}

// AuditStore abstrae la bitácora (tests sin Postgres).
type AuditStore interface {
	// Registrar guarda eventos de lectura. Las escrituras NO pasan por aquí:
	// se insertan en la transacción del hecho con insertAuditoria.
	Registrar(ctx context.Context, clinicaIdent string, actor AuditActor, evs ...AuditEvento) error
	// Listar consulta la bitácora de la clínica (RF19).
	Listar(ctx context.Context, clinicaIdent string, f BitacoraFiltro) ([]BitacoraEntrada, error)
}

type pgAuditStore struct {
	db *sql.DB
}

func newPGAuditStore(database *sql.DB) *pgAuditStore {
	return &pgAuditStore{db: database}
}

func (s *pgAuditStore) Registrar(ctx context.Context, clinicaIdent string, actor AuditActor, evs ...AuditEvento) error {
	for _, ev := range evs {
		if err := insertAuditoria(ctx, dbq(ctx, s.db), clinicaIdent, actor, ev); err != nil {
			return err
		}
	}
	return nil
}

func (s *pgAuditStore) Listar(ctx context.Context, clinicaIdent string, f BitacoraFiltro) ([]BitacoraEntrada, error) {
	rows, err := dbq(ctx, s.db).QueryContext(ctx, `
SELECT a."id", a."accion", a."entidad",
       COALESCE(a."entidadId"::text, ''), COALESCE(a."usuarioId"::text, ''),
       COALESCE(a."idpSub", ''), COALESCE(a."pacienteId"::text, ''),
       a."ocurridoEn", COALESCE(a."requestId"::text, ''), a."metadata"::text
FROM "Auditoria" a
WHERE a."clinicaId" = (SELECT "id" FROM "Clinica" WHERE "identificador" = $1)
  AND ($2::uuid IS NULL OR a."usuarioId" = $2::uuid)
  AND ($3::uuid IS NULL OR a."pacienteId" = $3::uuid)
  AND ($4::date IS NULL OR a."ocurridoEn" >= ($4::date::timestamp AT TIME ZONE
       (SELECT "zonaHoraria" FROM "Clinica" WHERE "identificador" = $1)))
  AND ($5::date IS NULL OR a."ocurridoEn" < (($5::date + 1)::timestamp AT TIME ZONE
       (SELECT "zonaHoraria" FROM "Clinica" WHERE "identificador" = $1)))
ORDER BY a."id" DESC
LIMIT $6`,
		clinicaIdent,
		uuidOrNil(f.UsuarioID),
		uuidOrNil(f.PacienteID),
		nullableFecha(f.Desde),
		nullableFecha(f.Hasta),
		f.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("consultar bitácora: %w", err)
	}
	defer rows.Close()

	out := make([]BitacoraEntrada, 0)
	for rows.Next() {
		var e BitacoraEntrada
		var meta string
		if err := rows.Scan(&e.ID, &e.Accion, &e.Entidad, &e.EntidadID,
			&e.UsuarioID, &e.IdpSub, &e.PacienteID,
			&e.OcurridoEn, &e.RequestID, &meta); err != nil {
			return nil, fmt.Errorf("leer bitácora: %w", err)
		}
		e.Metadata = map[string]any{}
		if strings.TrimSpace(meta) != "" {
			if err := json.Unmarshal([]byte(meta), &e.Metadata); err != nil {
				return nil, fmt.Errorf("decodificar metadata: %w", err)
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func nullableFecha(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return s
}

// auditStore es el store global (inyectable en tests).
var auditStore AuditStore

// --- requestId por petición (correlación en la bitácora) ---

const ctxRequestIDKey = "requestID"

// requestID genera un uuid v4 por petición y lo deja en el contexto.
func requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(ctxRequestIDKey, newRequestID())
		c.Next()
	}
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	b[6] = (b[6] & 0x0f) | 0x40 // versión 4
	b[8] = (b[8] & 0x3f) | 0x80 // variante RFC 4122
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func requestIDFromContext(c *gin.Context) string {
	v, _ := c.Get(ctxRequestIDKey)
	s, _ := v.(string)
	return s
}

// auditActorFromContext arma el actor auditable. Va después de loadDomainUser.
func auditActorFromContext(c *gin.Context) AuditActor {
	du, ok := domainUserFromContext(c)
	if !ok {
		return AuditActor{RequestID: requestIDFromContext(c)}
	}
	return AuditActor{
		UsuarioID: du.UsuarioID,
		IdpSub:    du.IdpSub,
		RequestID: requestIDFromContext(c),
	}
}

// --- RF19: consulta de la bitácora ---

const (
	bitacoraLimitDefault = 100
	bitacoraLimitMax     = 500
)

// listBitacora maneja GET /bitacora (RF19).
//
// Query: usuarioId, pacienteId (uuid), desde, hasta (YYYY-MM-DD, zona de la
// clínica), limit (1–500). Solo adm-clinica (OpBitacora). El tenant es
// SIEMPRE el del token (I5): los filtros no pueden cruzar de clínica.
func listBitacora(c *gin.Context) {
	if auditStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "bitácora no disponible"})
		return
	}
	clinica, ok := clinicaFromContext(c, false, "")
	if !ok {
		return
	}

	var f BitacoraFiltro
	var errs []string
	if v := strings.TrimSpace(c.Query("usuarioId")); v != "" {
		if !uuidRe.MatchString(v) {
			errs = append(errs, "usuarioId debe ser uuid")
		} else {
			f.UsuarioID = v
		}
	}
	if v := strings.TrimSpace(c.Query("pacienteId")); v != "" {
		if !uuidRe.MatchString(v) {
			errs = append(errs, "pacienteId debe ser uuid")
		} else {
			f.PacienteID = v
		}
	}
	f.Desde = strings.TrimSpace(c.Query("desde"))
	if f.Desde != "" && !fechaRe.MatchString(f.Desde) {
		errs = append(errs, "desde debe ser YYYY-MM-DD")
	}
	f.Hasta = strings.TrimSpace(c.Query("hasta"))
	if f.Hasta != "" && !fechaRe.MatchString(f.Hasta) {
		errs = append(errs, "hasta debe ser YYYY-MM-DD")
	}
	if f.Desde != "" && f.Hasta != "" && f.Desde > f.Hasta {
		errs = append(errs, "desde debe ser <= hasta")
	}
	f.Limit = bitacoraLimitDefault
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > bitacoraLimitMax {
			errs = append(errs, fmt.Sprintf("limit debe estar entre 1 y %d", bitacoraLimitMax))
		} else {
			f.Limit = n
		}
	}
	if len(errs) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida", "detalles": errs})
		return
	}

	entradas, err := auditStore.Listar(c.Request.Context(), clinica, f)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo consultar la bitácora"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"clinicaIdentificador": clinica,
		"limite":               f.Limit,
		"entradas":             entradas,
	})
}
