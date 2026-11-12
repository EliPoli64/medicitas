package availabilitycache

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// normalizeSnapshot completa los datos que la caché controla y normaliza los
// instantes a UTC. No inventa identificadores ni fechas: esos deben venir del
// Loader y se validan contra el Request.
//
// Copia la lista de espacios antes de modificarla. Un Loader puede devolver un
// valor compartido o reutilizado; la caché no debe escribir en esa memoria.
func normalizeSnapshot(snapshot *Snapshot) {
	if snapshot.SchemaVersion == 0 {
		snapshot.SchemaVersion = SnapshotSchemaVersion
	}
	if snapshot.Espacios == nil {
		snapshot.Espacios = []Slot{}
	} else {
		slots := make([]Slot, len(snapshot.Espacios))
		copy(slots, snapshot.Espacios)
		snapshot.Espacios = slots
	}
	for i := range snapshot.Espacios {
		snapshot.Espacios[i].Inicio = snapshot.Espacios[i].Inicio.UTC()
		snapshot.Espacios[i].Fin = snapshot.Espacios[i].Fin.UTC()
	}
	if snapshot.GeneradoEn.IsZero() {
		snapshot.GeneradoEn = time.Now().UTC()
	} else {
		snapshot.GeneradoEn = snapshot.GeneradoEn.UTC()
	}
}

func encodeSnapshot(snapshot Snapshot) (string, error) {
	normalizeSnapshot(&snapshot)

	raw, err := json.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("encode availability snapshot: %w", err)
	}
	return string(raw), nil
}

func decodeSnapshot(raw string, req Request) (Snapshot, error) {
	var snapshot Snapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	if err := validateSnapshot(snapshot, req); err != nil {
		return Snapshot{}, err
	}
	// Un productor anterior pudo serializar una lista vacía como null. Se
	// normaliza para que la respuesta siempre tenga la misma forma.
	if snapshot.Espacios == nil {
		snapshot.Espacios = []Slot{}
	}
	return snapshot, nil
}

func validateSnapshot(snapshot Snapshot, req Request) error {
	if snapshot.SchemaVersion != SnapshotSchemaVersion {
		return fmt.Errorf("%w: schema version %d, want %d", ErrInvalidPayload, snapshot.SchemaVersion, SnapshotSchemaVersion)
	}
	if snapshot.ClinicaID != req.ClinicaID {
		return fmt.Errorf("%w: clinic ID does not match request", ErrInvalidPayload)
	}
	if snapshot.ProfesionalID != req.ProfesionalID {
		return fmt.Errorf("%w: professional ID does not match request", ErrInvalidPayload)
	}
	if snapshot.ServicioID != req.ServicioID {
		return fmt.Errorf("%w: service ID does not match request", ErrInvalidPayload)
	}
	if snapshot.Fecha != req.Fecha {
		return fmt.Errorf("%w: date does not match request", ErrInvalidPayload)
	}
	if strings.TrimSpace(snapshot.ZonaHoraria) == "" {
		return fmt.Errorf("%w: time zone is empty", ErrInvalidPayload)
	}
	if snapshot.DuracionMinutos <= 0 {
		return fmt.Errorf("%w: duration must be positive", ErrInvalidPayload)
	}
	if snapshot.PasoMinutos <= 0 {
		return fmt.Errorf("%w: step minutes must be positive", ErrInvalidPayload)
	}
	// La clave ya separa por paso, pero el valor también lo declara. Esto
	// detecta un payload manipulado, mezclado o generado con otra rejilla.
	if snapshot.PasoMinutos != req.PasoMinutos {
		return fmt.Errorf("%w: step minutes does not match request", ErrInvalidPayload)
	}
	for i, slot := range snapshot.Espacios {
		if slot.Inicio.IsZero() || slot.Fin.IsZero() {
			return fmt.Errorf("%w: slot %d has a zero time", ErrInvalidPayload, i)
		}
		if !slot.Fin.After(slot.Inicio) {
			return fmt.Errorf("%w: slot %d ends before it starts", ErrInvalidPayload, i)
		}
	}
	return nil
}
