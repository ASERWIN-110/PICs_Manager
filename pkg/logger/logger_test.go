package logger

import (
	"PICs_Manager/config"
	"log/slog"
	"testing"
)

func TestInitLoggerRejectsMissingConfig(t *testing.T) {
	oldConfig := config.C
	t.Cleanup(func() { config.C = oldConfig })
	config.C = nil

	if err := InitLogger(); err == nil {
		t.Fatal("expected missing config error")
	}
}

func TestSetLogLevelNormalizesInput(t *testing.T) {
	tests := []struct {
		input string
		want  slog.Level
	}{
		{input: "", want: slog.LevelInfo},
		{input: " INFO ", want: slog.LevelInfo},
		{input: "Warn", want: slog.LevelWarn},
		{input: "ERROR", want: slog.LevelError},
		{input: "debug", want: slog.LevelDebug},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			level := new(slog.LevelVar)
			if err := setLogLevel(tt.input, level); err != nil {
				t.Fatalf("setLogLevel returned error: %v", err)
			}
			if got := level.Level(); got != tt.want {
				t.Fatalf("expected level %s, got %s", tt.want, got)
			}
		})
	}
}

func TestSetLogLevelRejectsInvalidInput(t *testing.T) {
	level := new(slog.LevelVar)

	if err := setLogLevel("verbose", level); err == nil {
		t.Fatal("expected invalid level error")
	}
}
