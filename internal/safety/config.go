package safety

import (
	"bytes"
	"embed"
	"fmt"
	"os"

	"go.yaml.in/yaml/v3"
)

// ConfigVersion is the embedded safety threshold version. It is recorded with
// each scan so a past result can be read against the thresholds it used.
const ConfigVersion = "safety-v1"

//go:embed config/*.yaml
var configFiles embed.FS

// Config holds the safety thresholds. Nothing clinical is hard-coded in the
// checker; changing a value is a reviewed file change.
type Config struct {
	Version                        string                `yaml:"version"`
	StrengthTolerance              float64               `yaml:"strength_tolerance"`
	MinIngredientConfidence        float64               `yaml:"min_ingredient_confidence"`
	WatchlistNameThreshold         float64               `yaml:"watchlist_name_threshold"`
	WatchlistManufacturerThreshold float64               `yaml:"watchlist_manufacturer_threshold"`
	Severity                       map[FlagType]Severity `yaml:"severity"`
}

// LoadConfig reads a threshold file. An empty path loads the embedded default.
// Unknown YAML fields are rejected so a typo cannot leave a threshold at zero.
func LoadConfig(path string) (Config, error) {
	var data []byte
	var err error
	if path == "" {
		data, err = configFiles.ReadFile("config/" + ConfigVersion + ".yaml")
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return Config{}, fmt.Errorf("safety: read config: %w", err)
	}

	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("safety: decode config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if c.Version == "" {
		return fmt.Errorf("safety: config version is required")
	}
	if c.StrengthTolerance <= 0 || c.StrengthTolerance > 0.5 {
		return fmt.Errorf("safety: strength_tolerance must be in (0, 0.5]")
	}
	if c.MinIngredientConfidence <= 0 || c.MinIngredientConfidence > 1 {
		return fmt.Errorf("safety: min_ingredient_confidence must be in (0, 1]")
	}
	if c.WatchlistNameThreshold <= 0 || c.WatchlistNameThreshold > 1 {
		return fmt.Errorf("safety: watchlist_name_threshold must be in (0, 1]")
	}
	if c.WatchlistManufacturerThreshold <= 0 || c.WatchlistManufacturerThreshold > 1 {
		return fmt.Errorf("safety: watchlist_manufacturer_threshold must be in (0, 1]")
	}
	for _, flagType := range FlagTypes {
		severity, ok := c.Severity[flagType]
		if !ok {
			return fmt.Errorf("safety: severity is missing for %s", flagType)
		}
		if !severity.Valid() {
			return fmt.Errorf("safety: severity for %s is %q", flagType, severity)
		}
	}
	return nil
}
