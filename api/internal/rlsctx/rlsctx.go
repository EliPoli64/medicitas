// Package rlsctx guarda, en el contexto de la petición, la conexión de
// Postgres en la que ya se fijaron app.idp_sub, app.clinica_id y app.rol.
//
// Las consultas de esa petición tienen que usar esa conexión. El pool puede
// entregar otra, y en esa otra las variables de sesión no existen: RLS
// ocultaría todas las filas o, peor, arrastraría la clínica anterior.
package rlsctx

import (
	"context"
	"database/sql"
)

// Queryer es el subconjunto de *sql.DB, *sql.Conn y *sql.Tx que usan los stores.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type ctxKey struct{}

// WithConn adjunta la conexión fijada a la petición.
func WithConn(ctx context.Context, conn *sql.Conn) context.Context {
	return context.WithValue(ctx, ctxKey{}, conn)
}

// Conn devuelve la conexión de la petición, o nil si no hay sesión RLS.
func Conn(ctx context.Context) *sql.Conn {
	c, _ := ctx.Value(ctxKey{}).(*sql.Conn)
	return c
}

// Pick usa la conexión de la petición cuando existe. Si no, el fallback
// (tests unitarios y arranque, sin sesión de tenant).
func Pick(ctx context.Context, fallback Queryer) Queryer {
	if c := Conn(ctx); c != nil {
		return c
	}
	return fallback
}
