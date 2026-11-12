package main

// RF6: bloqueos de agenda (vacaciones/permisos). No cancelan citas; al crear
// se listan las citas reservadas solapadas como conflictos. RF7 invalidará
// caché al mutar bloqueos.

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
	errBloqueoFueraDeClinica = errors.New("bloqueo no encontrado en la clínica")
	errBloqueoDatosInvalidos = errors.New("datos de bloqueo inválidos")
)

// Block es la proyección de "Bloqueo".
type Block struct {
	ID                 string `json:"id"`
	ProfesionalID      string `json:"profesionalId"`
	Inicio             string `json:"inicio"`
	Fin                string `json:"fin"`
	Motivo             string `json:"motivo"`
	CreadoPorUsuarioID string `json:"creadoPorUsuarioId"`
	CreadoEn           string `json:"creadoEn"`
}

// BlockConflict es una cita reservada que solapa el bloqueo (RF6).
type BlockConflict struct {
	CitaID     string `json:"citaId"`
	Inicio     string `json:"inicio"`
	Fin        string `json:"fin"`
	Estado     string `json:"estado"`
	PacienteID string `json:"pacienteId"`
}

// BlockStore abstrae bloqueos para tests.
type BlockStore interface {
	ListBloqueos(ctx context.Context, clinicaIdent, profesionalID string, desde, hasta *time.Time) ([]Block, error)
	CreateBloqueo(ctx context.Context, clinicaIdent, profesionalID, creadoPorUsuarioID string, inicio, fin time.Time, motivo string) (Block, []BlockConflict, error)
	DeleteBloqueo(ctx context.Context, clinicaIdent, bloqueoID string) (profesionalID string, err error)
	BloqueoProfesionalID(ctx context.Context, clinicaIdent, bloqueoID string) (string, error)
}

type pgBlockStore struct {
	db *sql.DB
}

func newPGBlockStore(database *sql.DB) *pgBlockStore {
	return &pgBlockStore{db: database}
}

func scanBlock(scanner interface {
	Scan(dest ...any) error
}) (Block, error) {
	var b Block
	var inicio, fin, creado time.Time
	err := scanner.Scan(
		&b.ID, &b.ProfesionalID, &inicio, &fin, &b.Motivo, &b.CreadoPorUsuarioID, &creado,
	)
	if err != nil {
		return Block{}, err
	}
	b.Inicio = inicio.UTC().Format(time.RFC3339)
	b.Fin = fin.UTC().Format(time.RFC3339)
	b.CreadoEn = creado.UTC().Format(time.RFC3339)
	return b, nil
}

func (s *pgBlockStore) BloqueoProfesionalID(ctx context.Context, clinicaIdent, bloqueoID string) (string, error) {
	var profesionalID string
	err := s.db.QueryRowContext(ctx, `
SELECT b."profesionalId"::text
FROM "Bloqueo" b
JOIN "Clinica" c ON c."id" = b."clinicaId"
WHERE c."identificador" = $1 AND b."id" = $2`,
		clinicaIdent, bloqueoID,
	).Scan(&profesionalID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errBloqueoFueraDeClinica
	}
	if err != nil {
		return "", fmt.Errorf("consultar bloqueo: %w", err)
	}
	return profesionalID, nil
}

func (s *pgBlockStore) ListBloqueos(ctx context.Context, clinicaIdent, profesionalID string, desde, hasta *time.Time) ([]Block, error) {
	q := `
SELECT b."id"::text, b."profesionalId"::text, b."inicio", b."fin",
       b."motivo", b."creadoPorUsuarioId"::text, b."creadoEn"
FROM "Bloqueo" b
JOIN "Clinica" c ON c."id" = b."clinicaId"
WHERE c."identificador" = $1 AND b."profesionalId" = $2`
	args := []any{clinicaIdent, profesionalID}
	n := 3
	if desde != nil {
		q += fmt.Sprintf(` AND b."fin" > $%d`, n)
		args = append(args, *desde)
		n++
	}
	if hasta != nil {
		q += fmt.Sprintf(` AND b."inicio" < $%d`, n)
		args = append(args, *hasta)
	}
	q += ` ORDER BY b."inicio"`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("listar bloqueos: %w", err)
	}
	defer rows.Close()

	out := make([]Block, 0)
	for rows.Next() {
		b, err := scanBlock(rows)
		if err != nil {
			return nil, fmt.Errorf("leer bloqueo: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *pgBlockStore) listConflictos(ctx context.Context, clinicaIdent, profesionalID string, inicio, fin time.Time) ([]BlockConflict, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT ci."id"::text, ci."inicio", ci."fin", ci."estado"::text, ci."pacienteId"::text
FROM "Cita" ci
JOIN "Clinica" c ON c."id" = ci."clinicaId"
WHERE c."identificador" = $1
  AND ci."profesionalId" = $2
  AND ci."estado" = 'reservada'
  AND tstzrange(ci."inicio", ci."fin", '[)') && tstzrange($3::timestamptz, $4::timestamptz, '[)')
ORDER BY ci."inicio"`,
		clinicaIdent, profesionalID, inicio, fin,
	)
	if err != nil {
		return nil, fmt.Errorf("listar conflictos: %w", err)
	}
	defer rows.Close()

	out := make([]BlockConflict, 0)
	for rows.Next() {
		var cf BlockConflict
		var i, f time.Time
		if err := rows.Scan(&cf.CitaID, &i, &f, &cf.Estado, &cf.PacienteID); err != nil {
			return nil, fmt.Errorf("leer conflicto: %w", err)
		}
		cf.Inicio = i.UTC().Format(time.RFC3339)
		cf.Fin = f.UTC().Format(time.RFC3339)
		out = append(out, cf)
	}
	return out, rows.Err()
}

func (s *pgBlockStore) CreateBloqueo(ctx context.Context, clinicaIdent, profesionalID, creadoPorUsuarioID string, inicio, fin time.Time, motivo string) (Block, []BlockConflict, error) {
	row := s.db.QueryRowContext(ctx, `
INSERT INTO "Bloqueo" (
  "clinicaId", "profesionalId", "inicio", "fin", "motivo", "creadoPorUsuarioId"
) VALUES (
  (SELECT "id" FROM "Clinica" WHERE "identificador" = $1),
  $2, $3, $4, $5, $6
)
RETURNING "id"::text, "profesionalId"::text, "inicio", "fin",
          "motivo", "creadoPorUsuarioId"::text, "creadoEn"`,
		clinicaIdent, profesionalID, inicio, fin, motivo, creadoPorUsuarioID,
	)
	b, err := scanBlock(row)
	if isCheckViolation(err) || isNotNullViolation(err) {
		return Block{}, nil, errBloqueoDatosInvalidos
	}
	if isForeignKeyViolation(err) {
		return Block{}, nil, errProfesionalAgendaFuera
	}
	if err != nil {
		return Block{}, nil, fmt.Errorf("crear bloqueo: %w", err)
	}
	conflicts, err := s.listConflictos(ctx, clinicaIdent, profesionalID, inicio, fin)
	if err != nil {
		return b, nil, err
	}
	return b, conflicts, nil
}

func (s *pgBlockStore) DeleteBloqueo(ctx context.Context, clinicaIdent, bloqueoID string) (string, error) {
	var profesionalID string
	err := s.db.QueryRowContext(ctx, `
DELETE FROM "Bloqueo" b
USING "Clinica" c
WHERE c."id" = b."clinicaId" AND c."identificador" = $1 AND b."id" = $2
RETURNING b."profesionalId"::text`,
		clinicaIdent, bloqueoID,
	).Scan(&profesionalID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errBloqueoFueraDeClinica
	}
	if err != nil {
		return "", fmt.Errorf("eliminar bloqueo: %w", err)
	}
	return profesionalID, nil
}

var blockStore BlockStore

type createBloqueoRequest struct {
	Inicio string `json:"inicio"`
	Fin    string `json:"fin"`
	Motivo string `json:"motivo"`
}

func parseRFC3339Flexible(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

func listBloqueos(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, blockStore == nil || scheduleStore == nil, "servicio de agenda no disponible")
	if !ok {
		return
	}
	profesionalID := c.Param("id")
	if !assertPuedeGestionarAgenda(c, clinica, profesionalID) {
		return
	}
	var desde, hasta *time.Time
	if q := c.Query("desde"); q != "" {
		t, err := parseRFC3339Flexible(q)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "desde debe ser timestamptz RFC3339"})
			return
		}
		desde = &t
	}
	if q := c.Query("hasta"); q != "" {
		t, err := parseRFC3339Flexible(q)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "hasta debe ser timestamptz RFC3339"})
			return
		}
		hasta = &t
	}
	list, err := blockStore.ListBloqueos(c.Request.Context(), clinica, profesionalID, desde, hasta)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudieron listar los bloqueos"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"clinicaIdentificador": clinica,
		"profesionalId":        profesionalID,
		"bloqueos":             list,
	})
}

func createBloqueo(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, blockStore == nil || scheduleStore == nil, "servicio de agenda no disponible")
	if !ok {
		return
	}
	profesionalID := c.Param("id")
	if !assertPuedeGestionarAgenda(c, clinica, profesionalID) {
		return
	}
	du, ok := domainUserFromContext(c)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "usuario de dominio no disponible"})
		return
	}

	var req createBloqueoRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cuerpo JSON inválido", "detalles": []string{err.Error()}})
		return
	}
	var errs []string
	inicio, errInicio := parseRFC3339Flexible(req.Inicio)
	if errInicio != nil {
		errs = append(errs, "inicio debe ser timestamptz RFC3339")
	}
	fin, errFin := parseRFC3339Flexible(req.Fin)
	if errFin != nil {
		errs = append(errs, "fin debe ser timestamptz RFC3339")
	}
	if errInicio == nil && errFin == nil && !fin.After(inicio) {
		errs = append(errs, "fin debe ser posterior a inicio")
	}
	if strings.TrimSpace(req.Motivo) == "" {
		errs = append(errs, "motivo es obligatorio")
	}
	if len(errs) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida", "detalles": errs})
		return
	}

	b, conflicts, err := blockStore.CreateBloqueo(
		c.Request.Context(), clinica, profesionalID, du.UsuarioID, inicio, fin, strings.TrimSpace(req.Motivo),
	)
	// CreateBloqueo inserta en autocommit y después lista los conflictos. Si
	// ese listado falla, la fila ya está confirmada aunque el handler responda
	// error; la invalidación debe cubrir también ese caso parcial.
	if b.ID != "" {
		registrarFalloInvalidacion(invalidarDisponibilidadDeAgenda(c.Request.Context(), clinica, profesionalID))
	}
	switch {
	case errors.Is(err, errProfesionalAgendaFuera):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, errBloqueoDatosInvalidos):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo crear el bloqueo"})
	default:
		if conflicts == nil {
			conflicts = []BlockConflict{}
		}
		c.JSON(http.StatusCreated, gin.H{
			"clinicaIdentificador": clinica,
			"bloqueo":              b,
			"conflictos":           conflicts,
		})
	}
}

func deleteBloqueo(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, blockStore == nil || scheduleStore == nil, "servicio de agenda no disponible")
	if !ok {
		return
	}
	bloqueoID := c.Param("id")
	profesionalID, err := blockStore.BloqueoProfesionalID(c.Request.Context(), clinica, bloqueoID)
	if errors.Is(err, errBloqueoFueraDeClinica) {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo resolver el bloqueo"})
		return
	}
	if !assertPuedeGestionarAgenda(c, clinica, profesionalID) {
		return
	}
	_, err = blockStore.DeleteBloqueo(c.Request.Context(), clinica, bloqueoID)
	switch {
	case errors.Is(err, errBloqueoFueraDeClinica):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo eliminar el bloqueo"})
	default:
		// El DELETE ya está confirmado. La invalidación es posterior al
		// COMMIT y su fallo no cambia la respuesta de la mutación.
		registrarFalloInvalidacion(invalidarDisponibilidadDeAgenda(c.Request.Context(), clinica, profesionalID))
		c.Status(http.StatusNoContent)
	}
}
