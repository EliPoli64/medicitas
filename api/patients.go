package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Patient es la proyección de lectura de "Paciente" para la API.
type Patient struct {
	ID               string `json:"id"`
	Identificacion   string `json:"identificacion"`
	Nombre           string `json:"nombre"`
	FechaNacimiento  string `json:"fechaNacimiento"`
	Activo           bool   `json:"activo"`
}

// PatientStore abstrae el listado para tests sin Postgres.
type PatientStore interface {
	ListActiveByClinicaIdentificador(ctx context.Context, clinicaIdentificador string) ([]Patient, error)
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
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterar pacientes: %w", err)
	}
	return out, nil
}

var patientStore PatientStore

func listPacientes(c *gin.Context) {
	du, ok := domainUserFromContext(c)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "usuario de dominio no disponible"})
		return
	}
	if du.ClinicaIdentificador == nil || *du.ClinicaIdentificador == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "usuario sin clínica"})
		return
	}
	if patientStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "almacén de pacientes no disponible"})
		return
	}

	// Tenant solo desde dominio: query/body (p. ej. clinicaId) se ignoran a propósito.
	clinica := *du.ClinicaIdentificador
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
