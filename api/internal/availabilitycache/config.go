package availabilitycache

import "fmt"

// validate verifica que la configuración tenga valores utilizables.
func (c Config) validate() error {
	if err := validateSafePart("prefix", c.Prefix); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	if err := validateSafePart("format version", c.FormatVersion); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	if c.DataTTL <= 0 {
		return fmt.Errorf("%w: data TTL must be positive", ErrInvalidConfig)
	}
	if c.EpochTTL < 0 {
		return fmt.Errorf("%w: epoch TTL cannot be negative", ErrInvalidConfig)
	}
	if c.OpTimeout < 0 {
		return fmt.Errorf("%w: operation timeout cannot be negative", ErrInvalidConfig)
	}
	return nil
}
