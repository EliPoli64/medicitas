package main

// Invalidación de la caché de disponibilidad después de mutar una agenda.
//
// La fuente de verdad es PostgreSQL. Por eso la invalidación se ejecuta
// únicamente cuando el store ya confirmó la mutación. Si Redis falla, el
// cambio en PostgreSQL sigue confirmado: la respuesta HTTP debe reflejar el
// éxito de la mutación y el fallo de caché se registra para observarlo.
//
// El alcance de InvalidateAgenda es (clínica, profesional): publica un epoch
// nuevo y deja inalcanzables todas las entradas de datos de esa agenda, sin
// importar servicio, fecha o paso. Es exactamente el alcance de un cambio de
// horario (RF5) o de un bloqueo (RF6).

import (
	"context"
	"fmt"
	"log"
)

// invalidarDisponibilidadDeAgenda publica un epoch nuevo para la agenda
// indicada. Devuelve nil cuando la caché está deshabilitada (Redis no estaba
// disponible al arrancar), de modo que los handlers no necesitan distinguir
// ese caso.
//
// El contexto de la petición se desacopla de su cancelación: si el cliente se
// desconecta después del COMMIT, la invalidación igual debe intentarse. La
// caché acota la escritura con Config.OpTimeout.
func invalidarDisponibilidadDeAgenda(ctx context.Context, clinica, profesionalID string) error {
	if availabilityCache == nil {
		return nil
	}

	ctx = context.WithoutCancel(ctx)
	if err := availabilityCache.InvalidateAgenda(ctx, clinica, profesionalID); err != nil {
		return fmt.Errorf("invalidar agenda %s/%s: %w", clinica, profesionalID, err)
	}
	return nil
}

// registrarFalloInvalidacion deja evidencia del fallo sin alterar la respuesta
// HTTP. Un error de invalidación no revierte el COMMIT y no debe presentarse
// como un fallo de la mutación.
func registrarFalloInvalidacion(err error) {
	if err != nil {
		log.Printf("cache: no se pudo invalidar la disponibilidad: %v", err)
	}
}

// invalidarAgendasPorServicio publica un epoch nuevo para cada profesional
// habilitado para el servicio en la clínica. Si falla la consulta de
// asociaciones o una invalidación individual, se registra pero no se altera la
// respuesta HTTP.
func invalidarAgendasPorServicio(ctx context.Context, clinica, servicioID string) {
	if availabilityCache == nil || serviceStore == nil {
		return
	}

	profesionales, err := serviceStore.ProfesionalesDelServicio(ctx, clinica, servicioID)
	if err != nil {
		log.Printf("cache: no se pudieron listar profesionales del servicio %s/%s: %v", clinica, servicioID, err)
		return
	}
	for _, pid := range profesionales {
		registrarFalloInvalidacion(invalidarDisponibilidadDeAgenda(ctx, clinica, pid))
	}
}
