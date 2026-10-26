// Package config loads node configuration: a JSON file, then environment
// variable overrides. Each field names its override with an `env` tag; the
// node's prefix is prepended (OPENARCANUM_RELAY_ + LISTEN, and so on).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Duration is a time.Duration that reads "30s"-style strings from JSON.
type Duration struct{ time.Duration }

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"30s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// Load fills cfg (a pointer to a struct holding defaults) from path, if path is
// non-empty, then applies environment overrides. Unknown JSON keys are errors,
// so typos don't silently fall back to defaults.
func Load(path, envPrefix string, cfg any) error {
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		dec := json.NewDecoder(f)
		dec.DisallowUnknownFields()
		if err := dec.Decode(cfg); err != nil {
			return fmt.Errorf("config %s: %w", path, err)
		}
	}
	return applyEnv(reflect.ValueOf(cfg).Elem(), envPrefix)
}

// Save writes cfg as indented JSON with mode 0600.
func Save(path string, cfg any) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

var durationType = reflect.TypeOf(Duration{})

func applyEnv(v reflect.Value, prefix string) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		// Embedded settings structs (ops.Settings) share the prefix; JSON
		// flattens them the same way.
		if f.Anonymous && f.Type.Kind() == reflect.Struct && f.Type != durationType {
			if err := applyEnv(v.Field(i), prefix); err != nil {
				return err
			}
			continue
		}
		name := f.Tag.Get("env")
		if name == "" {
			continue
		}
		raw, ok := os.LookupEnv(prefix + name)
		if !ok {
			continue
		}
		if err := setFromString(v.Field(i), raw); err != nil {
			return fmt.Errorf("env %s%s: %w", prefix, name, err)
		}
	}
	return nil
}

func setFromString(fv reflect.Value, raw string) error {
	if fv.Type() == durationType {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return err
		}
		fv.Set(reflect.ValueOf(Duration{d}))
		return nil
	}
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int64, reflect.Int32:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return err
		}
		fv.SetInt(n)
	case reflect.Uint, reflect.Uint64, reflect.Uint32:
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return err
		}
		fv.SetUint(n)
	case reflect.Float64:
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return err
		}
		fv.SetFloat(n)
	case reflect.Slice:
		if fv.Type().Elem().Kind() != reflect.String {
			return errors.New("unsupported slice type")
		}
		var parts []string
		for _, p := range strings.Split(raw, ",") {
			if p = strings.TrimSpace(p); p != "" {
				parts = append(parts, p)
			}
		}
		fv.Set(reflect.ValueOf(parts))
	default:
		return fmt.Errorf("unsupported field kind %s", fv.Kind())
	}
	return nil
}
