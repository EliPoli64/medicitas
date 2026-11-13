package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"medicitas/api/internal/rlsctx"
)

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

// applyTenantGUC publica la clínica y el rol de dominio en la misma conexión.
// adm_plataforma deja app.clinica_id vacío: la política lo deja ver todo.
func applyTenantGUC(ctx context.Context, du DomainUser) error {
	conn := rlsctx.Conn(ctx)
	if conn == nil {
		return nil
	}
	clinica := ""
	if du.ClinicaID != nil {
		clinica = *du.ClinicaID
	}
	_, err := conn.ExecContext(ctx, `
SELECT set_config('app.clinica_id', $1, false),
       set_config('app.rol', $2, false)`, clinica, du.RolCodigo)
	return err
}

func abortRLS(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "no se pudo fijar la sesión de la clínica"})
}
