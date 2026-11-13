package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"medicitas/api/internal/rlsctx"
)

// execSimple corre una orden de sesión (SET ROLE, RESET ROLE). pgx prepara
// las sentencias del protocolo extendido y PostgreSQL no admite SET ahí.
func execSimple(ctx context.Context, conn *sql.Conn, query string) error {
	return conn.Raw(func(driverConn any) error {
		stdConn, ok := driverConn.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("conexión de base de datos inesperada")
		}
		_, err := stdConn.Conn().Exec(ctx, query, pgx.QueryExecModeSimpleProtocol)
		return err
	})
}

// dbq elige la conexión de la petición (variables RLS ya fijadas) o el pool.
func dbq(ctx context.Context, database *sql.DB) rlsctx.Queryer {
	return rlsctx.Pick(ctx, database)
}

// beginDB abre la transacción en la conexión de la petición cuando existe,
// para que set_config de sesión siga visible dentro del BEGIN.
func beginDB(ctx context.Context, database *sql.DB) (*sql.Tx, error) {
	if conn := rlsctx.Conn(ctx); conn != nil {
		return conn.BeginTx(ctx, nil)
	}
	return database.BeginTx(ctx, nil)
}

// bindRLS fija app.idp_sub en una conexión dedicada y la deja en el contexto.
// El resto de la petición (lookup del usuario, stores, auditoría) usa esa
// misma conexión. Al terminar se limpian las variables antes de devolverla
// al pool.
//
// set_config(..., false) es de sesión, no SET LOCAL. Cada store hace COMMIT
// de su propio hecho (y después invalida Redis). Un SET LOCAL moriría en ese
// COMMIT y la siguiente sentencia de la misma petición quedaría sin clínica.
func bindRLS(c *gin.Context, idpSub string) (func(), error) {
	if db == nil {
		return func() {}, nil
	}
	ctx := c.Request.Context()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `
SELECT set_config('app.idp_sub', $1, false),
       set_config('app.clinica_id', '', false),
       set_config('app.rol', '', false)`, idpSub); err != nil {
		conn.Close()
		return nil, err
	}
	c.Request = c.Request.WithContext(rlsctx.WithConn(ctx, conn))
	return func() {
		resetCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// RESET ROLE antes de limpiar GUCs: la conexión vuelve a medicitas_app.
		if err := execSimple(resetCtx, conn, `RESET ROLE`); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			conn.Close()
			return
		}
		if _, err := conn.ExecContext(resetCtx, `
SELECT set_config('app.idp_sub', '', false),
       set_config('app.clinica_id', '', false),
       set_config('app.rol', '', false)`); err != nil {
			// No devolver al pool una conexión que todavía lleva la clínica anterior.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		conn.Close()
	}, nil
}

// sqlRoleForCodigo traduce el código de "Rol" al perfil NOLOGIN de 04_db_roles_matrix.
// El nombre SQL sale de esta tabla, nunca del request.
func sqlRoleForCodigo(codigo string) (string, bool) {
	switch codigo {
	case "recepcion":
		return "medicitas_recepcion", true
	case "profesional":
		return "medicitas_profesional", true
	case "adm_clinica":
		return "medicitas_adm_clinica", true
	case "adm_plataforma":
		return "medicitas_adm_plataforma", true
	default:
		return "", false
	}
}

// applyTenantGUC publica la clínica y el rol de dominio en la misma conexión
// y baja el privilegio con SET ROLE. adm_plataforma deja app.clinica_id vacío:
// la política lo deja ver todo lo que su perfil SQL puede leer.
func applyTenantGUC(ctx context.Context, du DomainUser) error {
	conn := rlsctx.Conn(ctx)
	if conn == nil {
		return nil
	}
	clinica := ""
	if du.ClinicaID != nil {
		clinica = *du.ClinicaID
	}
	if _, err := conn.ExecContext(ctx, `
SELECT set_config('app.clinica_id', $1, false),
       set_config('app.rol', $2, false)`, clinica, du.RolCodigo); err != nil {
		return err
	}
	sqlRole, ok := sqlRoleForCodigo(du.RolCodigo)
	if !ok {
		return fmt.Errorf("rol de dominio sin perfil SQL: %s", du.RolCodigo)
	}
	// sqlRole es una constante del mapa, no input del cliente.
	return execSimple(ctx, conn, "SET ROLE "+sqlRole)
}

func abortRLS(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "no se pudo fijar la sesión de la clínica"})
}
