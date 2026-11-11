package main

// RF1: dar de alta una clínica con su nombre, su identificador y su zona
// horaria, creando junto con ella su primer usuario administrador.
//
// Flujo (decisión defendible en la defensa):
//
//  1. Validar entrada (400).
//  2. Crear la identidad en Keycloak vía Admin API (rol adm-clinica, atributo
//     clinica_id = identificador). El sub resultante es el "Usuario"."idpSub".
//  3. En UNA transacción de Postgres: INSERT "Clinica" + INSERT "Usuario"
//     (rol adm_clinica). Ambos o ninguno.
//  4. Si la BD falla, se compensa eliminando la identidad recién creada en
//     Keycloak (saga con compensación; no hay XA entre servicios).
//
// El orden IdP → BD es obligado: "Usuario"."idpSub" es NOT NULL y UNIQUE, no
// se puede reservar la fila antes de tener el sub. La compensación invierte
// el paso 2; una caída entre 2 y 3 deja una identidad huérfana en Keycloak
// que no alcanza ningún dato (I5 no se compromete: sin fila "Usuario" no hay
// tenant), y queda registrada en logs para limpieza.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata" // IANA valida en contenedores sin zoneinfo del sistema

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
)

// Clinic es la proyección de lectura de "Clinica".
type Clinic struct {
	ID            string `json:"id"`
	Identificador string `json:"identificador"`
	Nombre        string `json:"nombre"`
	ZonaHoraria   string `json:"zonaHoraria"`
	Activo        bool   `json:"activo"`
	CreadoEn      string `json:"creadoEn"`
}

// ClinicAdmin es el usuario administrador creado con la clínica.
type ClinicAdmin struct {
	UsuarioID string `json:"usuarioId"`
	IdpSub    string `json:"idpSub"`
	Username  string `json:"username"`
	RolCodigo string `json:"rolCodigo"`
}

// NewClinic son los datos de alta de la clínica (ya validados).
type NewClinic struct {
	Identificador string
	Nombre        string
	ZonaHoraria   string
}

// ClinicStore abstrae el alta transaccional para tests sin Postgres.
type ClinicStore interface {
	CreateClinicaConAdmin(ctx context.Context, c NewClinic, adminIDPSub, adminUsername string) (Clinic, ClinicAdmin, error)
}

type pgClinicStore struct {
	db *sql.DB
}

func newPGClinicStore(database *sql.DB) *pgClinicStore {
	return &pgClinicStore{db: database}
}

var (
	errClinicaExists = errors.New("el identificador de la clínica ya está registrado")
	errUsuarioExists = errors.New("el usuario ya está registrado")
)

// CreateClinicaConAdmin inserta la clínica y su primer administrador en una
// sola transacción (RF1: "creando junto con ella"). La clínica del usuario se
// ancla con la FK compuesta (clinicaId, id) y el trigger validarUsuarioClinica
// exige clinicaId para todo rol que no sea adm_plataforma.
func (s *pgClinicStore) CreateClinicaConAdmin(ctx context.Context, c NewClinic, adminIDPSub, adminUsername string) (Clinic, ClinicAdmin, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Clinic{}, ClinicAdmin{}, fmt.Errorf("abrir transacción: %w", err)
	}
	defer tx.Rollback() // no-op si ya se hizo Commit

	var rolID string
	err = tx.QueryRowContext(ctx,
		`SELECT "id"::text FROM "Rol" WHERE "codigo" = 'adm_clinica'`).Scan(&rolID)
	if errors.Is(err, sql.ErrNoRows) {
		return Clinic{}, ClinicAdmin{}, fmt.Errorf("falta el rol adm_clinica en el catálogo base")
	}
	if err != nil {
		return Clinic{}, ClinicAdmin{}, fmt.Errorf("consultar rol: %w", err)
	}

	var clinic Clinic
	err = tx.QueryRowContext(ctx, `
INSERT INTO "Clinica" ("identificador", "nombre", "zonaHoraria")
VALUES ($1, $2, $3)
RETURNING "id"::text, "identificador", "nombre", "zonaHoraria", "activo", "creadoEn"::text`,
		c.Identificador, c.Nombre, c.ZonaHoraria,
	).Scan(&clinic.ID, &clinic.Identificador, &clinic.Nombre,
		&clinic.ZonaHoraria, &clinic.Activo, &clinic.CreadoEn)
	if err != nil {
		if isUniqueViolation(err, "ClinicaIdentificadorUq") {
			return Clinic{}, ClinicAdmin{}, errClinicaExists
		}
		return Clinic{}, ClinicAdmin{}, fmt.Errorf("insertar clínica: %w", err)
	}

	var admin ClinicAdmin
	admin.Username = adminUsername
	admin.RolCodigo = "adm_clinica"
	err = tx.QueryRowContext(ctx, `
INSERT INTO "Usuario" ("idpSub", "clinicaId", "rolId")
VALUES ($1, (SELECT "id" FROM "Clinica" WHERE "identificador" = $2), $3)
RETURNING "id"::text, "idpSub"`,
		adminIDPSub, c.Identificador, rolID,
	).Scan(&admin.UsuarioID, &admin.IdpSub)
	if err != nil {
		if isUniqueViolation(err, "UsuarioIdpSubUq") {
			return Clinic{}, ClinicAdmin{}, errUsuarioExists
		}
		return Clinic{}, ClinicAdmin{}, fmt.Errorf("insertar usuario administrador: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Clinic{}, ClinicAdmin{}, fmt.Errorf("confirmar transacción: %w", err)
	}
	return clinic, admin, nil
}

// isUniqueViolation reconoce la violación de la restricción nombrada.
func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

// isForeignKeyViolation / isNotNullViolation / isCheckViolation reconocen las
// violaciones de integridad que la API traduce a 400/409 en vez de 500.
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

func isNotNullViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23502"
}

func isCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

// isExclusionViolation reconoce EXCLUDE (p. ej. HorarioAtencionSinSolape) → SQLSTATE 23P01.
func isExclusionViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23P01"
}

// isRaiseException reconoce los RAISE EXCEPTION de los triggers de negocio
// (p. ej. validarRolProfesional), que Postgres reporta con SQLSTATE P0001.
func isRaiseException(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "P0001"
}

var (
	// Misma regla que el CHECK "ClinicaIdentificadorFormato".
	identificadorRe = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	// Lo mínimo para que el username sea usable como login de Keycloak.
	usernameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{3,64}$`)
)

// createClinicaRequest es la entrada de POST /clinicas.
type createClinicaRequest struct {
	Nombre        string `json:"nombre"`
	Identificador string `json:"identificador"`
	ZonaHoraria   string `json:"zonaHoraria"`
	Administrador struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Email    string `json:"email"`
		Nombre   string `json:"nombre"`
		Apellido string `json:"apellido"`
	} `json:"administrador"`
}

// validate devuelve los errores de forma agregada (respuesta 400).
func (r *createClinicaRequest) validate() []string {
	var errs []string
	if strings.TrimSpace(r.Nombre) == "" {
		errs = append(errs, "nombre es obligatorio")
	}
	if !identificadorRe.MatchString(r.Identificador) {
		errs = append(errs, "identificador debe ser minúsculas y números separados por guiones (p. ej. clinica-este)")
	}
	zona := r.ZonaHoraria
	if zona == "" {
		zona = "America/Costa_Rica" // default del esquema
	} else if _, err := time.LoadLocation(zona); err != nil {
		errs = append(errs, "zonaHoraria no es una zona IANA válida (p. ej. America/Costa_Rica)")
	}
	if !usernameRe.MatchString(r.Administrador.Username) {
		errs = append(errs, "administrador.username es obligatorio (3-64 caracteres: letras, dígitos, . _ -)")
	}
	if len(r.Administrador.Password) < 8 {
		errs = append(errs, "administrador.password debe tener al menos 8 caracteres")
	}
	if r.Administrador.Email != "" && !strings.Contains(r.Administrador.Email, "@") {
		errs = append(errs, "administrador.email no es un correo válido")
	}
	return errs
}

// clinicStore e identityAdmin son globales inyectables en tests.
var (
	clinicStore   ClinicStore
	identityAdmin IdentityAdmin
)

// createClinica maneja POST /clinicas (RF1).
// Matriz: solo adm-plataforma. La clínica nueva NUNCA se toma del body del
// cliente como tenant de la sesión: es el recurso que se está creando.
func createClinica(c *gin.Context) {
	if clinicStore == nil || identityAdmin == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "servicio de altas no disponible"})
		return
	}

	var req createClinicaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cuerpo JSON inválido", "detalles": []string{err.Error()}})
		return
	}
	if errs := req.validate(); len(errs) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "solicitud inválida", "detalles": errs})
		return
	}
	if req.ZonaHoraria == "" {
		req.ZonaHoraria = "America/Costa_Rica"
	}
	if req.Administrador.Email == "" {
		req.Administrador.Email = req.Administrador.Username + "@medicitas.local"
	}
	if req.Administrador.Nombre == "" {
		req.Administrador.Nombre = "Administración"
	}
	if req.Administrador.Apellido == "" {
		req.Administrador.Apellido = req.Identificador
	}

	// 1) Identidad primero: idpSub NOT NULL exige el sub de Keycloak.
	sub, err := identityAdmin.CreateUser(c.Request.Context(), NewIDPUser{
		Username:             req.Administrador.Username,
		Password:             req.Administrador.Password,
		Email:                req.Administrador.Email,
		FirstName:            req.Administrador.Nombre,
		LastName:             req.Administrador.Apellido,
		ClinicaIdentificador: req.Identificador,
		RealmRole:            "adm-clinica",
	})
	if errors.Is(err, errIDPUserExists) {
		c.JSON(http.StatusConflict, gin.H{"error": "el nombre de usuario ya existe en el proveedor de identidad"})
		return
	}
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "no se pudo crear la identidad en el proveedor"})
		return
	}

	// 2) Dominio: clínica + administrador en una transacción.
	clinic, admin, err := clinicStore.CreateClinicaConAdmin(c.Request.Context(), NewClinic{
		Identificador: req.Identificador,
		Nombre:        req.Nombre,
		ZonaHoraria:   req.ZonaHoraria,
	}, sub, req.Administrador.Username)

	// 3) Compensación: si la BD no confirmó, la identidad no debe quedar.
	if err != nil {
		if derr := identityAdmin.DeleteUser(c.Request.Context(), sub); derr != nil {
			// No se enmascara el error original; la huérfana queda en logs.
			log.Printf("rf1: compensación fallida al eliminar identidad %s: %v", sub, derr)
		}
		switch {
		case errors.Is(err, errClinicaExists):
			c.JSON(http.StatusConflict, gin.H{"error": errClinicaExists.Error()})
		case errors.Is(err, errUsuarioExists):
			c.JSON(http.StatusConflict, gin.H{"error": errUsuarioExists.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no se pudo registrar la clínica"})
		}
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"clinica":       clinic,
		"administrador": admin,
	})
}
