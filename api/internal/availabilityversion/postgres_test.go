package availabilityversion

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

func TestInterpretAgendaVersionAusenteNoEsCero(t *testing.T) {
	_, err := interpretAgendaVersion(0, sql.ErrNoRows)
	if !errors.Is(err, ErrAgendaVersionAusente) {
		t.Fatalf("err = %v, want ErrAgendaVersionAusente", err)
	}

	version, err := interpretAgendaVersion(0, nil)
	if err != nil || version != 0 {
		t.Fatalf("fila en 0: version=%d err=%v", version, err)
	}

	_, err = interpretAgendaVersion(0, fmt.Errorf("red: %w", sql.ErrNoRows))
	if !errors.Is(err, ErrAgendaVersionAusente) {
		t.Fatalf("error envuelto = %v, want ErrAgendaVersionAusente", err)
	}
}
