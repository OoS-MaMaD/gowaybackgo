package main

import "testing"

func TestValidateExclusiveModes(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"no mode", Config{}, false},
		{"single mode", Config{Subs: true}, false},
		{"json alone", Config{JSON: true}, false},
		{"two modes conflict", Config{Subs: true, JSON: true}, true},
		{"three modes conflict", Config{OnlyQuery: true, ExtractPaths: true, Subs: true}, true},
		{"no-query with a mode is allowed (warned)", Config{NoQuery: true, Subs: true}, false},
		{"no-query alone", Config{NoQuery: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
