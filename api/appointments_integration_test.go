//go:build integration

package main

// Integración RF9–RF13: reserva, carrera, I8, ciclo (reprogramar/cancelar/desenlace)
// e invariantes I2 (solape del paciente) e I3 (horario/bloqueos).
//
//	go test -tags=integration -count=1 -run 'RF9|RF10|Carrera|I8|Ciclo|I2|I3' ./...

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

// rf9Fixtures crea clínica + servicio 30m + profesional + horario lunes + 2 pacientes + recepción.
type rf9Fixtures struct {
	Ident, AdminsUser, RecepUser, DrUser string
	AdmTok, RecepTok, DrTok              string
	ProfID, SvcID                        string
	PacienteA, PacienteB                 string
	Lunes                                time.Time
	LunesStr                             string
	Loc                                  *time.Location
	TokenURL                             string
}

func setupRF9Fixtures(t *testing.T) (string, string, *http.Client, rf9Fixtures) {
	t.Helper()
	apiBase := envOr("API_BASE", "http://localhost:8080")
	tokenURL := envOr("KC_TOKEN_URL", "http://localhost:6767/realms/medicitas/protocol/openid-connect/token")
	client := &http.Client{Timeout: 20 * time.Second}
	waitReady(t, client, apiBase+"/ready")

	loc, err := time.LoadLocation("America/Costa_Rica")
	if err != nil {
		t.Fatal(err)
	}
	lunes := time.Now().In(loc).AddDate(0, 0, 1)
	for isoDow(lunes.Weekday()) != 1 {
		lunes = lunes.AddDate(0, 0, 1)
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000)
	fx := rf9Fixtures{
		Ident:      "clinica-rf9-" + suffix,
		AdminsUser: "adm-rf9-" + suffix,
		RecepUser:  "recep-rf9-" + suffix,
		Lunes:      lunes,
		LunesStr:   lunes.Format("2006-01-02"),
		Loc:        loc,
	}

	platTok := fetchToken(t, client, tokenURL, "plat-admin", "plat123")
	alta := fmt.Sprintf(`{"nombre":"Clínica RF9","identificador":%q,"administrador":{"username":%q,"password":"rf9pass12"}}`,
		fx.Ident, fx.AdminsUser)
	if code, raw := postJSON(t, client, apiBase+"/clinicas", platTok, alta); code != http.StatusCreated {
		t.Fatalf("alta clínica = %d body=%s", code, raw)
	}
	fx.AdmTok = fetchToken(t, client, tokenURL, fx.AdminsUser, "rf9pass12")

	var esp struct {
		Especialidades []Especialidad `json:"especialidades"`
	}
	code, raw := getRaw(t, client, apiBase+"/especialidades", fx.AdmTok)
	if code != http.StatusOK {
		t.Fatalf("especialidades = %d %s", code, raw)
	}
	if err := json.Unmarshal([]byte(raw), &esp); err != nil || len(esp.Especialidades) == 0 {
		t.Fatalf("especialidades: %v %s", err, raw)
	}
	espID := esp.Especialidades[0].ID

	code, raw = postJSON(t, client, apiBase+"/servicios", fx.AdmTok,
		fmt.Sprintf(`{"nombre":"Consulta RF9 %s","duracionMinutos":30,"especialidadId":%q}`, suffix, espID))
	if code != http.StatusCreated {
		t.Fatalf("servicio = %d %s", code, raw)
	}
	var svcOut struct {
		Servicio Service `json:"servicio"`
	}
	if err := json.Unmarshal([]byte(raw), &svcOut); err != nil {
		t.Fatal(err)
	}
	fx.SvcID = svcOut.Servicio.ID

	fx.DrUser = "dr-rf9-" + suffix
	code, raw = postJSON(t, client, apiBase+"/usuarios", fx.AdmTok,
		fmt.Sprintf(`{"username":%q,"password":"rf9pass12","rol":"profesional"}`, fx.DrUser))
	if code != http.StatusCreated {
		t.Fatalf("usuario prof = %d %s", code, raw)
	}
	var uOut struct {
		Usuario AdminUser `json:"usuario"`
	}
	if err := json.Unmarshal([]byte(raw), &uOut); err != nil {
		t.Fatal(err)
	}
	code, raw = postJSON(t, client, apiBase+"/profesionales", fx.AdmTok,
		fmt.Sprintf(`{"usuarioId":%q,"especialidadId":%q}`, uOut.Usuario.ID, espID))
	if code != http.StatusCreated {
		t.Fatalf("profesional = %d %s", code, raw)
	}
	var profOut struct {
		Profesional Professional `json:"profesional"`
	}
	if err := json.Unmarshal([]byte(raw), &profOut); err != nil {
		t.Fatal(err)
	}
	fx.ProfID = profOut.Profesional.ID

	if code, raw = doJSONRaw(t, client, http.MethodPut,
		apiBase+"/profesionales/"+fx.ProfID+"/servicios", fx.AdmTok,
		fmt.Sprintf(`{"servicioIds":[%q]}`, fx.SvcID)); code != http.StatusOK {
		t.Fatalf("habilitar servicio = %d %s", code, raw)
	}

	// vigenteDesde amplio para poder insertar citas de lunes pasados (desenlace RF12).
	hBody := fmt.Sprintf(`{"diaSemana":1,"horaInicio":"08:00","horaFin":"12:00","vigenteDesde":"2020-01-01"}`)
	if code, raw = postJSON(t, client, apiBase+"/profesionales/"+fx.ProfID+"/horarios", fx.AdmTok, hBody); code != http.StatusCreated {
		t.Fatalf("horario = %d %s", code, raw)
	}

	for i, dest := range []*string{&fx.PacienteA, &fx.PacienteB} {
		pacBody := fmt.Sprintf(`{"identificacion":"RF9-%s-%d","nombre":"Paciente RF9 %d","fechaNacimiento":"1990-01-01"}`, suffix, i, i)
		code, raw = postJSON(t, client, apiBase+"/pacientes", fx.AdmTok, pacBody)
		if code != http.StatusCreated {
			t.Fatalf("paciente %d = %d %s", i, code, raw)
		}
		var pacOut struct {
			Paciente struct {
				ID string `json:"id"`
			} `json:"paciente"`
		}
		if err := json.Unmarshal([]byte(raw), &pacOut); err != nil {
			t.Fatal(err)
		}
		*dest = pacOut.Paciente.ID
	}

	if code, raw = postJSON(t, client, apiBase+"/usuarios", fx.AdmTok,
		fmt.Sprintf(`{"username":%q,"password":"rf9pass12","rol":"recepcion"}`, fx.RecepUser)); code != http.StatusCreated {
		t.Fatalf("recepción = %d %s", code, raw)
	}
	fx.RecepTok = fetchToken(t, client, tokenURL, fx.RecepUser, "rf9pass12")
	fx.DrTok = fetchToken(t, client, tokenURL, fx.DrUser, "rf9pass12")
	fx.TokenURL = tokenURL

	return apiBase, tokenURL, client, fx
}

func TestIntegrationRF9ReservarCita(t *testing.T) {
	apiBase, _, client, fx := setupRF9Fixtures(t)

	inicio := time.Date(fx.Lunes.Year(), fx.Lunes.Month(), fx.Lunes.Day(), 8, 0, 0, 0, fx.Loc)
	body := fmt.Sprintf(`{
		"pacienteId":%q,
		"profesionalId":%q,
		"servicioId":%q,
		"inicio":%q
	}`, fx.PacienteA, fx.ProfID, fx.SvcID, inicio.Format(time.RFC3339))

	code, raw := postJSON(t, client, apiBase+"/citas?clinicaId=clinica-sur", fx.RecepTok, body)
	if code != http.StatusCreated {
		t.Fatalf("reservar = %d body=%s", code, raw)
	}
	var out struct {
		ClinicaIdentificador string      `json:"clinicaIdentificador"`
		Cita                 Appointment `json:"cita"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if out.ClinicaIdentificador != fx.Ident {
		t.Fatalf("I5: clínica=%s want %s", out.ClinicaIdentificador, fx.Ident)
	}
	if out.Cita.Estado != "reservada" || out.Cita.DuracionMinutos != 30 {
		t.Fatalf("cita=%+v", out.Cita)
	}

	// Mismo hueco, otro paciente → 409 (I1 / EXCLUDE).
	code, _ = postJSON(t, client, apiBase+"/citas", fx.RecepTok, fmt.Sprintf(`{
		"pacienteId":%q,"profesionalId":%q,"servicioId":%q,"inicio":%q
	}`, fx.PacienteB, fx.ProfID, fx.SvcID, inicio.Format(time.RFC3339)))
	if code != http.StatusConflict {
		t.Fatalf("segundo slot = %d want 409", code)
	}
}

func TestIntegrationRF9I8ConsultarReservarConsultar(t *testing.T) {
	apiBase, _, client, fx := setupRF9Fixtures(t)

	rf7 := fmt.Sprintf("%s/profesionales/%s/disponibilidad?fecha=%s&servicioId=%s",
		apiBase, fx.ProfID, fx.LunesStr, fx.SvcID)
	code, raw := getRaw(t, client, rf7, fx.RecepTok)
	if code != http.StatusOK {
		t.Fatalf("RF7 baseline = %d %s", code, raw)
	}
	antes := espaciosHoraLocal(t, raw)
	if !containsString(antes, "08:00") {
		t.Fatalf("08:00 no disponible: %v", antes)
	}

	inicio := time.Date(fx.Lunes.Year(), fx.Lunes.Month(), fx.Lunes.Day(), 8, 0, 0, 0, fx.Loc)
	body := fmt.Sprintf(`{"pacienteId":%q,"profesionalId":%q,"servicioId":%q,"inicio":%q}`,
		fx.PacienteA, fx.ProfID, fx.SvcID, inicio.Format(time.RFC3339))
	code, raw = postJSON(t, client, apiBase+"/citas", fx.RecepTok, body)
	if code != http.StatusCreated {
		t.Fatalf("reservar = %d %s", code, raw)
	}

	code, raw = getRaw(t, client, rf7, fx.RecepTok)
	if code != http.StatusOK {
		t.Fatalf("RF7 after = %d %s", code, raw)
	}
	despues := espaciosHoraLocal(t, raw)
	if containsString(despues, "08:00") {
		t.Fatalf("I8: 08:00 sigue libre tras reservar: %v", despues)
	}
}

func TestIntegrationRF9CarreraUnaGanadora(t *testing.T) {
	apiBase, _, client, fx := setupRF9Fixtures(t)

	inicio := time.Date(fx.Lunes.Year(), fx.Lunes.Month(), fx.Lunes.Day(), 9, 0, 0, 0, fx.Loc)
	inicioStr := inicio.Format(time.RFC3339)

	type result struct {
		code int
		raw  string
	}
	results := make([]result, 2)
	var wg sync.WaitGroup
	startGate := make(chan struct{})

	bodies := []string{
		fmt.Sprintf(`{"pacienteId":%q,"profesionalId":%q,"servicioId":%q,"inicio":%q}`,
			fx.PacienteA, fx.ProfID, fx.SvcID, inicioStr),
		fmt.Sprintf(`{"pacienteId":%q,"profesionalId":%q,"servicioId":%q,"inicio":%q}`,
			fx.PacienteB, fx.ProfID, fx.SvcID, inicioStr),
	}

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-startGate
			code, raw := postJSON(t, client, apiBase+"/citas", fx.RecepTok, bodies[i])
			results[i] = result{code: code, raw: raw}
		}(i)
	}
	close(startGate)
	wg.Wait()

	var won, lost int
	for _, r := range results {
		switch r.code {
		case http.StatusCreated:
			won++
		case http.StatusConflict:
			lost++
		default:
			t.Fatalf("código inesperado %d body=%s", r.code, r.raw)
		}
	}
	if won != 1 || lost != 1 {
		t.Fatalf("carrera: created=%d conflict=%d results=%+v", won, lost, results)
	}

	db := testDB(t)
	defer db.Close()
	var n int
	err := db.QueryRowContext(context.Background(), `
SELECT COUNT(*)::int
FROM "Cita" ci
JOIN "Clinica" c ON c."id" = ci."clinicaId"
WHERE c."identificador" = $1
  AND ci."profesionalId" = $2
  AND ci."estado" = 'reservada'
  AND ci."inicio" = $3`,
		fx.Ident, fx.ProfID, inicio.UTC(),
	).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("I1: citas reservadas en el slot = %d want 1", n)
	}
}

func TestIntegrationRF10a13CicloCita(t *testing.T) {
	apiBase, _, client, fx := setupRF9Fixtures(t)
	rf7 := fmt.Sprintf("%s/profesionales/%s/disponibilidad?fecha=%s&servicioId=%s",
		apiBase, fx.ProfID, fx.LunesStr, fx.SvcID)

	// --- RF10: reservar 08:00 y reprogramar a 10:00 ---
	inicio08 := time.Date(fx.Lunes.Year(), fx.Lunes.Month(), fx.Lunes.Day(), 8, 0, 0, 0, fx.Loc)
	inicio10 := time.Date(fx.Lunes.Year(), fx.Lunes.Month(), fx.Lunes.Day(), 10, 0, 0, 0, fx.Loc)
	code, raw := postJSON(t, client, apiBase+"/citas", fx.RecepTok, fmt.Sprintf(
		`{"pacienteId":%q,"profesionalId":%q,"servicioId":%q,"inicio":%q}`,
		fx.PacienteA, fx.ProfID, fx.SvcID, inicio08.Format(time.RFC3339)))
	if code != http.StatusCreated {
		t.Fatalf("reservar 08:00 = %d %s", code, raw)
	}
	var reserved struct {
		Cita Appointment `json:"cita"`
	}
	if err := json.Unmarshal([]byte(raw), &reserved); err != nil {
		t.Fatal(err)
	}

	if reserved.Cita.Estado != "reservada" || reserved.Cita.DuracionMinutos != 30 {
		t.Fatalf("I4: cita reservada=%+v (la duración la fija el servicio)", reserved.Cita)
	}

	code, raw = postJSON(t, client, apiBase+"/citas/"+reserved.Cita.ID+"/reprogramar", fx.RecepTok,
		fmt.Sprintf(`{"inicio":%q}`, inicio10.Format(time.RFC3339)))
	if code != http.StatusOK {
		t.Fatalf("reprogramar = %d %s", code, raw)
	}
	var repro struct {
		CitaAnteriorID string      `json:"citaAnteriorId"`
		Cita           Appointment `json:"cita"`
	}
	if err := json.Unmarshal([]byte(raw), &repro); err != nil {
		t.Fatal(err)
	}
	if repro.CitaAnteriorID != reserved.Cita.ID || repro.Cita.Estado != "reservada" || repro.Cita.CitaOrigenID != reserved.Cita.ID ||
		repro.Cita.DuracionMinutos != 30 {
		t.Fatalf("I4/RF10: repro=%+v (reprogramar no altera la duración del servicio)", repro)
	}

	code, raw = getRaw(t, client, rf7, fx.RecepTok)
	if code != http.StatusOK {
		t.Fatalf("RF7 post-repro = %d %s", code, raw)
	}
	espacios := espaciosHoraLocal(t, raw)
	if !containsString(espacios, "08:00") {
		t.Fatalf("I7: 08:00 debería liberarse tras reprogramar: %v", espacios)
	}
	if containsString(espacios, "10:00") {
		t.Fatalf("I7: 10:00 debería estar ocupado: %v", espacios)
	}

	// Profesional no reprograma.
	if code, _ = postJSON(t, client, apiBase+"/citas/"+repro.Cita.ID+"/reprogramar", fx.DrTok,
		fmt.Sprintf(`{"inicio":%q}`, inicio08.Format(time.RFC3339))); code != http.StatusForbidden {
		t.Fatalf("prof reprogramar = %d want 403", code)
	}

	// --- RF11: reservar 09:00 y cancelar ---
	inicio09 := time.Date(fx.Lunes.Year(), fx.Lunes.Month(), fx.Lunes.Day(), 9, 0, 0, 0, fx.Loc)
	code, raw = postJSON(t, client, apiBase+"/citas", fx.RecepTok, fmt.Sprintf(
		`{"pacienteId":%q,"profesionalId":%q,"servicioId":%q,"inicio":%q}`,
		fx.PacienteB, fx.ProfID, fx.SvcID, inicio09.Format(time.RFC3339)))
	if code != http.StatusCreated {
		t.Fatalf("reservar 09:00 = %d %s", code, raw)
	}
	var forCancel struct {
		Cita Appointment `json:"cita"`
	}
	_ = json.Unmarshal([]byte(raw), &forCancel)

	if code, _ = postJSON(t, client, apiBase+"/citas/"+forCancel.Cita.ID+"/cancelar", fx.DrTok,
		`{"motivo":"no"}`); code != http.StatusForbidden {
		t.Fatalf("prof cancelar = %d want 403", code)
	}

	code, raw = postJSON(t, client, apiBase+"/citas/"+forCancel.Cita.ID+"/cancelar", fx.RecepTok,
		`{"motivo":"paciente canceló"}`)
	if code != http.StatusOK {
		t.Fatalf("cancelar = %d %s", code, raw)
	}
	var cancelled struct {
		Cita Appointment `json:"cita"`
	}
	_ = json.Unmarshal([]byte(raw), &cancelled)
	if cancelled.Cita.Estado != "cancelada" {
		t.Fatalf("estado=%s", cancelled.Cita.Estado)
	}

	code, raw = getRaw(t, client, rf7, fx.RecepTok)
	if code != http.StatusOK {
		t.Fatalf("RF7 post-cancel = %d %s", code, raw)
	}
	espacios = espaciosHoraLocal(t, raw)
	if !containsString(espacios, "09:00") {
		t.Fatalf("I7: 09:00 debería liberarse tras cancelar: %v", espacios)
	}

	// RF13: cancelar de nuevo → 400
	if code, _ = postJSON(t, client, apiBase+"/citas/"+forCancel.Cita.ID+"/cancelar", fx.RecepTok,
		`{"motivo":"otra vez"}`); code != http.StatusBadRequest {
		t.Fatalf("cancelar final = %d want 400", code)
	}

	// --- RF12: cita en un lunes pasado (horario vigente desde 2020) + desenlace ---
	db := testDB(t)
	defer db.Close()
	lunesPasado := fx.Lunes.AddDate(0, 0, -7)
	inicioPasado := time.Date(lunesPasado.Year(), lunesPasado.Month(), lunesPasado.Day(), 8, 0, 0, 0, fx.Loc)
	var citaPasadaID string
	err := db.QueryRowContext(context.Background(), `
INSERT INTO "Cita" ("clinicaId", "pacienteId", "profesionalId", "servicioId", "inicio")
VALUES ((SELECT "id" FROM "Clinica" WHERE "identificador" = $1), $2, $3, $4, $5)
RETURNING "id"::text`,
		fx.Ident, fx.PacienteA, fx.ProfID, fx.SvcID, inicioPasado,
	).Scan(&citaPasadaID)
	if err != nil {
		t.Fatalf("insert cita pasada: %v", err)
	}

	code, raw = postJSON(t, client, apiBase+"/citas/"+citaPasadaID+"/desenlace", fx.DrTok,
		`{"estado":"atendida"}`)
	if code != http.StatusOK {
		t.Fatalf("desenlace propio = %d %s", code, raw)
	}
	var attended struct {
		Cita Appointment `json:"cita"`
	}
	_ = json.Unmarshal([]byte(raw), &attended)
	if attended.Cita.Estado != "atendida" {
		t.Fatalf("estado=%s", attended.Cita.Estado)
	}

	// RF13: segundo desenlace → 400
	if code, _ = postJSON(t, client, apiBase+"/citas/"+citaPasadaID+"/desenlace", fx.RecepTok,
		`{"estado":"ausente"}`); code != http.StatusBadRequest {
		t.Fatalf("segundo desenlace = %d want 400", code)
	}
}

// TestIntegrationI2PacienteSinSolape: I2 — un paciente no puede tener dos
// citas vigentes solapadas dentro de la misma clínica, aunque sean con
// profesionales distintos (EXCLUDE "CitaPacienteSinSolape").
func TestIntegrationI2PacienteSinSolape(t *testing.T) {
	apiBase, _, client, fx := setupRF9Fixtures(t)

	// Segundo profesional con el mismo servicio y horario de lunes.
	var esp struct {
		Especialidades []Especialidad `json:"especialidades"`
	}
	code, raw := getRaw(t, client, apiBase+"/especialidades", fx.AdmTok)
	if code != http.StatusOK {
		t.Fatalf("especialidades = %d %s", code, raw)
	}
	if err := json.Unmarshal([]byte(raw), &esp); err != nil || len(esp.Especialidades) == 0 {
		t.Fatalf("especialidades: %v %s", err, raw)
	}
	espID := esp.Especialidades[0].ID

	dr2User := fx.DrUser + "-b"
	if code, raw = postJSON(t, client, apiBase+"/usuarios", fx.AdmTok,
		fmt.Sprintf(`{"username":%q,"password":"rf9pass12","rol":"profesional"}`, dr2User)); code != http.StatusCreated {
		t.Fatalf("usuario dr2 = %d %s", code, raw)
	}
	var uOut struct {
		Usuario AdminUser `json:"usuario"`
	}
	if err := json.Unmarshal([]byte(raw), &uOut); err != nil {
		t.Fatal(err)
	}
	if code, raw = postJSON(t, client, apiBase+"/profesionales", fx.AdmTok,
		fmt.Sprintf(`{"usuarioId":%q,"especialidadId":%q}`, uOut.Usuario.ID, espID)); code != http.StatusCreated {
		t.Fatalf("profesional dr2 = %d %s", code, raw)
	}
	var profOut struct {
		Profesional Professional `json:"profesional"`
	}
	if err := json.Unmarshal([]byte(raw), &profOut); err != nil {
		t.Fatal(err)
	}
	prof2 := profOut.Profesional.ID
	if code, raw = doJSONRaw(t, client, http.MethodPut,
		apiBase+"/profesionales/"+prof2+"/servicios", fx.AdmTok,
		fmt.Sprintf(`{"servicioIds":[%q]}`, fx.SvcID)); code != http.StatusOK {
		t.Fatalf("habilitar servicio dr2 = %d %s", code, raw)
	}
	if code, raw = postJSON(t, client, apiBase+"/profesionales/"+prof2+"/horarios", fx.AdmTok,
		`{"diaSemana":1,"horaInicio":"08:00","horaFin":"12:00","vigenteDesde":"2020-01-01"}`); code != http.StatusCreated {
		t.Fatalf("horario dr2 = %d %s", code, raw)
	}

	// Cita vigente del paciente A con el profesional 1.
	inicio08 := time.Date(fx.Lunes.Year(), fx.Lunes.Month(), fx.Lunes.Day(), 8, 0, 0, 0, fx.Loc)
	if code, raw = postJSON(t, client, apiBase+"/citas", fx.RecepTok, fmt.Sprintf(
		`{"pacienteId":%q,"profesionalId":%q,"servicioId":%q,"inicio":%q}`,
		fx.PacienteA, fx.ProfID, fx.SvcID, inicio08.Format(time.RFC3339))); code != http.StatusCreated {
		t.Fatalf("reservar A+prof1 = %d %s", code, raw)
	}

	// Mismo paciente, otro profesional, mismo hueco → 409 (I2).
	if code, raw = postJSON(t, client, apiBase+"/citas", fx.RecepTok, fmt.Sprintf(
		`{"pacienteId":%q,"profesionalId":%q,"servicioId":%q,"inicio":%q}`,
		fx.PacienteA, prof2, fx.SvcID, inicio08.Format(time.RFC3339))); code != http.StatusConflict {
		t.Fatalf("I2: solape del mismo paciente = %d want 409, body=%s", code, raw)
	}

	// Control: otro paciente con el segundo profesional en el mismo hueco → 201
	// (el 409 anterior proviene del paciente, no del profesional).
	if code, raw = postJSON(t, client, apiBase+"/citas", fx.RecepTok, fmt.Sprintf(
		`{"pacienteId":%q,"profesionalId":%q,"servicioId":%q,"inicio":%q}`,
		fx.PacienteB, prof2, fx.SvcID, inicio08.Format(time.RFC3339))); code != http.StatusCreated {
		t.Fatalf("control paciente B = %d %s", code, raw)
	}

	// En la BD queda exactamente una cita vigente del paciente A en ese hueco.
	db := testDB(t)
	defer db.Close()
	fin08 := inicio08.Add(30 * time.Minute)
	var n int
	if err := db.QueryRowContext(context.Background(), `
SELECT count(*) FROM "Cita"
WHERE "pacienteId" = $1 AND "estado" = 'reservada'
  AND tstzrange("inicio", "fin", '[)') && tstzrange($2::timestamptz, $3::timestamptz, '[)')`,
		fx.PacienteA, inicio08, fin08,
	).Scan(&n); err != nil {
		t.Fatalf("count I2: %v", err)
	}
	if n != 1 {
		t.Fatalf("I2: citas vigentes del paciente en el hueco = %d want 1", n)
	}
}

// TestIntegrationI3DentroDeHorarioYBloqueo: I3 — toda cita vigente cae dentro
// del horario de atención y fuera de los bloqueos del profesional. La defensa
// es el trigger validarCitaEnHorario; el handler la traduce a 400.
func TestIntegrationI3DentroDeHorarioYBloqueo(t *testing.T) {
	apiBase, _, client, fx := setupRF9Fixtures(t)

	// Bloqueo del profesional (RF6): lunes 10:00–11:00.
	if code, raw := postJSON(t, client, apiBase+"/profesionales/"+fx.ProfID+"/bloqueos", fx.AdmTok, fmt.Sprintf(
		`{"inicio":%q,"fin":%q,"motivo":"permiso"}`,
		time.Date(fx.Lunes.Year(), fx.Lunes.Month(), fx.Lunes.Day(), 10, 0, 0, 0, fx.Loc).Format(time.RFC3339),
		time.Date(fx.Lunes.Year(), fx.Lunes.Month(), fx.Lunes.Day(), 11, 0, 0, 0, fx.Loc).Format(time.RFC3339),
	)); code != http.StatusCreated {
		t.Fatalf("bloqueo = %d %s", code, raw)
	}

	reservar := func(hora int) (int, string) {
		inicio := time.Date(fx.Lunes.Year(), fx.Lunes.Month(), fx.Lunes.Day(), hora, 0, 0, 0, fx.Loc)
		return postJSON(t, client, apiBase+"/citas", fx.RecepTok, fmt.Sprintf(
			`{"pacienteId":%q,"profesionalId":%q,"servicioId":%q,"inicio":%q}`,
			fx.PacienteA, fx.ProfID, fx.SvcID, inicio.Format(time.RFC3339)))
	}

	// Dentro de un bloqueo → rechazada.
	if code, raw := reservar(10); code != http.StatusBadRequest {
		t.Fatalf("I3: cita dentro de bloqueo = %d want 400, body=%s", code, raw)
	}

	// Fuera del horario de atención (20:00 > 12:00) → rechazada.
	if code, raw := reservar(20); code != http.StatusBadRequest {
		t.Fatalf("I3: cita fuera de horario = %d want 400, body=%s", code, raw)
	}

	// Control: un hueco dentro del horario y sin bloqueo sigue concediéndose.
	if code, raw := reservar(9); code != http.StatusCreated {
		t.Fatalf("I3: control 09:00 = %d %s", code, raw)
	}

	// En la BD no quedó ningún rastro de las dos rechazadas.
	db := testDB(t)
	defer db.Close()
	var n int
	if err := db.QueryRowContext(context.Background(), `
SELECT count(*) FROM "Cita"
WHERE "pacienteId" = $1 AND "estado" = 'reservada'
  AND (("inicio" AT TIME ZONE 'America/Costa_Rica')::time >= '20:00'
       OR ("inicio" AT TIME ZONE 'America/Costa_Rica')::time = '10:00')`,
		fx.PacienteA,
	).Scan(&n); err != nil {
		t.Fatalf("count I3: %v", err)
	}
	if n != 0 {
		t.Fatalf("I3: quedaron %d citas rechazadas insertadas", n)
	}
}
