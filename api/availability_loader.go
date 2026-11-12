package main

// Adaptador entre el cálculo real de disponibilidad (RF7) y el contrato
// availabilitycache.LoaderFunc.
//
// La caché no sabe calcular disponibilidad: solo decide si sirve un snapshot
// guardado o si debe pedir uno nuevo. Este archivo es el puente que, ante un
// MISS, reproduce la misma secuencia de validaciones y el mismo cálculo que
// listDisponibilidadProfesional, y devuelve el resultado como Snapshot.
//
// Decisiones importantes:
//
//   - Reutiliza DatosAgenda y espaciosDelDia. No hay una segunda
//     implementación del cálculo que pudiera divergir de RF7.
//   - Reutiliza los errores de dominio (errServicioDispoFuera, etc.) para que
//     la futura capa HTTP pueda mapearlos al mismo status que hoy.
//   - La clínica sale siempre del Request, que a su vez vendrá del token. El
//     Loader no acepta un tenant alternativo (I5).
//   - Usa el mismo reloj global nowFn que RF7. Así el handler y el Loader
//     comparten la noción de "ahora" y las pruebas pueden congelarlo.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"medicitas/api/internal/availabilitycache"
)

// disponibilidadLoader implementa availabilitycache.LoaderFunc sobre el store
// de disponibilidad (PostgreSQL en producción, fake en pruebas).
type disponibilidadLoader struct {
	store DisponibilidadStore
}

// loaderDisponibilidad es el loader global. Se inicializa en main con el store
// real; los tests pueden reemplazarlo junto con disponibilidadStore.
var loaderDisponibilidad interface {
	Load(context.Context, availabilitycache.Request) (availabilitycache.Snapshot, error)
}

// newDisponibilidadLoader construye el adaptador. El store puede ser el
// pgDisponibilidadStore real o cualquier implementación de DisponibilidadStore.
func newDisponibilidadLoader(store DisponibilidadStore) *disponibilidadLoader {
	return &disponibilidadLoader{store: store}
}

// Load calcula la disponibilidad de una combinación
// clínica+profesional+servicio+fecha con el paso solicitado.
//
// El orden de las validaciones replica RF7: zona horaria, servicio, fecha,
// profesional, habilitación y, recién entonces, la agenda. Así un request
// inválido no llega a consultar horarios, bloqueos ni citas.
func (l *disponibilidadLoader) Load(ctx context.Context, req availabilitycache.Request) (availabilitycache.Snapshot, error) {
	if l == nil || l.store == nil {
		return availabilitycache.Snapshot{}, errors.New("disponibilidad: store no configurado")
	}
	if req.ClinicaID == "" || req.ProfesionalID == "" || req.ServicioID == "" || req.Fecha == "" {
		return availabilitycache.Snapshot{}, fmt.Errorf("%w: faltan identificadores o fecha", availabilitycache.ErrInvalidRequest)
	}
	// La caché ya exige un paso positivo antes de llamar al Loader. Se repite
	// aquí porque el Loader también puede usarse directo (por ejemplo, en
	// pruebas o en un futuro consumidor interno).
	if req.PasoMinutos <= 0 {
		return availabilitycache.Snapshot{}, fmt.Errorf("%w: pasoMinutos debe ser positivo", availabilitycache.ErrInvalidRequest)
	}

	// La zona horaria define la medianoche local y, por lo tanto, la rejilla
	// de espacios. Sin ella no se pueden interpretar las fechas locales.
	zona, err := l.store.ZonaHoraria(ctx, req.ClinicaID)
	if err != nil {
		return availabilitycache.Snapshot{}, fmt.Errorf("resolver zona horaria: %w", err)
	}
	loc, err := time.LoadLocation(zona)
	if err != nil {
		return availabilitycache.Snapshot{}, fmt.Errorf("zona horaria inválida %q: %w", zona, err)
	}

	// La duración viene del servicio, nunca del cliente (I4).
	duracion, activo, err := l.store.ServicioDispo(ctx, req.ClinicaID, req.ServicioID)
	switch {
	case errors.Is(err, errServicioDispoFuera):
		return availabilitycache.Snapshot{}, err
	case err != nil:
		return availabilitycache.Snapshot{}, fmt.Errorf("consultar servicio: %w", err)
	}
	if !activo {
		return availabilitycache.Snapshot{}, errServicioDispoInactivo
	}
	if duracion <= 0 {
		// Dato corrupto: sin una duración positiva no hay espacios válidos.
		return availabilitycache.Snapshot{}, fmt.Errorf("duración de servicio inválida: %d", duracion)
	}

	// Fecha local de la clínica. El Request la trae como YYYY-MM-DD; se
	// interpreta en la zona de la clínica, no en UTC.
	fecha, err := time.ParseInLocation("2006-01-02", req.Fecha, loc)
	if err != nil {
		return availabilitycache.Snapshot{}, fmt.Errorf("%w: %v", errFechaDispoInvalida, err)
	}

	prof, err := l.store.ProfesionalDispo(ctx, req.ClinicaID, req.ProfesionalID)
	switch {
	case errors.Is(err, errProfesionalDispoFuera):
		return availabilitycache.Snapshot{}, err
	case err != nil:
		return availabilitycache.Snapshot{}, fmt.Errorf("consultar profesional: %w", err)
	}
	if !prof.Activo {
		return availabilitycache.Snapshot{}, errProfesionalDispoInactivo
	}

	habilitado, err := l.store.ProfesionalHabilitado(ctx, req.ClinicaID, req.ProfesionalID, req.ServicioID)
	if err != nil {
		return availabilitycache.Snapshot{}, fmt.Errorf("verificar habilitación: %w", err)
	}
	if !habilitado {
		return availabilitycache.Snapshot{}, errProfesionalSinServicio
	}

	// DatosAgenda espera un rango de instantes. Se usa la medianoche local y
	// el inicio del día siguiente para que la consulta cubra exactamente la
	// fecha pedida, incluso si el día tiene cambio de horario.
	dia := time.Date(fecha.Year(), fecha.Month(), fecha.Day(), 0, 0, 0, 0, loc)
	datos, err := l.store.DatosAgenda(ctx, req.ClinicaID, req.ProfesionalID, dia, dia.AddDate(0, 0, 1))
	if err != nil {
		return availabilitycache.Snapshot{}, fmt.Errorf("cargar agenda: %w", err)
	}

	// Mismo cálculo que RF7. `ahora` se toma una sola vez para que todos los
	// espacios se filtren con el mismo instante de referencia.
	ahora := nowFn()
	espacios := espaciosDelDia(datos, fecha, loc, duracion, req.PasoMinutos, ahora)

	slots := make([]availabilitycache.Slot, len(espacios))
	for i, e := range espacios {
		// El Snapshot solo necesita el intervalo. HoraLocal es una
		// representación de respuesta que se reconstruye en la capa HTTP a
		// partir de Inicio y de la zona horaria de la clínica.
		slots[i] = availabilitycache.Slot{Inicio: e.Inicio, Fin: e.Fin}
	}

	return availabilitycache.Snapshot{
		SchemaVersion:   availabilitycache.SnapshotSchemaVersion,
		ClinicaID:       req.ClinicaID,
		ProfesionalID:   req.ProfesionalID,
		ServicioID:      req.ServicioID,
		Fecha:           req.Fecha,
		ZonaHoraria:     zona,
		DuracionMinutos: duracion,
		PasoMinutos:     req.PasoMinutos,
		Espacios:        slots,
		GeneradoEn:      ahora.UTC(),
	}, nil
}
