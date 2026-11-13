//go:build integration

package main

// Integración RF7/RF8 contra la pila real (docker compose up +
// go test -tags=integration ./...).
//
// Crea su propia clínica (RF1), catálogo (RF2–RF4) y agenda (RF5/RF6) por la
// API, y luego comprueba el cálculo de espacios:
//
//   * el espacio ofrecido es reservable según el motor: una cita INSERTADA vía
//     SQL en el primer espacio pasa los triggers validarCitaEnHorario y
//     prepararCita (el cálculo Go es un espejo del trigger);
//   * la cita ocupada desaparece de la consulta siguiente (I7);
//   * un bloqueo retira espacios hacia adelante (RF6);
//   * RF8 agrupa por fecha solo donde hay espacio;
//   * matriz (adm, recepción, profesional pueden; adm-plataforma no) e I5
//     (recursos de otra clínica → 404, nunca datos).

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestIntegrationRF7RF8Disponibilidad(t *testing.T) {
	apiBase := envOr("API_BASE", "http://localhost:8080")
	tokenURL := envOr("KC_TOKEN_URL", "http://localhost:6767/realms/medicitas/protocol/openid-connect/token")
	client := &http.Client{Timeout: 15 * time.Second}

	waitReady(t, client, apiBase+"/ready")

	loc, err := time.LoadLocation("America/Costa_Rica")
	if err != nil {
		t.Fatal(err)
	}
	// Próximo lunes (al menos mañana) en la zona de la clínica.
	lunes := time.Now().In(loc).AddDate(0, 0, 1)
	for isoDow(lunes.Weekday()) != 1 {
		lunes = lunes.AddDate(0, 0, 1)
	}
	lunesStr := lunes.Format("2006-01-02")

	// --- Fixtures por API (RF1–RF4, RF5, RF14) ---
	suffix := fmt.Sprintf("%d", time.Now().Unix()%1000000)
	ident := "clinica-dispo-" + suffix
	admUser := "adm-dispo-" + suffix
	platTok := fetchToken(t, client, tokenURL, "plat-admin", "plat123")

	altaBody := fmt.Sprintf(`{
		"nombre": "Clínica Dispo",
		"identificador": %q,
		"administrador": {"username": %q, "password": "dispo1234"}
	}`, ident, admUser)
	if code, raw := postJSON(t, client, apiBase+"/clinicas", platTok, altaBody); code != http.StatusCreated {
		t.Fatalf("alta clínica = %d body=%s", code, raw)
	}
	admTok := fetchToken(t, client, tokenURL, admUser, "dispo1234")

	// Especialidad y servicio de 30 minutos (la duración la fija el servicio: I4).
	var esp struct {
		Especialidades []Especialidad `json:"especialidades"`
	}
	code, raw := getRaw(t, client, apiBase+"/especialidades", admTok)
	if code != http.StatusOK {
		t.Fatalf("especialidades = %d body=%s", code, raw)
	}
	if err := json.Unmarshal([]byte(raw), &esp); err != nil || len(esp.Especialidades) == 0 {
		t.Fatalf("especialidades = %s err=%v", raw, err)
	}
	espID := esp.Especialidades[0].ID

	svcBody := fmt.Sprintf(`{"nombre":"Consulta dispo %s","duracionMinutos":30,"especialidadId":%q}`, suffix, espID)
	code, raw = postJSON(t, client, apiBase+"/servicios", admTok, svcBody)
	if code != http.StatusCreated {
		t.Fatalf("alta servicio = %d body=%s", code, raw)
	}
	var svcOut struct {
		Servicio Service `json:"servicio"`
	}
	if err := json.Unmarshal([]byte(raw), &svcOut); err != nil {
		t.Fatal(err)
	}
	svcID := svcOut.Servicio.ID

	// Profesional habilitado para el servicio.
	drUser := "dr-dispo-" + suffix
	uBody := fmt.Sprintf(`{"username":%q,"password":"dispo1234","rol":"profesional"}`, drUser)
	code, raw = postJSON(t, client, apiBase+"/usuarios", admTok, uBody)
	if code != http.StatusCreated {
		t.Fatalf("alta usuario profesional = %d body=%s", code, raw)
	}
	var uOut struct {
		Usuario AdminUser `json:"usuario"`
	}
	if err := json.Unmarshal([]byte(raw), &uOut); err != nil {
		t.Fatal(err)
	}
	code, raw = postJSON(t, client, apiBase+"/profesionales", admTok,
		fmt.Sprintf(`{"usuarioId":%q,"especialidadId":%q}`, uOut.Usuario.ID, espID))
	if code != http.StatusCreated {
		t.Fatalf("alta profesional = %d body=%s", code, raw)
	}
	var profOut struct {
		Profesional Professional `json:"profesional"`
	}
	if err := json.Unmarshal([]byte(raw), &profOut); err != nil {
		t.Fatal(err)
	}
	profID := profOut.Profesional.ID

	if code, raw = doJSONRaw(t, client, http.MethodPut,
		apiBase+"/profesionales/"+profID+"/servicios", admTok,
		fmt.Sprintf(`{"servicioIds":[%q]}`, svcID)); code != http.StatusOK {
		t.Fatalf("habilitar servicio = %d body=%s", code, raw)
	}

	// Agenda del lunes en jornada partida (RF5): 08:00-12:00 y 13:00-17:00.
	for _, bloque := range [][2]string{{"08:00", "12:00"}, {"13:00", "17:00"}} {
		hBody := fmt.Sprintf(`{"diaSemana":1,"horaInicio":%q,"horaFin":%q,"vigenteDesde":%q}`,
			bloque[0], bloque[1], lunesStr)
		if code, raw = postJSON(t, client, apiBase+"/profesionales/"+profID+"/horarios", admTok, hBody); code != http.StatusCreated {
			t.Fatalf("horario %v = %d body=%s", bloque, code, raw)
		}
	}

	// Paciente para la cita (RF14) y usuario de recepción para la matriz.
	pacBody := fmt.Sprintf(`{"identificacion":"DISPO-%s","nombre":"Paciente Dispo","fechaNacimiento":"1990-01-01"}`, suffix)
	code, raw = postJSON(t, client, apiBase+"/pacientes", admTok, pacBody)
	if code != http.StatusCreated {
		t.Fatalf("alta paciente = %d body=%s", code, raw)
	}
	var pacOut struct {
		Paciente struct {
			ID string `json:"id"`
		} `json:"paciente"`
	}
	if err := json.Unmarshal([]byte(raw), &pacOut); err != nil {
		t.Fatal(err)
	}
	recepUser := "recep-dispo-" + suffix
	if code, raw = postJSON(t, client, apiBase+"/usuarios", admTok,
		fmt.Sprintf(`{"username":%q,"password":"dispo1234","rol":"recepcion"}`, recepUser)); code != http.StatusCreated {
		t.Fatalf("alta recepción = %d body=%s", code, raw)
	}

	// --- RF7: línea base (16 espacios en dos bloques) ---
	rf7URL := fmt.Sprintf("%s/profesionales/%s/disponibilidad?fecha=%s&servicioId=%s",
		apiBase, profID, lunesStr, svcID)
	code, raw = getRaw(t, client, rf7URL, admTok)
	if code != http.StatusOK {
		t.Fatalf("RF7 = %d body=%s", code, raw)
	}
	espacios := espaciosHoraLocal(t, raw)
	if len(espacios) != 16 {
		t.Fatalf("RF7 espacios = %v (esperaba 16)", espacios)
	}
	if espacios[0] != "08:00" || espacios[7] != "11:30" || espacios[8] != "13:00" || espacios[15] != "16:30" {
		t.Fatalf("RF7 espacios = %v", espacios)
	}

	// --- El primer espacio es reservable según el motor ---
	// La cita se inserta vía SQL a propósito: si el cálculo de RF7 no fuera
	// coherente con validarCitaEnHorario/prepararCita, el INSERT fallaría.
	bdb := testDB(t)
	defer bdb.Close()
	inicio08 := time.Date(lunes.Year(), lunes.Month(), lunes.Day(), 8, 0, 0, 0, loc)
	var citaID string
	err = bdb.QueryRowContext(context.Background(), `
INSERT INTO "Cita" ("clinicaId", "pacienteId", "profesionalId", "servicioId", "inicio")
VALUES ((SELECT "id" FROM "Clinica" WHERE "identificador" = $1), $2, $3, $4, $5)
RETURNING "id"::text`,
		ident, pacOut.Paciente.ID, profID, svcID, inicio08,
	).Scan(&citaID)
	if err != nil {
		t.Fatalf("la cita no cupo en el espacio calculado (trigger rechazó): %v", err)
	}

	// --- I7: el espacio reservado desaparece de inmediato ---
	code, raw = getRaw(t, client, rf7URL, admTok)
	if code != http.StatusOK {
		t.Fatalf("RF7 tras cita = %d body=%s", code, raw)
	}
	espacios = espaciosHoraLocal(t, raw)
	if len(espacios) != 15 {
		t.Fatalf("RF7 tras cita = %v (esperaba 15)", espacios)
	}
	for _, h := range espacios {
		if h == "08:00" {
			t.Fatalf("el espacio reservado sigue ofreciéndose: %v", espacios)
		}
	}

	// --- RF6: un bloqueo retira espacios hacia adelante ---
	// El lunes 09:00–10:00 en la zona de la clínica.
	inicioBloqueo := time.Date(lunes.Year(), lunes.Month(), lunes.Day(), 9, 0, 0, 0, loc)
	finBloqueo := time.Date(lunes.Year(), lunes.Month(), lunes.Day(), 10, 0, 0, 0, loc)
	bloqueoBody := fmt.Sprintf(`{"inicio":%q,"fin":%q,"motivo":"Capacitación"}`,
		inicioBloqueo.Format(time.RFC3339), finBloqueo.Format(time.RFC3339))
	if code, raw = postJSON(t, client, apiBase+"/profesionales/"+profID+"/bloqueos", admTok, bloqueoBody); code != http.StatusCreated {
		t.Fatalf("bloqueo = %d body=%s", code, raw)
	}
	code, raw = getRaw(t, client, rf7URL, admTok)
	if code != http.StatusOK {
		t.Fatalf("RF7 tras bloqueo = %d body=%s", code, raw)
	}
	espacios = espaciosHoraLocal(t, raw)
	if len(espacios) != 13 {
		t.Fatalf("RF7 tras bloqueo = %v (esperaba 13)", espacios)
	}
	for _, h := range espacios {
		if h == "09:00" || h == "09:30" {
			t.Fatalf("el bloqueo no retiró %s: %v", h, espacios)
		}
	}

	// --- RF8: disponibilidad por servicio en el rango ---
	code, raw = getRaw(t, client,
		fmt.Sprintf("%s/servicios/%s/disponibilidad?desde=%s&hasta=%s", apiBase, svcID, lunesStr, lunesStr),
		admTok)
	if code != http.StatusOK {
		t.Fatalf("RF8 = %d body=%s", code, raw)
	}
	var rf8 struct {
		Disponibilidad []struct {
			Fecha         string `json:"fecha"`
			Profesionales []struct {
				ProfesionalID string `json:"profesionalId"`
				Espacios      []struct {
					HoraLocal string `json:"horaLocal"`
				} `json:"espacios"`
			} `json:"profesionales"`
		} `json:"disponibilidad"`
	}
	if err := json.Unmarshal([]byte(raw), &rf8); err != nil {
		t.Fatal(err)
	}
	if len(rf8.Disponibilidad) != 1 || rf8.Disponibilidad[0].Fecha != lunesStr {
		t.Fatalf("RF8 fechas = %s", raw)
	}
	profs := rf8.Disponibilidad[0].Profesionales
	if len(profs) != 1 || profs[0].ProfesionalID != profID {
		t.Fatalf("RF8 profesionales = %+v", profs)
	}
	if len(profs[0].Espacios) != 13 {
		t.Fatalf("RF8 espacios = %+v (esperaba 13)", profs[0].Espacios)
	}

	// --- Matriz (RF17): adm-clinica, recepción y profesional pueden consultar ---
	recepTok := fetchToken(t, client, tokenURL, recepUser, "dispo1234")
	if code, _ := getRaw(t, client, rf7URL, recepTok); code != http.StatusOK {
		t.Fatalf("recepción consulta disponibilidad = %d, want 200", code)
	}
	drTok := fetchToken(t, client, tokenURL, drUser, "dispo1234")
	if code, _ := getRaw(t, client, rf7URL, drTok); code != http.StatusOK {
		t.Fatalf("profesional consulta disponibilidad = %d, want 200", code)
	}
	if code, _ := getRaw(t, client, rf7URL, platTok); code != http.StatusForbidden {
		t.Fatalf("adm-plataforma consulta disponibilidad = %d, want 403", code)
	}

	// --- I5: datos de otra clínica responden 404, nunca datos ---
	recepNorteTok := fetchToken(t, client, tokenURL, "recep-norte", "recep123")
	if code, _ := getRaw(t, client, rf7URL, recepNorteTok); code != http.StatusNotFound {
		t.Fatalf("profesional de otra clínica = %d, want 404", code)
	}
	ajeno := "00000000-0000-0000-0000-000000000000"
	if code, _ := getRaw(t, client,
		fmt.Sprintf("%s/profesionales/%s/disponibilidad?fecha=%s&servicioId=%s", apiBase, ajeno, lunesStr, svcID),
		admTok); code != http.StatusNotFound {
		t.Fatalf("profesional inexistente = %d, want 404", code)
	}
	if code, _ := getRaw(t, client,
		fmt.Sprintf("%s/servicios/%s/disponibilidad?desde=%s&hasta=%s", apiBase, ajeno, lunesStr, lunesStr),
		admTok); code != http.StatusNotFound {
		t.Fatalf("servicio inexistente = %d, want 404", code)
	}

	// --- Parámetros inválidos ---
	if code, _ := getRaw(t, client,
		fmt.Sprintf("%s/profesionales/%s/disponibilidad?fecha=%s", apiBase, profID, lunesStr),
		admTok); code != http.StatusBadRequest {
		t.Fatalf("RF7 sin servicioId = %d, want 400", code)
	}

	// El servicio sigue operativo.
	if code, _ := getRaw(t, client, apiBase+"/health", ""); code != http.StatusOK {
		t.Fatalf("servicio caído: %d", code)
	}
}

// espaciosHoraLocal extrae horaLocal de la respuesta de RF7.
func espaciosHoraLocal(t *testing.T, raw string) []string {
	t.Helper()
	var out struct {
		Espacios []struct {
			HoraLocal string `json:"horaLocal"`
		} `json:"espacios"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	horas := make([]string, 0, len(out.Espacios))
	for _, e := range out.Espacios {
		horas = append(horas, e.HoraLocal)
	}
	return horas
}

// testDB abre Postgres para insertar una cita y comprobar que RF7 coincide
// con los triggers del motor (el INSERT no pasa por la API a propósito).
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	host := envOr("DB_HOST", "localhost")
	port := envOr("DB_PORT", "5432")
	user := envOr("DB_USER", envOr("POSTGRES_USER", "medicitas"))
	pass := envOr("DB_PASSWORD", envOr("POSTGRES_PASSWORD", "cambiar_en_local"))
	name := envOr("DB_NAME", envOr("POSTGRES_DB", "medicitas"))
	conn, err := sql.Open("pgx", postgresDSN(user, pass, host, port, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Ping(); err != nil {
		t.Fatalf("conexión a Postgres: %v", err)
	}
	return conn
}
