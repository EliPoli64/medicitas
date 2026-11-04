package availabilitycache

import (
	"fmt"
	"regexp"
	"time"
)

var (
	safePartPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	epochPattern    = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// keyBuilder construye las claves de Redis a partir de la configuración.
//
// Las claves tienen dos espacios separados:
//
//	{prefix}:avail:{format}:epoch:{clinicaId}:{profesionalId}
//	{prefix}:avail:{format}:data:{clinicaId}:{profesionalId}:{servicioId}:{fecha}:{epoch}
//
// El epoch es un token opaco. La clave de datos lo incluye para que una
// invalidación pueda dejar inalcanzables las entradas anteriores sin borrarlas.
type keyBuilder struct {
	prefix        string
	formatVersion string
}

func newKeyBuilder(cfg Config) (keyBuilder, error) {
	if err := cfg.validate(); err != nil {
		return keyBuilder{}, err
	}
	return keyBuilder{
		prefix:        cfg.Prefix,
		formatVersion: cfg.FormatVersion,
	}, nil
}

func (b keyBuilder) epochKey(clinicaID, profesionalID string) (string, error) {
	if err := validateSafePart("clinic ID", clinicaID); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if err := validateSafePart("professional ID", profesionalID); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}

	return fmt.Sprintf(
		"%s:avail:%s:epoch:%s:%s",
		b.prefix,
		b.formatVersion,
		clinicaID,
		profesionalID,
	), nil
}

func (b keyBuilder) dataKey(req Request, epoch string) (string, error) {
	if err := validateRequest(req); err != nil {
		return "", err
	}
	if !epochPattern.MatchString(epoch) {
		return "", fmt.Errorf("%w: invalid epoch token", ErrInvalidRequest)
	}

	return fmt.Sprintf(
		"%s:avail:%s:data:%s:%s:%s:%s:%s",
		b.prefix,
		b.formatVersion,
		req.ClinicaID,
		req.ProfesionalID,
		req.ServicioID,
		req.Fecha,
		epoch,
	), nil
}

func validateRequest(req Request) error {
	if err := validateSafePart("clinic ID", req.ClinicaID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if err := validateSafePart("professional ID", req.ProfesionalID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if err := validateSafePart("service ID", req.ServicioID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if err := validateDate(req.Fecha); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	return nil
}

func validateSafePart(name, value string) error {
	if !safePartPattern.MatchString(value) {
		return fmt.Errorf("%s must match %s", name, safePartPattern.String())
	}
	return nil
}

func validateDate(value string) error {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil || parsed.Format("2006-01-02") != value {
		return fmt.Errorf("date must use YYYY-MM-DD format")
	}
	return nil
}
