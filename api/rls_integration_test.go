//go:build integration

package main

// Compara las dos estrategias de tenancy sobre la misma consulta:
// la columna filtrada a mano (superusuario, sin RLS efectivo) y RLS
// (medicitas_app, GUC de la clínica). También comprueba que la API,
// ya entrando como medicitas_app, sigue resolviendo al usuario y no
// entrega la otra clínica aunque el query pida clinicaId=sur.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIntegrationRLS(t *testing.T) {
	ctx := context.Background()
	super := agendaVersionDB(t)
	t.Cleanup(func() { super.Close() })
	app := openAppDB(t)
	t.Cleanup(func() { app.Close() })

	norte := newTestUUID()
	sur := newTestUUID()
	suf := time.Now().UnixNano()
	mustExec(ctx, t, super, `
INSERT INTO "Clinica" ("id", "identificador", "nombre")
VALUES ($1::uuid, $2, 'RLS norte'), ($3::uuid, $4, 'RLS sur')`,
		norte, fmt.Sprintf("rls-norte-%d", suf), sur, fmt.Sprintf("rls-sur-%d", suf))
	mustExec(ctx, t, super, `
INSERT INTO "Paciente" ("clinicaId", "identificacion", "nombre", "fechaNacimiento")
VALUES ($1::uuid, $2, 'Paciente norte', '1990-01-01'),
       ($3::uuid, $4, 'Paciente sur', '1991-02-02')`,
		norte, fmt.Sprintf("rn-%d", suf), sur, fmt.Sprintf("rs-%d", suf))
	t.Cleanup(func() {
		_, _ = super.Exec(`DELETE FROM "Paciente" WHERE "clinicaId" IN ($1::uuid, $2::uuid)`, norte, sur)
		_, _ = super.Exec(`DELETE FROM "Clinica" WHERE "id" IN ($1::uuid, $2::uuid)`, norte, sur)
	})

	const misma = `SELECT count(*) FROM "Paciente" WHERE "clinicaId" = $1::uuid`

	if n := scanAs(t, ctx, app, "recepcion", norte, misma, sur); n != 0 {
		t.Fatalf("RLS dejó ver %d pacientes de sur con la clínica norte", n)
	}
	if n := scanAs(t, ctx, super, "", "", misma, sur); n != 1 {
		t.Fatalf("superusuario (sin RLS efectivo) = %d, want 1", n)
	}
	if n := scanAs(t, ctx, app, "recepcion", norte, misma, norte); n != 1 {
		t.Fatalf("RLS norte = %d, want 1", n)
	}
	if n := scanAs(t, ctx, app, "", "", `SELECT count(*) FROM "Paciente"`); n != 0 {
		t.Fatalf("sin GUC se vieron %d pacientes; RLS debe cerrar en vacío", n)
	}
	if n := scanAs(t, ctx, app, "adm_plataforma", "", `SELECT count(DISTINCT "clinicaId") FROM "Paciente"`); n < 2 {
		t.Fatalf("adm_plataforma vio %d clínicas, want al menos 2", n)
	}

	tx, err := app.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
SELECT set_config('app.rol', 'recepcion', true),
       set_config('app.clinica_id', $1, true)`, norte); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO "Paciente" ("clinicaId", "identificacion", "nombre", "fechaNacimiento")
VALUES ($1::uuid, $2, 'Propio', '1992-03-03')`, norte, fmt.Sprintf("propio-%d", suf)); err != nil {
		t.Fatalf("INSERT en la clínica propia: %v", err)
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO "Paciente" ("clinicaId", "identificacion", "nombre", "fechaNacimiento")
VALUES ($1::uuid, $2, 'Fuga', '1992-03-03')`, sur, fmt.Sprintf("fuga-%d", suf))
	_ = tx.Rollback()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("INSERT en otra clínica: %v, want 42501", err)
	}

	rlsDur := timeSameCount(t, app, misma, norte, "recepcion", norte)
	superDur := timeSameCount(t, super, misma, norte, "", "")
	t.Logf("RLS_MEDICION rls=%s superusuario=%s", rlsDur, superDur)

	apiBase := envOr("API_BASE", "http://localhost:8080")
	tokenURL := envOr("KC_TOKEN_URL", "http://localhost:6767/realms/medicitas/protocol/openid-connect/token")
	client := &http.Client{Timeout: 10 * time.Second}
	waitReady(t, client, apiBase+"/ready")
	tok := fetchToken(t, client, tokenURL, envOr("SMOKE_USER", "recep-norte"), envOr("SMOKE_PASS", "recep123"))
	me := getJSON(t, client, apiBase+"/me", tok)
	if me["clinicaIdentificador"] != "clinica-norte" {
		t.Fatalf("/me clinica=%v", me["clinicaIdentificador"])
	}
	pac := getJSON(t, client, apiBase+"/pacientes?clinicaId=clinica-sur", tok)
	if pac["clinicaIdentificador"] != "clinica-norte" {
		t.Fatalf("I5 clinica=%v", pac["clinicaIdentificador"])
	}
	lista, ok := pac["pacientes"].([]any)
	if !ok || len(lista) == 0 {
		t.Fatalf("la API no vio pacientes de su clínica: %v", pac)
	}
}

func openAppDB(t *testing.T) *sql.DB {
	t.Helper()
	host := envOr("DB_HOST", "localhost")
	port := envOr("DB_PORT", "5432")
	user := envOr("APP_DB_USER", "medicitas_app")
	pass := envOr("APP_DB_PASSWORD", "medicitas_app")
	name := envOr("DB_NAME", envOr("POSTGRES_DB", "medicitas"))
	conn, err := sql.Open("pgx", postgresDSN(user, pass, host, port, name))
	if err != nil {
		t.Fatalf("abrir medicitas_app: %v", err)
	}
	if err := conn.Ping(); err != nil {
		conn.Close()
		t.Fatalf("ping medicitas_app (¿migración 03_rls?): %v", err)
	}
	return conn
}

// scanAs ejecuta la consulta dentro de una transacción con el GUC local.
// rol y clinica vacíos dejan la sesión sin tenant (fallo cerrado).
func scanAs(t *testing.T, ctx context.Context, db *sql.DB, rol, clinica, query string, args ...any) int {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
SELECT set_config('app.rol', $1, true),
       set_config('app.clinica_id', $2, true)`, rol, clinica); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func timeSameCount(t *testing.T, db *sql.DB, query, clinicaID, rol, gucClinica string) time.Duration {
	t.Helper()
	ctx := context.Background()
	start := time.Now()
	for i := 0; i < 100; i++ {
		if n := scanAs(t, ctx, db, rol, gucClinica, query, clinicaID); n != 1 {
			t.Fatalf("medición: count=%d want 1", n)
		}
	}
	return time.Since(start)
}
