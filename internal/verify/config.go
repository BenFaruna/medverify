// Package verify matches validated label data against the local NAFDAC
// Greenbook dataset. It produces a typed, explainable result and never decides
// that a medicine is safe or genuine.
//
// Comparison logic is pure and takes plain structs; the Catalog interface only
// fetches candidates. Thresholds live in a versioned YAML file so tuning does
// not require code changes, and the version is stored on every result.
package verify

import (
	"bytes"
	"embed"
	"fmt"
	"os"

	"go.yaml.in/yaml/v3"
)

// ConfigVersion is the threshold file shipped with this build.
const ConfigVersion = "v1"

//go:embed config/v1.yaml
var configFiles embed.FS

// Config holds the matching thresholds. Lower thresholds find more candidates
// and make false matches more likely; higher thresholds make ambiguity and
// not-found outcomes more common. That trade-off belongs to a pharmacist, not
// to a test.
type Config struct {
	Version               string  `yaml:"version"`
	NameThreshold         float64 `yaml:"name_threshold"`
	NameOnlyThreshold     float64 `yaml:"name_only_threshold"`
	ManufacturerThreshold float64 `yaml:"manufacturer_threshold"`
	StrengthTolerance     float64 `yaml:"strength_tolerance"`
	AmbiguityMargin       float64 `yaml:"ambiguity_margin"`
	MaxNameCandidates     int     `yaml:"max_name_candidates"`
	MaxCandidates         int     `yaml:"max_candidates"`
	MaxVariantLookups     int     `yaml:"max_variant_lookups"`
}

// LoadConfig reads a threshold file. An empty path loads the embedded default
// for ConfigVersion. Unknown YAML fields are rejected so a typo cannot silently
// leave a threshold at zero.
func LoadConfig(path string) (Config, error) {
	var data []byte
	var err error
	if path == "" {
		data, err = configFiles.ReadFile("config/" + ConfigVersion + ".yaml")
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return Config{}, fmt.Errorf("verify: read config: %w", err)
	}

	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("verify: decode config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if c.Version == "" {
		return fmt.Errorf("verify: config version is required")
	}
	for name, value := range map[string]float64{
		"name_threshold":         c.NameThreshold,
		"name_only_threshold":    c.NameOnlyThreshold,
		"manufacturer_threshold": c.ManufacturerThreshold,
		"strength_tolerance":     c.StrengthTolerance,
		"ambiguity_margin":       c.AmbiguityMargin,
	} {
		if value < 0 || value > 1 {
			return fmt.Errorf("verify: %s must be between 0 and 1", name)
		}
	}
	if c.NameOnlyThreshold < c.NameThreshold {
		return fmt.Errorf("verify: name_only_threshold must be at least name_threshold")
	}
	for name, value := range map[string]int{
		"max_name_candidates": c.MaxNameCandidates,
		"max_candidates":      c.MaxCandidates,
		"max_variant_lookups": c.MaxVariantLookups,
	} {
		if value <= 0 {
			return fmt.Errorf("verify: %s must be greater than zero", name)
		}
	}
	return nil
}
