package main

// Pruebas de invalidación de disponibilidad para RF3 (servicios) y RF4
// (profesionales). Usan los fakes de catalog_test.go y una Cache real con un
// Store en memoria.

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func setupInvalidationCatalogTest(t *testing.T, svcs ServiceStore, profs ProfessionalStore) (*gin.Engine, *testKeys) {
	t.Helper()

	tk := newTestKeys(t)
	srv := tk.jwksServer()
	t.Cleanup(srv.Close)

	return setupCatalogoTestEnv(t, srv.URL, nil, svcs, profs, &fakeIdentityAdmin{}, admClinicaUser()), tk
}

func TestUpdateServicioDuracionInvalidaMultiplesProfesionales(t *testing.T) {
	cacheStore := newInvalidationCacheStore(t)
	old := strings.Repeat("a", 32)
	seedEpoch(cacheStore, "clinica-norte", "p1", old)
	seedEpoch(cacheStore, "clinica-norte", "p2", old)
	seedEpoch(cacheStore, "clinica-norte", "p3", old)

	svcs := &fakeServiceStore{
		updated: Service{ID: "s1", Nombre: "Consulta", DuracionMinutos: 45, Activo: true},
		profesionalesPorServicio: map[string][]string{
			"s1": {"p1", "p2"},
		},
	}
	r, tk := setupInvalidationCatalogTest(t, svcs, nil)
	code, w := doJSON(t, r, http.MethodPatch, "/servicios/s1", mintAdm(t, tk), `{"duracionMinutos":45}`)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}

	assertEpochRotated(t, cacheStore, "clinica-norte", "p1", old)
	assertEpochRotated(t, cacheStore, "clinica-norte", "p2", old)
	assertEpochUnchanged(t, cacheStore, "clinica-norte", "p3", old)
	if got := cacheStore.setCount(); got != 2 {
		t.Fatalf("Set() calls = %d, want 2", got)
	}
}

func TestUpdateServicioNombreNoInvalidaCache(t *testing.T) {
	cacheStore := newInvalidationCacheStore(t)
	old := strings.Repeat("b", 32)
	seedEpoch(cacheStore, "clinica-norte", "p1", old)

	svcs := &fakeServiceStore{
		updated: Service{ID: "s1", Nombre: "Nuevo nombre", DuracionMinutos: 30, Activo: true},
	}
	r, tk := setupInvalidationCatalogTest(t, svcs, nil)
	code, _ := doJSON(t, r, http.MethodPatch, "/servicios/s1", mintAdm(t, tk), `{"nombre":"Nuevo nombre"}`)
	if code != http.StatusOK {
		t.Fatalf("status=%d want 200", code)
	}
	if got := cacheStore.setCount(); got != 0 {
		t.Fatalf("Set() calls = %d, want 0 para cambio de nombre", got)
	}
	assertEpochUnchanged(t, cacheStore, "clinica-norte", "p1", old)
}

func TestUpdateServicioRechazadoNoInvalidaCache(t *testing.T) {
	cacheStore := newInvalidationCacheStore(t)
	old := strings.Repeat("c", 32)
	seedEpoch(cacheStore, "clinica-norte", "p1", old)

	svcs := &fakeServiceStore{updateErr: errServicioDuplicado}
	r, tk := setupInvalidationCatalogTest(t, svcs, nil)
	code, _ := doJSON(t, r, http.MethodPatch, "/servicios/s1", mintAdm(t, tk), `{"duracionMinutos":45}`)
	if code != http.StatusConflict {
		t.Fatalf("status=%d want 409", code)
	}
	if got := cacheStore.setCount(); got != 0 {
		t.Fatalf("Set() calls = %d, want 0 cuando PostgreSQL rechaza", got)
	}
	assertEpochUnchanged(t, cacheStore, "clinica-norte", "p1", old)
}

func TestUpdateServicioActivoInvalidaCache(t *testing.T) {
	cacheStore := newInvalidationCacheStore(t)
	old := strings.Repeat("d", 32)
	seedEpoch(cacheStore, "clinica-norte", "p1", old)
	seedEpoch(cacheStore, "clinica-norte", "p2", old)

	svcs := &fakeServiceStore{
		updated: Service{ID: "s1", Nombre: "Consulta", DuracionMinutos: 30, Activo: false},
		profesionalesPorServicio: map[string][]string{
			"s1": {"p1"},
		},
	}
	r, tk := setupInvalidationCatalogTest(t, svcs, nil)
	code, _ := doJSON(t, r, http.MethodPatch, "/servicios/s1", mintAdm(t, tk), `{"activo":false}`)
	if code != http.StatusOK {
		t.Fatalf("status=%d want 200", code)
	}
	assertEpochRotated(t, cacheStore, "clinica-norte", "p1", old)
	assertEpochUnchanged(t, cacheStore, "clinica-norte", "p2", old)
}

func TestUpdateServicioDuracionYActivoParcialInvalidaCache(t *testing.T) {
	cacheStore := newInvalidationCacheStore(t)
	old := strings.Repeat("e", 32)
	seedEpoch(cacheStore, "clinica-norte", "p1", old)

	svcs := &fakeServiceStore{
		updated:                  Service{ID: "s1", Nombre: "Consulta", DuracionMinutos: 45, Activo: true},
		setActivoErr:             errors.New("fallo activo"),
		profesionalesPorServicio: map[string][]string{"s1": {"p1"}},
	}
	r, tk := setupInvalidationCatalogTest(t, svcs, nil)
	code, _ := doJSON(t, r, http.MethodPatch, "/servicios/s1", mintAdm(t, tk), `{"duracionMinutos":45,"activo":false}`)
	if code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", code)
	}
	// UpdateServicio confirmó el cambio de duración; la invalidación debe
	// ocurrir aunque SetServicioActivo haya fallado.
	assertEpochRotated(t, cacheStore, "clinica-norte", "p1", old)
}

func TestUpdateServicioProfesionalesDelServicioFalla(t *testing.T) {
	cacheStore := newInvalidationCacheStore(t)
	old := strings.Repeat("f", 32)
	seedEpoch(cacheStore, "clinica-norte", "p1", old)

	svcs := &fakeServiceStore{
		updated:                  Service{ID: "s1", Nombre: "Consulta", DuracionMinutos: 45, Activo: true},
		profesionalesErr:         errors.New("base de datos caida"),
		profesionalesPorServicio: map[string][]string{"s1": {"p1"}},
	}
	r, tk := setupInvalidationCatalogTest(t, svcs, nil)
	code, _ := doJSON(t, r, http.MethodPatch, "/servicios/s1", mintAdm(t, tk), `{"duracionMinutos":45}`)
	if code != http.StatusOK {
		t.Fatalf("status=%d want 200", code)
	}
	// No se pudo determinar quiénes están afectados; no se publica epoch.
	if got := cacheStore.setCount(); got != 0 {
		t.Fatalf("Set() calls = %d, want 0 cuando falla el listado", got)
	}
	assertEpochUnchanged(t, cacheStore, "clinica-norte", "p1", old)
}

func TestCreateServicioNoInvalidaCache(t *testing.T) {
	cacheStore := newInvalidationCacheStore(t)
	old := strings.Repeat("1", 32)
	seedEpoch(cacheStore, "clinica-norte", "p1", old)

	svcs := &fakeServiceStore{}
	r, tk := setupInvalidationCatalogTest(t, svcs, nil)
	body := `{"nombre":"Nuevo","duracionMinutos":30,"especialidadId":"e1"}`
	code, _ := doJSON(t, r, http.MethodPost, "/servicios", mintAdm(t, tk), body)
	if code != http.StatusCreated {
		t.Fatalf("status=%d want 201", code)
	}
	if got := cacheStore.setCount(); got != 0 {
		t.Fatalf("Set() calls = %d, want 0 para servicio nuevo", got)
	}
	assertEpochUnchanged(t, cacheStore, "clinica-norte", "p1", old)
}

func TestUpdateProfesionalActivoInvalidaCache(t *testing.T) {
	cacheStore := newInvalidationCacheStore(t)
	old := strings.Repeat("2", 32)
	seedEpoch(cacheStore, "clinica-norte", "p1", old)
	seedEpoch(cacheStore, "clinica-norte", "p2", old)

	profs := &fakeProfessionalStore{}
	r, tk := setupInvalidationCatalogTest(t, nil, profs)
	code, _ := doJSON(t, r, http.MethodPatch, "/profesionales/p1", mintAdm(t, tk), `{"activo":false}`)
	if code != http.StatusOK {
		t.Fatalf("status=%d want 200", code)
	}
	assertEpochRotated(t, cacheStore, "clinica-norte", "p1", old)
	assertEpochUnchanged(t, cacheStore, "clinica-norte", "p2", old)
}

func TestSetServiciosInvalidaCacheDelProfesional(t *testing.T) {
	cacheStore := newInvalidationCacheStore(t)
	old := strings.Repeat("3", 32)
	seedEpoch(cacheStore, "clinica-norte", "p1", old)
	seedEpoch(cacheStore, "clinica-norte", "p2", old)

	profs := &fakeProfessionalStore{
		setSvcsOut: []ProfessionalService{{ID: "s1", Nombre: "X", DuracionMinutos: 30}},
	}
	r, tk := setupInvalidationCatalogTest(t, nil, profs)
	code, _ := doJSON(t, r, http.MethodPut, "/profesionales/p1/servicios", mintAdm(t, tk), `{"servicioIds":["s1"]}`)
	if code != http.StatusOK {
		t.Fatalf("status=%d want 200", code)
	}
	assertEpochRotated(t, cacheStore, "clinica-norte", "p1", old)
	assertEpochUnchanged(t, cacheStore, "clinica-norte", "p2", old)
}

func TestCreateProfesionalNoInvalidaCache(t *testing.T) {
	cacheStore := newInvalidationCacheStore(t)
	old := strings.Repeat("4", 32)
	seedEpoch(cacheStore, "clinica-norte", "p1", old)

	profs := &fakeProfessionalStore{}
	r, tk := setupInvalidationCatalogTest(t, nil, profs)
	body := `{"usuarioId":"u-dr","especialidadId":"e1"}`
	code, _ := doJSON(t, r, http.MethodPost, "/profesionales", mintAdm(t, tk), body)
	if code != http.StatusCreated {
		t.Fatalf("status=%d want 201", code)
	}
	if got := cacheStore.setCount(); got != 0 {
		t.Fatalf("Set() calls = %d, want 0 para profesional nuevo", got)
	}
	assertEpochUnchanged(t, cacheStore, "clinica-norte", "p1", old)
}
