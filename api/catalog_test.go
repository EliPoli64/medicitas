package main

// Tests unitarios de RF2 (gestión de usuarios), RF3 (servicios) y
// RF4 (profesionales). Sin Docker: fakes sobre el router real con JWT.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// --- Fakes de stores ---

type fakeUserAdminStore struct {
	created    []struct{ clinica, idpSub, rol string }
	list       []AdminUser
	target     AdminUser
	targetErr  error
	updated    AdminUser
	updateErr  error
	gotClinica string
	gotID      string
}

func (f *fakeUserAdminStore) CreateUsuario(_ context.Context, clinicaIdent, idpSub, rolCodigo string) (AdminUser, error) {
	f.created = append(f.created, struct{ clinica, idpSub, rol string }{clinicaIdent, idpSub, rolCodigo})
	if f.updateErr != nil {
		return AdminUser{}, f.updateErr
	}
	return AdminUser{ID: "u-new", IdpSub: idpSub, RolCodigo: rolCodigo, Activo: true}, nil
}

func (f *fakeUserAdminStore) ListUsuarios(_ context.Context, clinicaIdent string) ([]AdminUser, error) {
	f.gotClinica = clinicaIdent
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return f.list, nil
}

func (f *fakeUserAdminStore) GetUsuario(_ context.Context, clinicaIdent, id string) (AdminUser, error) {
	f.gotClinica, f.gotID = clinicaIdent, id
	if f.targetErr != nil {
		return AdminUser{}, f.targetErr
	}
	return f.target, nil
}

func (f *fakeUserAdminStore) UpdateUsuario(_ context.Context, clinicaIdent, id string, _ *string, _ *bool) (AdminUser, error) {
	f.gotClinica, f.gotID = clinicaIdent, id
	if f.updateErr != nil {
		return AdminUser{}, f.updateErr
	}
	return f.updated, nil
}

type fakeServiceStore struct {
	created []struct {
		clinica, nombre string
		duracion        int
		espID           string
	}
	list                     []Service
	updated                  Service
	updateErr                error
	setActivoErr             error
	profesionalesPorServicio map[string][]string
	profesionalesErr         error
	gotClinica               string
}

func (f *fakeServiceStore) CreateServicio(_ context.Context, clinicaIdent, nombre string, duracionMinutos int, especialidadID string) (Service, error) {
	f.created = append(f.created, struct {
		clinica, nombre string
		duracion        int
		espID           string
	}{clinicaIdent, nombre, duracionMinutos, especialidadID})
	if f.updateErr != nil {
		return Service{}, f.updateErr
	}
	return Service{ID: "s-new", Nombre: nombre, DuracionMinutos: duracionMinutos, EspecialidadID: especialidadID, Activo: true}, nil
}

func (f *fakeServiceStore) ListServicios(_ context.Context, clinicaIdent string) ([]Service, error) {
	f.gotClinica = clinicaIdent
	return f.list, f.updateErr
}

func (f *fakeServiceStore) GetServicio(_ context.Context, _, _ string) (Service, error) {
	return Service{}, errors.New("no usado")
}

func (f *fakeServiceStore) UpdateServicio(_ context.Context, clinicaIdent, _ string, _ *string, _ *int) (Service, error) {
	f.gotClinica = clinicaIdent
	if f.updateErr != nil {
		return Service{}, f.updateErr
	}
	return f.updated, nil
}

func (f *fakeServiceStore) SetServicioActivo(_ context.Context, clinicaIdent, _ string, _ bool) (Service, error) {
	f.gotClinica = clinicaIdent
	if f.setActivoErr != nil {
		return Service{}, f.setActivoErr
	}
	if f.updateErr != nil {
		return Service{}, f.updateErr
	}
	return f.updated, nil
}

func (f *fakeServiceStore) PatchServicio(_ context.Context, clinicaIdent, _ string, _ *string, _ *int, activo *bool) (Service, error) {
	f.gotClinica = clinicaIdent
	if f.updateErr != nil {
		return Service{}, f.updateErr
	}
	if activo != nil && f.setActivoErr != nil {
		return Service{}, f.setActivoErr
	}
	return f.updated, nil
}

func (f *fakeServiceStore) ProfesionalesDelServicio(_ context.Context, clinicaIdent, servicioID string) ([]string, error) {
	f.gotClinica = clinicaIdent
	if f.profesionalesErr != nil {
		return nil, f.profesionalesErr
	}
	return f.profesionalesPorServicio[servicioID], nil
}

func (f *fakeServiceStore) ListEspecialidades(_ context.Context) ([]Especialidad, error) {
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return []Especialidad{{ID: "e1", Codigo: "general", Nombre: "Medicina General", Activo: true}}, nil
}

type fakeProfessionalStore struct {
	created    []struct{ clinica, usuarioID, espID string }
	list       []Professional
	setSvcs    []string
	setSvcsOut []ProfessionalService
	targetErr  error
	updateErr  error
	gotClinica string
}

func (f *fakeProfessionalStore) CreateProfesional(_ context.Context, clinicaIdent, usuarioID, especialidadID string) (Professional, error) {
	f.created = append(f.created, struct{ clinica, usuarioID, espID string }{clinicaIdent, usuarioID, especialidadID})
	if f.updateErr != nil {
		return Professional{}, f.updateErr
	}
	return Professional{ID: "p-new", UsuarioID: usuarioID, EspecialidadID: especialidadID, Activo: true}, nil
}

func (f *fakeProfessionalStore) ListProfesionales(_ context.Context, clinicaIdent string) ([]Professional, error) {
	f.gotClinica = clinicaIdent
	return f.list, f.updateErr
}

func (f *fakeProfessionalStore) GetProfesional(_ context.Context, _, _ string) (Professional, error) {
	return Professional{}, errors.New("no usado")
}

func (f *fakeProfessionalStore) SetProfesionalActivo(_ context.Context, clinicaIdent, _ string, _ bool) (Professional, error) {
	f.gotClinica = clinicaIdent
	if f.updateErr != nil {
		return Professional{}, f.updateErr
	}
	return Professional{ID: "p1", Activo: true}, nil
}

func (f *fakeProfessionalStore) SetServicios(_ context.Context, clinicaIdent, _ string, servicioIDs []string) ([]ProfessionalService, error) {
	f.gotClinica = clinicaIdent
	f.setSvcs = servicioIDs
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	if f.setSvcsOut != nil {
		return f.setSvcsOut, nil
	}
	return []ProfessionalService{}, nil
}

// setupCatalogoTestEnv arma el router con todos los stores falsos.
func setupCatalogoTestEnv(t *testing.T, srvURL string,
	usuarios UserAdminStore, servicios ServiceStore, profesionales ProfessionalStore,
	idp IdentityAdmin, users map[string]DomainUser) *gin.Engine {
	t.Helper()
	withTestIssuer(t, srvURL)

	prevPing := pingDBFn
	pingDBFn = func(context.Context) error { return nil }
	t.Cleanup(func() { pingDBFn = prevPing })

	prevUsers := userStore
	userStore = &fakeUserStore{bySub: users}
	t.Cleanup(func() { userStore = prevUsers })

	prevUA := userAdminStore
	userAdminStore = usuarios
	t.Cleanup(func() { userAdminStore = prevUA })

	prevSvc := serviceStore
	serviceStore = servicios
	t.Cleanup(func() { serviceStore = prevSvc })

	prevProf := professionalStore
	professionalStore = profesionales
	t.Cleanup(func() { professionalStore = prevProf })

	prevIDP := identityAdmin
	identityAdmin = idp
	t.Cleanup(func() { identityAdmin = prevIDP })

	gin.SetMode(gin.TestMode)
	return setupRouter()
}

func admClinicaUser() map[string]DomainUser {
	clinica := "clinica-norte"
	return map[string]DomainUser{
		"test-user": {
			UsuarioID: "u-adm", IdpSub: "test-user", Activo: true,
			ClinicaIdentificador: &clinica, RolCodigo: "adm_clinica",
		},
	}
}

func mintAdm(t *testing.T, tk *testKeys) string {
	t.Helper()
	return tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleAdmClinica}, testKid, "clinica-norte")
}

// --- RF2 ---

func TestCreateUsuarioOK(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	ua := &fakeUserAdminStore{}
	idp := &fakeIdentityAdmin{}
	r := setupCatalogoTestEnv(t, srv.URL, ua, nil, nil, idp, admClinicaUser())

	body := `{"username":"recep-2","password":"secret123","rol":"recepcion"}`
	code, w := doJSON(t, r, http.MethodPost, "/usuarios", mintAdm(t, tk), body)
	if code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	// La identidad se crea con el rol de realm equivalente y la clínica del token.
	if len(idp.created) != 1 || idp.created[0].RealmRole != "recepcion" || idp.created[0].ClinicaIdentificador != "clinica-norte" {
		t.Fatalf("identidad = %+v", idp.created)
	}
	// La fila Usuario queda anclada al sub de Keycloak.
	if len(ua.created) != 1 || ua.created[0].clinica != "clinica-norte" || ua.created[0].idpSub != "sub-nuevo" {
		t.Fatalf("usuario = %+v", ua.created)
	}
}

func TestCreateUsuarioRolNoAsignable400(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	ua := &fakeUserAdminStore{}
	idp := &fakeIdentityAdmin{}
	r := setupCatalogoTestEnv(t, srv.URL, ua, nil, nil, idp, admClinicaUser())

	// adm_plataforma no se asigna desde una clínica.
	body := `{"username":"plat-2","password":"secret123","rol":"adm_plataforma"}`
	code, _ := doJSON(t, r, http.MethodPost, "/usuarios", mintAdm(t, tk), body)
	if code != http.StatusBadRequest {
		t.Fatalf("status=%d", code)
	}
	if len(idp.created) != 0 {
		t.Fatalf("se provisionó un rol no asignable")
	}
}

func TestCreateUsuarioValidacion400(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	ua := &fakeUserAdminStore{}
	idp := &fakeIdentityAdmin{}
	r := setupCatalogoTestEnv(t, srv.URL, ua, nil, nil, idp, admClinicaUser())

	for nombre, body := range map[string]string{
		"password corta":    `{"username":"adm-x","password":"corto","rol":"recepcion"}`,
		"username inválido": `{"username":"a b","password":"secret123","rol":"recepcion"}`,
		"rol inexistente":   `{"username":"adm-x","password":"secret123","rol":"superadmin"}`,
	} {
		t.Run(nombre, func(t *testing.T) {
			code, _ := doJSON(t, r, http.MethodPost, "/usuarios", mintAdm(t, tk), body)
			if code != http.StatusBadRequest {
				t.Fatalf("status=%d", code)
			}
		})
	}
}

func TestCreateUsuarioRecepcion403(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	clinica := "clinica-norte"
	ua := &fakeUserAdminStore{}
	idp := &fakeIdentityAdmin{}
	r := setupCatalogoTestEnv(t, srv.URL, ua, nil, nil, idp, map[string]DomainUser{
		"test-user": {UsuarioID: "u", IdpSub: "test-user", Activo: true,
			ClinicaIdentificador: &clinica, RolCodigo: "recepcion"},
	})

	body := `{"username":"adm-x","password":"secret123","rol":"recepcion"}`
	tok := tk.mint(testIssuer, time.Now().Add(time.Hour), true, []string{RoleRecepcion}, testKid, "clinica-norte")
	code, _ := doJSON(t, r, http.MethodPost, "/usuarios", tok, body)
	if code != http.StatusForbidden {
		t.Fatalf("status=%d", code)
	}
}

func TestCreateUsuarioDuplicado409Compensa(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	ua := &fakeUserAdminStore{updateErr: errUsuarioExists}
	idp := &fakeIdentityAdmin{}
	r := setupCatalogoTestEnv(t, srv.URL, ua, nil, nil, idp, admClinicaUser())

	body := `{"username":"adm-x","password":"secret123","rol":"recepcion"}`
	code, _ := doJSON(t, r, http.MethodPost, "/usuarios", mintAdm(t, tk), body)
	if code != http.StatusConflict {
		t.Fatalf("status=%d", code)
	}
	if len(idp.deleted) != 1 {
		t.Fatalf("compensación = %v", idp.deleted)
	}
}

func TestUpdateUsuarioBajaLogica(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	ua := &fakeUserAdminStore{
		target:  AdminUser{ID: "u1", IdpSub: "sub-1", RolCodigo: "recepcion", Activo: true},
		updated: AdminUser{ID: "u1", IdpSub: "sub-1", RolCodigo: "recepcion", Activo: false},
	}
	idp := &fakeIdentityAdmin{}
	r := setupCatalogoTestEnv(t, srv.URL, ua, nil, nil, idp, admClinicaUser())

	code, w := doJSON(t, r, http.MethodPatch, "/usuarios/u1", mintAdm(t, tk), `{"activo":false}`)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	// La baja lógica se refleja en la identidad.
	if idp.enabledSet["sub-1"] != false {
		t.Fatalf("enabledSet = %v", idp.enabledSet)
	}
	// El usuario objetivo se resolvió dentro de la clínica del token (I5).
	if ua.gotClinica != "clinica-norte" || ua.gotID != "u1" {
		t.Fatalf("scope = %s/%s", ua.gotClinica, ua.gotID)
	}
}

func TestUpdateUsuarioCambioRol(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	ua := &fakeUserAdminStore{
		target:  AdminUser{ID: "u1", IdpSub: "sub-1", RolCodigo: "recepcion", Activo: true},
		updated: AdminUser{ID: "u1", IdpSub: "sub-1", RolCodigo: "adm_clinica", Activo: true},
	}
	idp := &fakeIdentityAdmin{}
	r := setupCatalogoTestEnv(t, srv.URL, ua, nil, nil, idp, admClinicaUser())

	code, _ := doJSON(t, r, http.MethodPatch, "/usuarios/u1", mintAdm(t, tk), `{"rol":"adm_clinica"}`)
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
	if idp.rolesSet["sub-1"] != "adm-clinica" {
		t.Fatalf("rolesSet = %v", idp.rolesSet)
	}
}

func TestUpdateUsuarioFueraDeClinica404(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	ua := &fakeUserAdminStore{targetErr: errUsuarioFueraDeClinica}
	idp := &fakeIdentityAdmin{}
	r := setupCatalogoTestEnv(t, srv.URL, ua, nil, nil, idp, admClinicaUser())

	code, _ := doJSON(t, r, http.MethodPatch, "/usuarios/otra-clinica", mintAdm(t, tk), `{"activo":false}`)
	if code != http.StatusNotFound {
		t.Fatalf("status=%d", code)
	}
	if len(idp.enabledSet) != 0 || len(idp.rolesSet) != 0 {
		t.Fatalf("se tocó la identidad de otro tenant")
	}
}

func TestUpdateUsuarioRolProfesionalActivo409(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	ua := &fakeUserAdminStore{
		target: AdminUser{ID: "u1", IdpSub: "sub-1", RolCodigo: "profesional", Activo: true},
	}
	idp := &fakeIdentityAdmin{}
	r := setupCatalogoTestEnv(t, srv.URL, ua, nil, nil, idp, admClinicaUser())

	code, _ := doJSON(t, r, http.MethodPatch, "/usuarios/u1", mintAdm(t, tk), `{"rol":"recepcion"}`)
	if code != http.StatusConflict {
		t.Fatalf("status=%d", code)
	}
	if len(idp.rolesSet) != 0 {
		t.Fatalf("se cambió el rol en Keycloak antes del rechazo")
	}
}

func TestUpdateUsuarioCompensaRolSiFallaBD(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	ua := &fakeUserAdminStore{
		target:    AdminUser{ID: "u1", IdpSub: "sub-1", RolCodigo: "recepcion", Activo: true},
		updateErr: errors.New("boom"),
	}
	idp := &fakeIdentityAdmin{}
	r := setupCatalogoTestEnv(t, srv.URL, ua, nil, nil, idp, admClinicaUser())

	code, _ := doJSON(t, r, http.MethodPatch, "/usuarios/u1", mintAdm(t, tk), `{"rol":"adm_clinica"}`)
	if code != http.StatusInternalServerError {
		t.Fatalf("status=%d", code)
	}
	// Compensación: el rol vuelve al previo en Keycloak.
	if idp.rolesSet["sub-1"] != "recepcion" {
		t.Fatalf("rolesSet = %v (esperaba rollback a recepcion)", idp.rolesSet)
	}
}

func TestListUsuariosSoloClinicaDelToken(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	ua := &fakeUserAdminStore{list: []AdminUser{{ID: "u1", IdpSub: "sub-1", RolCodigo: "recepcion", Activo: true}}}
	r := setupCatalogoTestEnv(t, srv.URL, ua, nil, nil, &fakeIdentityAdmin{}, admClinicaUser())

	code, w := doGet(t, r, "/usuarios?clinicaId=clinica-sur", mintAdm(t, tk))
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	if ua.gotClinica != "clinica-norte" {
		t.Fatalf("I5: la clínica vino del query: %s", ua.gotClinica)
	}
}

// --- RF3 ---

func TestCreateServicioOK(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	svcs := &fakeServiceStore{}
	r := setupCatalogoTestEnv(t, srv.URL, nil, svcs, nil, &fakeIdentityAdmin{}, admClinicaUser())

	body := `{"nombre":"Consulta general","duracionMinutos":30,"especialidadId":"e1"}`
	code, _ := doJSON(t, r, http.MethodPost, "/servicios", mintAdm(t, tk), body)
	if code != http.StatusCreated {
		t.Fatalf("status=%d", code)
	}
	if svcs.created[0].clinica != "clinica-norte" || svcs.created[0].duracion != 30 {
		t.Fatalf("servicio = %+v", svcs.created)
	}
}

func TestCreateServicioValidacion400(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	svcs := &fakeServiceStore{}
	r := setupCatalogoTestEnv(t, srv.URL, nil, svcs, nil, &fakeIdentityAdmin{}, admClinicaUser())

	for nombre, body := range map[string]string{
		"sin nombre":        `{"nombre":"","duracionMinutos":30,"especialidadId":"e1"}`,
		"duración cero":     `{"nombre":"X","duracionMinutos":0,"especialidadId":"e1"}`,
		"duración negativa": `{"nombre":"X","duracionMinutos":-5,"especialidadId":"e1"}`,
		"sin especialidad":  `{"nombre":"X","duracionMinutos":30,"especialidadId":""}`,
	} {
		t.Run(nombre, func(t *testing.T) {
			code, _ := doJSON(t, r, http.MethodPost, "/servicios", mintAdm(t, tk), body)
			if code != http.StatusBadRequest {
				t.Fatalf("status=%d", code)
			}
		})
	}
}

func TestCreateServicioDuplicado409(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	svcs := &fakeServiceStore{updateErr: errServicioDuplicado}
	r := setupCatalogoTestEnv(t, srv.URL, nil, svcs, nil, &fakeIdentityAdmin{}, admClinicaUser())

	body := `{"nombre":"Consulta general","duracionMinutos":30,"especialidadId":"e1"}`
	code, _ := doJSON(t, r, http.MethodPost, "/servicios", mintAdm(t, tk), body)
	if code != http.StatusConflict {
		t.Fatalf("status=%d", code)
	}
}

func TestCreateServicioEspecialidadInvalida400(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	svcs := &fakeServiceStore{updateErr: errEspecialidadInvalida}
	r := setupCatalogoTestEnv(t, srv.URL, nil, svcs, nil, &fakeIdentityAdmin{}, admClinicaUser())

	body := `{"nombre":"X","duracionMinutos":30,"especialidadId":"no-existe"}`
	code, _ := doJSON(t, r, http.MethodPost, "/servicios", mintAdm(t, tk), body)
	if code != http.StatusBadRequest {
		t.Fatalf("status=%d", code)
	}
}

func TestUpdateServicioFueraDeClinica404(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	svcs := &fakeServiceStore{updateErr: errServicioFueraDeClinica}
	r := setupCatalogoTestEnv(t, srv.URL, nil, svcs, nil, &fakeIdentityAdmin{}, admClinicaUser())

	code, _ := doJSON(t, r, http.MethodPatch, "/servicios/ajeno", mintAdm(t, tk), `{"nombre":"Nuevo"}`)
	if code != http.StatusNotFound {
		t.Fatalf("status=%d", code)
	}
}

func TestUpdateServicioDuracionInvalida400(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	svcs := &fakeServiceStore{}
	r := setupCatalogoTestEnv(t, srv.URL, nil, svcs, nil, &fakeIdentityAdmin{}, admClinicaUser())

	code, _ := doJSON(t, r, http.MethodPatch, "/servicios/s1", mintAdm(t, tk), `{"duracionMinutos":0}`)
	if code != http.StatusBadRequest {
		t.Fatalf("status=%d", code)
	}
}

func TestListEspecialidades(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	svcs := &fakeServiceStore{}
	r := setupCatalogoTestEnv(t, srv.URL, nil, svcs, nil, &fakeIdentityAdmin{}, admClinicaUser())

	code, w := doGet(t, r, "/especialidades", mintAdm(t, tk))
	if code != http.StatusOK || !contains(w.Body.String(), "Medicina General") {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
}

// --- RF4 ---

func TestCreateProfesionalOK(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	profs := &fakeProfessionalStore{}
	r := setupCatalogoTestEnv(t, srv.URL, nil, nil, profs, &fakeIdentityAdmin{}, admClinicaUser())

	body := `{"usuarioId":"u-dr","especialidadId":"e1"}`
	code, _ := doJSON(t, r, http.MethodPost, "/profesionales", mintAdm(t, tk), body)
	if code != http.StatusCreated {
		t.Fatalf("status=%d", code)
	}
	if profs.created[0].clinica != "clinica-norte" || profs.created[0].usuarioID != "u-dr" {
		t.Fatalf("profesional = %+v", profs.created)
	}
}

func TestCreateProfesionalSinUsuario400(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	profs := &fakeProfessionalStore{updateErr: errUsuarioNoProfesional}
	r := setupCatalogoTestEnv(t, srv.URL, nil, nil, profs, &fakeIdentityAdmin{}, admClinicaUser())

	body := `{"usuarioId":"u-recep","especialidadId":"e1"}`
	code, _ := doJSON(t, r, http.MethodPost, "/profesionales", mintAdm(t, tk), body)
	if code != http.StatusBadRequest {
		t.Fatalf("status=%d", code)
	}
}

func TestCreateProfesionalDuplicado409(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	profs := &fakeProfessionalStore{updateErr: errProfesionalDuplicado}
	r := setupCatalogoTestEnv(t, srv.URL, nil, nil, profs, &fakeIdentityAdmin{}, admClinicaUser())

	body := `{"usuarioId":"u-dr","especialidadId":"e1"}`
	code, _ := doJSON(t, r, http.MethodPost, "/profesionales", mintAdm(t, tk), body)
	if code != http.StatusConflict {
		t.Fatalf("status=%d", code)
	}
}

func TestSetServiciosOK(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	profs := &fakeProfessionalStore{setSvcsOut: []ProfessionalService{{ID: "s1", Nombre: "X", DuracionMinutos: 30}}}
	r := setupCatalogoTestEnv(t, srv.URL, nil, nil, profs, &fakeIdentityAdmin{}, admClinicaUser())

	code, w := doJSON(t, r, http.MethodPut, "/profesionales/p1/servicios", mintAdm(t, tk), `{"servicioIds":["s1","s1"]}`)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}
	if profs.gotClinica != "clinica-norte" {
		t.Fatalf("I5: scope = %s", profs.gotClinica)
	}
}

func TestSetServiciosIncompatible400(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	profs := &fakeProfessionalStore{updateErr: errServicioIncompatible}
	r := setupCatalogoTestEnv(t, srv.URL, nil, nil, profs, &fakeIdentityAdmin{}, admClinicaUser())

	code, _ := doJSON(t, r, http.MethodPut, "/profesionales/p1/servicios", mintAdm(t, tk), `{"servicioIds":["s2"]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("status=%d", code)
	}
}

func TestSetServiciosFueraDeClinica404(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	profs := &fakeProfessionalStore{updateErr: errProfesionalFueraDeClinica}
	r := setupCatalogoTestEnv(t, srv.URL, nil, nil, profs, &fakeIdentityAdmin{}, admClinicaUser())

	code, _ := doJSON(t, r, http.MethodPut, "/profesionales/ajeno/servicios", mintAdm(t, tk), `{"servicioIds":[]}`)
	if code != http.StatusNotFound {
		t.Fatalf("status=%d", code)
	}
}

func TestUpdateProfesionalActivo(t *testing.T) {
	tk := newTestKeys(t)
	srv := tk.jwksServer()
	defer srv.Close()

	profs := &fakeProfessionalStore{}
	r := setupCatalogoTestEnv(t, srv.URL, nil, nil, profs, &fakeIdentityAdmin{}, admClinicaUser())

	code, _ := doJSON(t, r, http.MethodPatch, "/profesionales/p1", mintAdm(t, tk), `{"activo":false}`)
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
