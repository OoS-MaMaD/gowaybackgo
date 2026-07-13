package main

import "testing"

func TestEffectiveExclude(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"nothing set", Config{}, ""},
		{"exclude-defaults", Config{ExcludeDefaults: true}, defaultExclude},
		{"flag set, empty value uses defaults", Config{excludeFlagSet: true, ExcludeExt: ""}, defaultExclude},
		{"flag set with value", Config{excludeFlagSet: true, ExcludeExt: "js,css"}, "js,css"},
		{"exclude-defaults overrides value", Config{ExcludeDefaults: true, excludeFlagSet: true, ExcludeExt: "js"}, defaultExclude},
		{"value without flag is ignored", Config{ExcludeExt: "js"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.EffectiveExclude(); got != tt.want {
				t.Errorf("EffectiveExclude() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateNumericFlags(t *testing.T) {
	// A baseline of valid numeric values; each case overrides one field.
	base := func() Config {
		return Config{Workers: 20, PageWorkers: 10, Timeout: 80 * 1e9, RateLimit: 0}
	}
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"valid defaults", func(c *Config) {}, false},
		{"workers zero", func(c *Config) { c.Workers = 0 }, true},
		{"workers negative", func(c *Config) { c.Workers = -1 }, true},
		{"page-workers zero", func(c *Config) { c.PageWorkers = 0 }, true},
		{"page-workers negative", func(c *Config) { c.PageWorkers = -5 }, true},
		{"timeout zero", func(c *Config) { c.Timeout = 0 }, true},
		{"timeout negative", func(c *Config) { c.Timeout = -1 }, true},
		{"rate negative", func(c *Config) { c.RateLimit = -3 }, true},
		{"rate zero is valid", func(c *Config) { c.RateLimit = 0 }, false},
		{"rate huge is valid (guarded at runtime)", func(c *Config) { c.RateLimit = 1 << 40 }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := base()
			tt.mutate(&c)
			if err := c.validate(); (err != nil) != tt.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateExclusiveModes(t *testing.T) {
	// Start from valid numeric fields so only the mode logic is under test.
	base := func() Config {
		return Config{Workers: 20, PageWorkers: 10, Timeout: 80 * 1e9}
	}
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"no mode", func(c *Config) {}, false},
		{"single mode", func(c *Config) { c.Subs = true }, false},
		{"json alone", func(c *Config) { c.JSON = true }, false},
		{"two modes conflict", func(c *Config) { c.Subs = true; c.JSON = true }, true},
		{"three modes conflict", func(c *Config) { c.OnlyQuery = true; c.ExtractPaths = true; c.Subs = true }, true},
		{"no-query with a mode is allowed (warned)", func(c *Config) { c.NoQuery = true; c.Subs = true }, false},
		{"no-query alone", func(c *Config) { c.NoQuery = true }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := base()
			tt.mutate(&c)
			if err := c.validate(); (err != nil) != tt.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
