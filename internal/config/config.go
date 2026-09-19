// Package config loads the strict, non-secret repository configuration.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Config contains only non-secret settings.
type Config struct {
	Mode          string   `json:"mode"`
	RiskThreshold float64  `json:"riskThreshold"`
	TimeoutMS     int      `json:"timeoutMs"`
	MaxDiffBytes  int      `json:"maxDiffBytes"`
	Exclude       []string `json:"exclude"`
}

// Defaults returns the shipped settings.
func Defaults() Config {
	return Config{Mode: "assist", RiskThreshold: 0.85, TimeoutMS: 2000, MaxDiffBytes: 65536, Exclude: []string{}}
}

// Load reads the optional config directly under the repository root.
func Load(root string) (Config, error) {
	c := Defaults()
	f, err := os.Open(filepath.Join(root, ".jev-preflight.json"))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, errors.New("cannot read repository config")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(b) > 65536 {
		return c, errors.New("repository config exceeds read limit")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return Defaults(), errors.New("invalid repository config JSON")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return Defaults(), errors.New("trailing repository config JSON")
	}
	// JSON null otherwise silently leaves scalar defaults in place.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil || fields == nil {
		return Defaults(), errors.New("repository config must be an object")
	}
	for name, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return Defaults(), fmt.Errorf("config %s cannot be null", name)
		}
	}
	if err := c.Validate(); err != nil {
		return Defaults(), err
	}
	return c, nil
}

// Validate rejects settings that cannot be applied safely.
func (c Config) Validate() error {
	if c.Mode != "assist" && c.Mode != "report" && c.Mode != "off" {
		return errors.New("config mode must be assist, report, or off")
	}
	if math.IsNaN(c.RiskThreshold) || math.IsInf(c.RiskThreshold, 0) || c.RiskThreshold < 0 || c.RiskThreshold > 1 {
		return errors.New("config riskThreshold must be between 0 and 1")
	}
	if c.TimeoutMS < 1 || c.TimeoutMS > 60000 {
		return errors.New("config timeoutMs must be between 1 and 60000")
	}
	if c.MaxDiffBytes < 1 || c.MaxDiffBytes > 1048576 {
		return errors.New("config maxDiffBytes must be between 1 and 1048576")
	}
	for _, pattern := range c.Exclude {
		if !validPattern(pattern) {
			return errors.New("config exclude must contain repository-relative slash patterns")
		}
	}
	return nil
}

func validPattern(pattern string) bool {
	if pattern == "" || strings.ContainsAny(pattern, "\\\x00\r\n:") || strings.HasPrefix(pattern, "/") {
		return false
	}
	for _, part := range strings.Split(pattern, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	base := strings.TrimSuffix(pattern, "/**")
	if strings.Contains(base, "**") {
		return false
	}
	_, err := path.Match(base, "")
	return err == nil
}

// APIKey resolves the plugin option before the process environment fallback.
func APIKey(getenv func(string) string) string {
	if key := strings.TrimSpace(getenv("CLAUDE_PLUGIN_OPTION_TYPESAFE_API_KEY")); key != "" {
		return key
	}
	return strings.TrimSpace(getenv("TYPESAFE_API_KEY"))
}
