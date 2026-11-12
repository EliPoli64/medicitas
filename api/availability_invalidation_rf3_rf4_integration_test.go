//go:build integration

package main

// Prueba de integración de la invalidación de RF3 (cambio de duración de un
// servicio) contra Redis real. Verifica que, cuando un servicio está habilitado
// para varios profesionales, el epoch de cada uno de ellos rota y las entradas
// cacheadas anteriores dejan de servirse.

import (
	"net/http"
	"testing"

	"medicitas/api/internal/availabilitycache"
)

func TestIntegrationRF3DuracionInvalidaMultiplesProfesionales(t *testing.T) {
	cache, client, prefix := newIntegrationInvalidationCache(t)

	reqP1 := availabilitycache.Request{
		ClinicaID:     "clinica-norte",
		ProfesionalID: "p1",
		ServicioID:    "s1",
		Fecha:         "2026-11-20",
		PasoMinutos:   30,
	}
	reqP2 := availabilitycache.Request{
		ClinicaID:     "clinica-norte",
		ProfesionalID: "p2",
		ServicioID:    "s1",
		Fecha:         "2026-11-20",
		PasoMinutos:   30,
	}

	beforeP1 := seedCachedAvailability(t, cache, client, prefix, reqP1)
	beforeP2 := seedCachedAvailability(t, cache, client, prefix, reqP2)

	tk := newTestKeys(t)
	srv := tk.jwksServer()
	t.Cleanup(srv.Close)

	svcs := &fakeServiceStore{
		updated: Service{ID: "s1", Nombre: "Consulta", DuracionMinutos: 45, Activo: true},
		profesionalesPorServicio: map[string][]string{
			"s1": {"p1", "p2"},
		},
	}
	r := setupCatalogoTestEnv(t, srv.URL, nil, svcs, nil, &fakeIdentityAdmin{}, admClinicaUser())

	code, w := doJSON(t, r, http.MethodPatch, "/servicios/s1", mintAdm(t, tk), `{"duracionMinutos":45}`)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, w.Body.String())
	}

	assertCacheInvalidated(t, cache, client, prefix, reqP1, beforeP1)
	assertCacheInvalidated(t, cache, client, prefix, reqP2, beforeP2)
}
