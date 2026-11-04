package availabilitycache

import (
	"errors"
	"testing"
	"time"
)

func TestKeyBuilder_DataKey(t *testing.T) {
	builder, err := newKeyBuilder(DefaultConfig())
	if err != nil {
		t.Fatalf("newKeyBuilder() error = %v", err)
	}

	req := Request{
		ClinicaID:     "clinica-a",
		ProfesionalID: "prof-1",
		ServicioID:    "serv-1",
		Fecha:         "2026-11-10",
	}
	epoch := "0123456789abcdef0123456789abcdef"

	got, err := builder.dataKey(req, epoch)
	if err != nil {
		t.Fatalf("dataKey() error = %v", err)
	}

	want := "medicitas:avail:v1:data:clinica-a:prof-1:serv-1:2026-11-10:" + epoch
	if got != want {
		t.Fatalf("dataKey() = %q, want %q", got, want)
	}
}

func TestKeyBuilder_EpochKey(t *testing.T) {
	builder, err := newKeyBuilder(DefaultConfig())
	if err != nil {
		t.Fatalf("newKeyBuilder() error = %v", err)
	}

	got, err := builder.epochKey("clinica-a", "prof-1")
	if err != nil {
		t.Fatalf("epochKey() error = %v", err)
	}

	want := "medicitas:avail:v1:epoch:clinica-a:prof-1"
	if got != want {
		t.Fatalf("epochKey() = %q, want %q", got, want)
	}
}

func TestKeyBuilder_TenantIsolation(t *testing.T) {
	builder, err := newKeyBuilder(DefaultConfig())
	if err != nil {
		t.Fatalf("newKeyBuilder() error = %v", err)
	}
	epoch := "0123456789abcdef0123456789abcdef"

	reqA := Request{
		ClinicaID:     "clinica-a",
		ProfesionalID: "prof-1",
		ServicioID:    "serv-1",
		Fecha:         "2026-11-10",
	}
	reqB := reqA
	reqB.ClinicaID = "clinica-b"

	keyA, err := builder.dataKey(reqA, epoch)
	if err != nil {
		t.Fatalf("dataKey(A) error = %v", err)
	}
	keyB, err := builder.dataKey(reqB, epoch)
	if err != nil {
		t.Fatalf("dataKey(B) error = %v", err)
	}
	if keyA == keyB {
		t.Fatal("data keys from different clinics must not collide")
	}
}

func TestKeyBuilder_RejectsInvalidDate(t *testing.T) {
	builder, err := newKeyBuilder(DefaultConfig())
	if err != nil {
		t.Fatalf("newKeyBuilder() error = %v", err)
	}
	epoch := "0123456789abcdef0123456789abcdef"

	invalidDates := []string{"", "2026-1-1", "2026-13-01", "10/11/2026"}
	for _, date := range invalidDates {
		t.Run(date, func(t *testing.T) {
			req := Request{
				ClinicaID:     "clinica-a",
				ProfesionalID: "prof-1",
				ServicioID:    "serv-1",
				Fecha:         date,
			}
			if _, err := builder.dataKey(req, epoch); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("dataKey() error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestKeyBuilder_RejectsInvalidIDs(t *testing.T) {
	builder, err := newKeyBuilder(DefaultConfig())
	if err != nil {
		t.Fatalf("newKeyBuilder() error = %v", err)
	}
	epoch := "0123456789abcdef0123456789abcdef"

	invalidIDs := []string{"", "a:b", "a b", "clínica"}
	for _, id := range invalidIDs {
		t.Run(id, func(t *testing.T) {
			req := Request{
				ClinicaID:     id,
				ProfesionalID: "prof-1",
				ServicioID:    "serv-1",
				Fecha:         "2026-11-10",
			}
			if _, err := builder.dataKey(req, epoch); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("dataKey() error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestKeyBuilder_RejectsInvalidEpoch(t *testing.T) {
	builder, err := newKeyBuilder(DefaultConfig())
	if err != nil {
		t.Fatalf("newKeyBuilder() error = %v", err)
	}
	req := Request{
		ClinicaID:     "clinica-a",
		ProfesionalID: "prof-1",
		ServicioID:    "serv-1",
		Fecha:         "2026-11-10",
	}

	invalidEpochs := []string{"", "short", "0123456789ABCDEF0123456789ABCDEF"}
	for _, epoch := range invalidEpochs {
		t.Run(epoch, func(t *testing.T) {
			if _, err := builder.dataKey(req, epoch); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("dataKey() error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestConfig_Validate(t *testing.T) {
	if err := DefaultConfig().validate(); err != nil {
		t.Fatalf("DefaultConfig().validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"empty prefix", func(c *Config) { c.Prefix = "" }},
		{"invalid prefix", func(c *Config) { c.Prefix = "medi citas" }},
		{"empty format", func(c *Config) { c.FormatVersion = "" }},
		{"zero data TTL", func(c *Config) { c.DataTTL = 0 }},
		{"negative epoch TTL", func(c *Config) { c.EpochTTL = -time.Second }},
		{"negative op timeout", func(c *Config) { c.OpTimeout = -time.Second }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.mutate(&cfg)
			if err := cfg.validate(); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("validate() error = %v, want ErrInvalidConfig", err)
			}
		})
	}
}
