package main

// RF14: registrar/actualizar pacientes y contactos.
// RF16: historial de citas del paciente en su clínica.
//
// Lectura: OpPacientesLectura / OpHistorialPaciente.
// Escritura: OpPacientesEscritura (adm-clinica, recepcion).
// Tenant siempre desde DomainUser (I5).

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
	errPacienteFueraDeClinica   = errors.New("paciente no encontrado en la clínica")
	errPacienteDuplicado        = errors.New("ya existe un paciente con esa identificación en la clínica")
	errPacienteDatosInvalidos   = errors.New("datos de paciente inválidos")
	errContactoFueraDeClinica   = errors.New("contacto no encontrado en la clínica")
	errContactoTipoInvalido     = errors.New("tipo de contacto inválido")
	errContactoDatosInvalidos   = errors.New("datos de contacto inválidos")
	errContactoPrincipalDup     = errors.New("ya hay un contacto principal activo de ese tipo")
)

// Patient es la proyección de "Paciente".
type Patient struct {
	ID              string           `json:"id"`
	Identificacion  string           `json:"identificacion"`
	Nombre          string           `json:"nombre"`
	FechaNacimiento string           `json:"fechaNacimiento"`
	Activo          bool             `json:"activo"`
	Contactos       []PatientContact `json:"contactos,omitempty"`
}

// PatientContact es la proyección de "ContactoPaciente".
type PatientContact struct {
	ID         string `json:"id"`
	PacienteID string `json:"pacienteId"`
	TipoCodigo string `json:"tipoCodigo"`
	TipoNombre string `json:"tipoNombre"`
	Valor      string `json:"valor"`
	EsPrincipal bool  `json:"esPrincipal"`
	Activo     bool   `json:"activo"`
}

// PatientAppointment es una cita del historial (RF16).
type PatientAppointment struct {
	ID              string `json:"id"`
	Inicio          string `json:"inicio"`
	Fin             string `json:"fin"`
	Estado          string `json:"estado"`
	ProfesionalID   string `json:"profesionalId"`
	ServicioID      string `json:"servicioId"`
	DuracionMinutos int    `json:"duracionMinutos"`
}

// ContactInput es la entrada de un contacto al crear/actualizar.
type ContactInput struct {
	TipoCodigo  string `json:"tipoCodigo"`
	Valor       string `json:"valor"`
	EsPrincipal bool   `json:"esPrincipal"`
}

// PatientStore abstrae pacientes/contactos/historial.
type PatientStore interface {
	ListActiveByClinicaIdentificador(ctx context.Context, clinicaIdentificador string) ([]Patient, error)
	GetPaciente(ctx context.Context, clinicaIdent, id string) (Patient, error)
	CreatePaciente(ctx context.Context, clinicaIdent, identificacion, nombre, fechaNac string, contactos []ContactInput) (Patient, error)
	UpdatePaciente(ctx context.Context, clinicaIdent, id string, patch PatientPatch) (Patient, error)
	CreateContacto(ctx context.Context, clinicaIdent, pacienteID string, in ContactInput) (PatientContact, error)
	UpdateContacto(ctx context.Context, clinicaIdent, contactoID string, patch ContactPatch) (PatientContact, error)
	ListCitasByPaciente(ctx context.Context, clinicaIdent, pacienteID string) ([]PatientAppointment, error)
}

// PatientPatch campos opcionales de PATCH /pacientes/:id.
type PatientPatch struct {
	Identificacion  *string
	Nombre          *string
	FechaNacimiento *string
	Activo          *bool
}

// ContactPatch campos opcionales de PATCH /contactos/:id.
type ContactPatch struct {
	Valor       *string
	EsPrincipal *bool
	Activo      *bool
}

type pgPatientStore struct {
	db *sql.DB
}

func newPGPatientStore(database *sql.DB) *pgPatientStore {
	return &pgPatientStore{db: database}
}

func (s *pgPatientStore) ListActiveByClinicaIdentificador(ctx context.Context, clinicaIdentificador string) ([]Patient, error) {
	const q = `
SELECT p."id"::text, p."identificacion", p."nombre",
       p."fechaNacimiento"::text, p."activo"
FROM "Paciente" p
JOIN "Clinica" c ON c."id" = p."clinicaId"
WHERE c."identificador" = $1
  AND p."activo" = true
ORDER BY p."nombre"`

	rows, err := s.db.QueryContext(ctx, q, clinicaIdentificador)
	if err != nil {
		return nil, fmt.Errorf("listar pacientes: %w", err)
	}
	defer rows.Close()

	out := make([]Patient, 0)
	for rows.Next() {
		var p Patient
		if err := rows.Scan(&p.ID, &p.Identificacion, &p.Nombre, &p.FechaNacimiento, &p.Activo); err != nil {
			return nil, fmt.Errorf("leer paciente: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *pgPatientStore) listContactos(ctx context.Context, tx *sql.Tx, clinicaIdent, pacienteID string, soloActivos bool) ([]PatientContact, error) {
	q := `
SELECT cp."id"::text, cp."pacienteId"::text, tc."codigo", tc."nombre",
       cp."valor", cp."esPrincipal", cp."activo"
FROM "ContactoPaciente" cp
JOIN "Clinica" c ON c."id" = cp."clinicaId"
JOIN "TipoContacto" tc ON tc."id" = cp."tipoContactoId"
WHERE c."identificador" = $1 AND cp."pacienteId" = $2`
	if soloActivos {
		q += ` AND cp."activo" = true`
	}
	q += ` ORDER BY tc."codigo", cp."esPrincipal" DESC, cp."valor"`

	var rows *sql.Rows
	var err error
	if tx != nil {
		rows, err = tx.QueryContext(ctx, q, clinicaIdent, pacienteID)
	} else {
		rows, err = s.db.QueryContext(ctx, q, clinicaIdent, pacienteID)
	}
	if err != nil {
		return nil, fmt.Errorf("listar contactos: %w", err)
	}
	defer rows.Close()

	out := make([]PatientContact, 0)
	for rows.Next() {
		var ct PatientContact
		if err := rows.Scan(&ct.ID, &ct.PacienteID, &ct.TipoCodigo, &ct.TipoNombre, &ct.Valor, &ct.EsPrincipal, &ct.Activo); err != nil {
			return nil, fmt.Errorf("leer contacto: %w", err)
		}
		out = append(out, ct)
	}
	return out, rows.Err()
}

func (s *pgPatientStore) GetPaciente(ctx context.Context, clinicaIdent, id string) (Patient, error) {
	var p Patient
	err := s.db.QueryRowContext(ctx, `
SELECT p."id"::text, p."identificacion", p."nombre",
       p."fechaNacimiento"::text, p."activo"
FROM "Paciente" p
JOIN "Clinica" c ON c."id" = p."clinicaId"
WHERE c."identificador" = $1 AND p."id" = $2`,
		clinicaIdent, id,
	).Scan(&p.ID, &p.Identificacion, &p.Nombre, &p.FechaNacimiento, &p.Activo)
	if errors.Is(err, sql.ErrNoRows) {
		return Patient{}, errPacienteFueraDeClinica
	}
	if err != nil {
		return Patient{}, fmt.Errorf("consultar paciente: %w", err)
	}
	cts, err := s.listContactos(ctx, nil, clinicaIdent, id, true)
	if err != nil {
		return Patient{}, err
	}
	p.Contactos = cts
	return p, nil
}

func (s *pgPatientStore) insertContactoTx(ctx context.Context, tx *sql.Tx, clinicaIdent, pacienteID string, in ContactInput) (PatientContact, error) {
	var tipoID string
	err := tx.QueryRowContext(ctx,
		`SELECT "id"::text FROM "TipoContacto" WHERE "codigo" = $1`, in.TipoCodigo,
	).Scan(&tipoID)
	if errors.Is(err, sql.ErrNoRows) {
		return PatientContact{}, errContactoTipoInvalido
	}
	if err != nil {
		return PatientContact{}, fmt.Errorf("consultar tipo contacto: %w", err)
	}

	if in.EsPrincipal {
		_, err = tx.ExecContext(ctx, `
UPDATE "ContactoPaciente" cp SET "esPrincipal" = false, "actualizadoEn" = now()
FROM "Clinica" c
WHERE c."id" = cp."clinicaId" AND c."identificador" = $1
  AND cp."pacienteId" = $2 AND cp."tipoContactoId" = $3
  AND cp."esPrincipal" = true AND cp."activo" = true`,
			clinicaIdent, pacienteID, tipoID,
		)
		if err != nil {
			return PatientContact{}, fmt.Errorf("limpiar principal: %w", err)
		}
	}

	var ct PatientContact
	err = tx.QueryRowContext(ctx, `
INSERT INTO "ContactoPaciente" (
  "clinicaId", "pacienteId", "tipoContactoId", "valor", "esPrincipal"
) VALUES (
  (SELECT "id" FROM "Clinica" WHERE "identificador" = $1),
  $2, $3, $4, $5
)
RETURNING "id"::text, "pacienteId"::text, $6, (SELECT "nombre" FROM "TipoContacto" WHERE "id" = $3),
          "valor", "esPrincipal", "activo"`,
		clinicaIdent, pacienteID, tipoID, in.Valor, in.EsPrincipal, in.TipoCodigo,
	).Scan(&ct.ID, &ct.PacienteID, &ct.TipoCodigo, &ct.TipoNombre, &ct.Valor, &ct.EsPrincipal, &ct.Activo)
	if isUniqueViolation(err, "ContactoPacientePrincipalUq") {
		return PatientContact{}, errContactoPrincipalDup
	}
	if isCheckViolation(err) || isNotNullViolation(err) {
		return PatientContact{}, errContactoDatosInvalidos
	}
	if isForeignKeyViolation(err) {
		return PatientContact{}, errPacienteFueraDeClinica
	}
	if err != nil {
		return PatientContact{}, fmt.Errorf("crear contacto: %w", err)
	}
	return ct, nil
}

func (s *pgPatientStore) CreatePaciente(ctx context.Context, clinicaIdent, identificacion, nombre, fechaNac string, contactos []ContactInput) (Patient, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Patient{}, fmt.Errorf("abrir transacción: %w", err)
	}
	defer tx.Rollback()

	var p Patient
	err = tx.QueryRowContext(ctx, `
INSERT INTO "Paciente" ("clinicaId", "identificacion", "nombre", "fechaNacimiento")
VALUES ((SELECT "id" FROM "Clinica" WHERE "identificador" = $1), $2, $3, $4::date)
RETURNING "id"::text, "identificacion", "nombre", "fechaNacimiento"::text, "activo"`,
		clinicaIdent, identificacion, nombre, fechaNac,
	).Scan(&p.ID, &p.Identificacion, &p.Nombre, &p.FechaNacimiento, &p.Activo)
	if isUniqueViolation(err, "PacienteIdentificacionUq") {
		return Patient{}, errPacienteDuplicado
	}
	if isCheckViolation(err) || isNotNullViolation(err) {
		return Patient{}, errPacienteDatosInvalidos
	}
	if err != nil {
		return Patient{}, fmt.Errorf("crear paciente: %w", err)
	}

	p.Contactos = make([]PatientContact, 0, len(contactos))
	for _, in := range contactos {
		ct, err := s.insertContactoTx(ctx, tx, clinicaIdent, p.ID, in)
		if err != nil {
			return Patient{}, err
		}
		p.Contactos = append(p.Contactos, ct)
	}
	if err := tx.Commit(); err != nil {
		return Patient{}, fmt.Errorf("commit paciente: %w", err)
	}
	return p, nil
}

func (s *pgPatientStore) UpdatePaciente(ctx context.Context, clinicaIdent, id string, patch PatientPatch) (Patient, error) {
	cur, err := s.GetPaciente(ctx, clinicaIdent, id)
	if err != nil {
		return Patient{}, err
	}
	if patch.Identificacion != nil {
		cur.Identificacion = *patch.Identificacion
	}
	if patch.Nombre != nil {
		cur.Nombre = *patch.Nombre
	}
	if patch.FechaNacimiento != nil {
		cur.FechaNacimiento = *patch.FechaNacimiento
	}
	if patch.Activo != nil {
		cur.Activo = *patch.Activo
	}

	var p Patient
	err = s.db.QueryRowContext(ctx, `
UPDATE "Paciente" p SET
  "identificacion" = $3,
  "nombre" = $4,
  "fechaNacimiento" = $5::date,
  "activo" = $6,
  "actualizadoEn" = now()
FROM "Clinica" c
WHERE c."id" = p."clinicaId" AND c."identificador" = $1 AND p."id" = $2
RETURNING p."id"::text, p."identificacion", p."nombre", p."fechaNacimiento"::text, p."activo"`,
		clinicaIdent, id, cur.Identificacion, cur.Nombre, cur.FechaNacimiento, cur.Activo,
	).Scan(&p.ID, &p.Identificacion, &p.Nombre, &p.FechaNacimiento, &p.Activo)
	if errors.Is(err, sql.ErrNoRows) {
		return Patient{}, errPacienteFueraDeClinica
	}
	if isUniqueViolation(err, "PacienteIdentificacionUq") {
		return Patient{}, errPacienteDuplicado
	}
	if isCheckViolation(err) || isNotNullViolation(err) {
		return Patient{}, errPacienteDatosInvalidos
	}
	if err != nil {
		return Patient{}, fmt.Errorf("actualizar paciente: %w", err)
	}
	cts, err := s.listContactos(ctx, nil, clinicaIdent, id, true)
	if err != nil {
		return Patient{}, err
	}
	p.Contactos = cts
	return p, nil
}

func (s *pgPatientStore) CreateContacto(ctx context.Context, clinicaIdent, pacienteID string, in ContactInput) (PatientContact, error) {
	if _, err := s.GetPaciente(ctx, clinicaIdent, pacienteID); err != nil {
		return PatientContact{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PatientContact{}, fmt.Errorf("abrir transacción: %w", err)
	}
	defer tx.Rollback()
	ct, err := s.insertContactoTx(ctx, tx, clinicaIdent, pacienteID, in)
	if err != nil {
		return PatientContact{}, err
	}
	if err := tx.Commit(); err != nil {
		return PatientContact{}, fmt.Errorf("commit contacto: %w", err)
	}
	return ct, nil
}

func (s *pgPatientStore) UpdateContacto(ctx context.Context, clinicaIdent, contactoID string, patch ContactPatch) (PatientContact, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PatientContact{}, fmt.Errorf("abrir transacción: %w", err)
	}
	defer tx.Rollback()

	var cur PatientContact
	var tipoID string
	err = tx.QueryRowContext(ctx, `
SELECT cp."id"::text, cp."pacienteId"::text, tc."codigo", tc."nombre",
       cp."valor", cp."esPrincipal", cp."activo", cp."tipoContactoId"::text
FROM "ContactoPaciente" cp
JOIN "Clinica" c ON c."id" = cp."clinicaId"
JOIN "TipoContacto" tc ON tc."id" = cp."tipoContactoId"
WHERE c."identificador" = $1 AND cp."id" = $2`,
		clinicaIdent, contactoID,
	).Scan(&cur.ID, &cur.PacienteID, &cur.TipoCodigo, &cur.TipoNombre, &cur.Valor, &cur.EsPrincipal, &cur.Activo, &tipoID)
	if errors.Is(err, sql.ErrNoRows) {
		return PatientContact{}, errContactoFueraDeClinica
	}
	if err != nil {
		return PatientContact{}, fmt.Errorf("leer contacto: %w", err)
	}

	if patch.Valor != nil {
		cur.Valor = *patch.Valor
	}
	if patch.EsPrincipal != nil {
		cur.EsPrincipal = *patch.EsPrincipal
	}
	if patch.Activo != nil {
		cur.Activo = *patch.Activo
	}

	if cur.EsPrincipal && cur.Activo {
		_, err = tx.ExecContext(ctx, `
UPDATE "ContactoPaciente" cp SET "esPrincipal" = false, "actualizadoEn" = now()
FROM "Clinica" c
WHERE c."id" = cp."clinicaId" AND c."identificador" = $1
  AND cp."pacienteId" = $2 AND cp."tipoContactoId" = $3
  AND cp."id" <> $4
  AND cp."esPrincipal" = true AND cp."activo" = true`,
			clinicaIdent, cur.PacienteID, tipoID, contactoID,
		)
		if err != nil {
			return PatientContact{}, fmt.Errorf("limpiar principal: %w", err)
		}
	}

	var out PatientContact
	err = tx.QueryRowContext(ctx, `
UPDATE "ContactoPaciente" cp SET
  "valor" = $3, "esPrincipal" = $4, "activo" = $5, "actualizadoEn" = now()
FROM "Clinica" c
WHERE c."id" = cp."clinicaId" AND c."identificador" = $1 AND cp."id" = $2
RETURNING cp."id"::text, cp."pacienteId"::text,
          (SELECT "codigo" FROM "TipoContacto" WHERE "id" = cp."tipoContactoId"),
          (SELECT "nombre" FROM "TipoContacto" WHERE "id" = cp."tipoContactoId"),
          cp."valor", cp."esPrincipal", cp."activo"`,
		clinicaIdent, contactoID, cur.Valor, cur.EsPrincipal, cur.Activo,
	).Scan(&out.ID, &out.PacienteID, &out.TipoCodigo, &out.TipoNombre, &out.Valor, &out.EsPrincipal, &out.Activo)
	if errors.Is(err, sql.ErrNoRows) {
		return PatientContact{}, errContactoFueraDeClinica
	}
	if isUniqueViolation(err, "ContactoPacientePrincipalUq") {
		return PatientContact{}, errContactoPrincipalDup
	}
	if isCheckViolation(err) || isNotNullViolation(err) {
		return PatientContact{}, errContactoDatosInvalidos
	}
	if err != nil {
		return PatientContact{}, fmt.Errorf("actualizar contacto: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return PatientContact{}, fmt.Errorf("commit contacto: %w", err)
	}
	return out, nil
}

func (s *pgPatientStore) ListCitasByPaciente(ctx context.Context, clinicaIdent, pacienteID string) ([]PatientAppointment, error) {
	// Asegura que el paciente existe en la clínica (404 vs lista vacía).
	var ok bool
	err := s.db.QueryRowContext(ctx, `
SELECT true FROM "Paciente" p
JOIN "Clinica" c ON c."id" = p."clinicaId"
WHERE c."identificador" = $1 AND p."id" = $2`,
		clinicaIdent, pacienteID,
	).Scan(&ok)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errPacienteFueraDeClinica
	}
	if err != nil {
		return nil, fmt.Errorf("verificar paciente: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT ci."id"::text, ci."inicio", ci."fin", ci."estado"::text,
       ci."profesionalId"::text, ci."servicioId"::text, ci."duracionMinutos"
FROM "Cita" ci
JOIN "Clinica" c ON c."id" = ci."clinicaId"
WHERE c."identificador" = $1 AND ci."pacienteId" = $2
ORDER BY ci."inicio" DESC`,
		clinicaIdent, pacienteID,
	)
	if err != nil {
		return nil, fmt.Errorf("listar citas: %w", err)
	}
	defer rows.Close()

	out := make([]PatientAppointment, 0)
	for rows.Next() {
		var a PatientAppointment
		var inicio, fin time.Time
		if err := rows.Scan(&a.ID, &inicio, &fin, &a.Estado, &a.ProfesionalID, &a.ServicioID, &a.DuracionMinutos); err != nil {
			return nil, fmt.Errorf("leer cita: %w", err)
		}
		a.Inicio = inicio.UTC().Format(time.RFC3339)
		a.Fin = fin.UTC().Format(time.RFC3339)
		out = append(out, a)
	}
	return out, rows.Err()
}

var patientStore PatientStore

// --- Handlers ---

func listPacientes(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, patientStore == nil, "almacén de pacientes no disponible")
	if !ok {
		return
	}
	pacientes, err := patientStore.ListActiveByClinicaIdentificador(c.Request.Context(), clinica)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudieron listar pacientes"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"clinicaIdentificador": clinica,
		"pacientes":            pacientes,
	})
}

type createPacienteRequest struct {
	Identificacion  string         `json:"identificacion"`
	Nombre          string         `json:"nombre"`
	FechaNacimiento string         `json:"fechaNacimiento"`
	Contactos       []ContactInput `json:"contactos"`
}

func (r *createPacienteRequest) validate() []string {
	var errs []string
	if strings.TrimSpace(r.Identificacion) == "" {
		errs = append(errs, "identificacion es obligatoria")
	}
	if strings.TrimSpace(r.Nombre) == "" {
		errs = append(errs, "nombre es obligatorio")
	}
	if !fechaRe.MatchString(r.FechaNacimiento) {
		errs = append(errs, "fechaNacimiento debe ser YYYY-MM-DD")
	}
	for i, ct := range r.Contactos {
		if ct.TipoCodigo != "telefono" && ct.TipoCodigo != "email" && ct.TipoCodigo != "direccion" {
			errs = append(errs, fmt.Sprintf("contactos[%d].tipoCodigo inválido", i))
		}
		if strings.TrimSpace(ct.Valor) == "" {
			errs = append(errs, fmt.Sprintf("contactos[%d].valor es obligatorio", i))
		}
	}
	return errs
}

func createPaciente(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, patientStore == nil, "almacén de pacientes no disponible")
	if !ok {
		return
	}
	var req createPacienteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cuerpo JSON inválido", "detalles": []string{err.Error()}})
		return
	}
	if errs := req.validate(); len(errs) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida", "detalles": errs})
		return
	}
	p, err := patientStore.CreatePaciente(c.Request.Context(), clinica,
		strings.TrimSpace(req.Identificacion), strings.TrimSpace(req.Nombre), req.FechaNacimiento, req.Contactos)
	switch {
	case errors.Is(err, errPacienteDuplicado):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, errPacienteDatosInvalidos), errors.Is(err, errContactoTipoInvalido), errors.Is(err, errContactoDatosInvalidos):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, errContactoPrincipalDup):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo crear el paciente"})
	default:
		c.JSON(http.StatusCreated, gin.H{"clinicaIdentificador": clinica, "paciente": p})
	}
}

func getPaciente(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, patientStore == nil, "almacén de pacientes no disponible")
	if !ok {
		return
	}
	p, err := patientStore.GetPaciente(c.Request.Context(), clinica, c.Param("id"))
	switch {
	case errors.Is(err, errPacienteFueraDeClinica):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo obtener el paciente"})
	default:
		c.JSON(http.StatusOK, gin.H{"clinicaIdentificador": clinica, "paciente": p})
	}
}

func updatePaciente(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, patientStore == nil, "almacén de pacientes no disponible")
	if !ok {
		return
	}
	var raw map[string]any
	if err := c.ShouldBindJSON(&raw); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cuerpo JSON inválido", "detalles": []string{err.Error()}})
		return
	}
	var patch PatientPatch
	var errs []string
	if v, ok := raw["identificacion"]; ok {
		s, ok := v.(string)
		if !ok || strings.TrimSpace(s) == "" {
			errs = append(errs, "identificacion inválida")
		} else {
			s = strings.TrimSpace(s)
			patch.Identificacion = &s
		}
	}
	if v, ok := raw["nombre"]; ok {
		s, ok := v.(string)
		if !ok || strings.TrimSpace(s) == "" {
			errs = append(errs, "nombre inválido")
		} else {
			s = strings.TrimSpace(s)
			patch.Nombre = &s
		}
	}
	if v, ok := raw["fechaNacimiento"]; ok {
		s, ok := v.(string)
		if !ok || !fechaRe.MatchString(s) {
			errs = append(errs, "fechaNacimiento debe ser YYYY-MM-DD")
		} else {
			patch.FechaNacimiento = &s
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
	if patch.Identificacion == nil && patch.Nombre == nil && patch.FechaNacimiento == nil && patch.Activo == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida", "detalles": []string{"debe indicar al menos un campo"}})
		return
	}
	p, err := patientStore.UpdatePaciente(c.Request.Context(), clinica, c.Param("id"), patch)
	switch {
	case errors.Is(err, errPacienteFueraDeClinica):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, errPacienteDuplicado):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, errPacienteDatosInvalidos):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo actualizar el paciente"})
	default:
		c.JSON(http.StatusOK, gin.H{"clinicaIdentificador": clinica, "paciente": p})
	}
}

func createContacto(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, patientStore == nil, "almacén de pacientes no disponible")
	if !ok {
		return
	}
	var in ContactInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cuerpo JSON inválido", "detalles": []string{err.Error()}})
		return
	}
	var errs []string
	if in.TipoCodigo != "telefono" && in.TipoCodigo != "email" && in.TipoCodigo != "direccion" {
		errs = append(errs, "tipoCodigo inválido")
	}
	if strings.TrimSpace(in.Valor) == "" {
		errs = append(errs, "valor es obligatorio")
	}
	if len(errs) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida", "detalles": errs})
		return
	}
	in.Valor = strings.TrimSpace(in.Valor)
	ct, err := patientStore.CreateContacto(c.Request.Context(), clinica, c.Param("id"), in)
	switch {
	case errors.Is(err, errPacienteFueraDeClinica):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, errContactoTipoInvalido), errors.Is(err, errContactoDatosInvalidos):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, errContactoPrincipalDup):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo crear el contacto"})
	default:
		c.JSON(http.StatusCreated, gin.H{"clinicaIdentificador": clinica, "contacto": ct})
	}
}

func updateContacto(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, patientStore == nil, "almacén de pacientes no disponible")
	if !ok {
		return
	}
	var raw map[string]any
	if err := c.ShouldBindJSON(&raw); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cuerpo JSON inválido", "detalles": []string{err.Error()}})
		return
	}
	var patch ContactPatch
	var errs []string
	if v, ok := raw["valor"]; ok {
		s, ok := v.(string)
		if !ok || strings.TrimSpace(s) == "" {
			errs = append(errs, "valor inválido")
		} else {
			s = strings.TrimSpace(s)
			patch.Valor = &s
		}
	}
	if v, ok := raw["esPrincipal"]; ok {
		b, ok := v.(bool)
		if !ok {
			errs = append(errs, "esPrincipal debe ser boolean")
		} else {
			patch.EsPrincipal = &b
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
	if patch.Valor == nil && patch.EsPrincipal == nil && patch.Activo == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida", "detalles": []string{"debe indicar al menos un campo"}})
		return
	}
	ct, err := patientStore.UpdateContacto(c.Request.Context(), clinica, c.Param("id"), patch)
	switch {
	case errors.Is(err, errContactoFueraDeClinica):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, errContactoDatosInvalidos):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, errContactoPrincipalDup):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo actualizar el contacto"})
	default:
		c.JSON(http.StatusOK, gin.H{"clinicaIdentificador": clinica, "contacto": ct})
	}
}

func listPacienteCitas(c *gin.Context) {
	clinica, ok := clinicaFromContext(c, patientStore == nil, "almacén de pacientes no disponible")
	if !ok {
		return
	}
	citas, err := patientStore.ListCitasByPaciente(c.Request.Context(), clinica, c.Param("id"))
	switch {
	case errors.Is(err, errPacienteFueraDeClinica):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo listar el historial"})
	default:
		c.JSON(http.StatusOK, gin.H{
			"clinicaIdentificador": clinica,
			"pacienteId":           c.Param("id"),
			"citas":                citas,
		})
	}
}
