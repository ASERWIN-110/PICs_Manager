package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestServerConfigJSONUsesDurationString(t *testing.T) {
	cfg := Config{
		Server: ServerConfig{
			Port:    ":8080",
			Timeout: 30 * time.Second,
		},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	var payload struct {
		Server struct {
			Port    string `json:"port"`
			Timeout string `json:"timeout"`
		} `json:"server"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("Unmarshal marshaled JSON returned error: %v", err)
	}
	if payload.Server.Port != ":8080" || payload.Server.Timeout != "30s" {
		t.Fatalf("unexpected server JSON: %+v", payload.Server)
	}

	var decoded Config
	if err := json.Unmarshal([]byte(`{"server":{"port":":9090","timeout":"45s"}}`), &decoded); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if decoded.Server.Port != ":9090" {
		t.Fatalf("expected port :9090, got %q", decoded.Server.Port)
	}
	if decoded.Server.Timeout != 45*time.Second {
		t.Fatalf("expected timeout 45s, got %s", decoded.Server.Timeout)
	}
}

func TestServerConfigYAMLUsesDurationString(t *testing.T) {
	cfg := Config{
		Server: ServerConfig{
			Port:    ":8080",
			Timeout: 30 * time.Second,
		},
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	if !strings.Contains(string(data), "timeout: 30s") {
		t.Fatalf("expected YAML duration string, got:\n%s", data)
	}
}

func TestValidateConfigAcceptsLegacyImagePatterns(t *testing.T) {
	cfg := Config{
		Scanner: ScannerConfig{
			Mode:        "full",
			WorkerCount: 4,
			BatchSize:   100,
			FilePatterns: []string{
				`^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$`,
			},
		},
	}

	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("ValidateConfig returned error: %v", err)
	}
}

func TestValidateConfigRejectsInvalidMode(t *testing.T) {
	err := ValidateConfig(Config{
		Scanner: ScannerConfig{
			Mode:         "bad",
			FilePatterns: []string{`^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$`},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "scanner.mode") {
		t.Fatalf("expected scanner.mode error, got %v", err)
	}
}

func TestValidateConfigRejectsInvalidRegex(t *testing.T) {
	err := ValidateConfig(Config{
		Scanner: ScannerConfig{
			Mode:         "classifyOnly",
			FilePatterns: []string{"("},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid regex") {
		t.Fatalf("expected invalid regex error, got %v", err)
	}
}

func TestValidateConfigRejectsMissingPatterns(t *testing.T) {
	err := ValidateConfig(Config{})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateConfigRejectsInvalidLoggerFormat(t *testing.T) {
	err := ValidateConfig(Config{
		Logger: LoggerConfig{Format: "xml"},
		Scanner: ScannerConfig{
			FilePatterns: []string{`^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$`},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "logger.format") {
		t.Fatalf("expected logger.format error, got %v", err)
	}
}

func TestValidateConfigRejectsInvalidMaintenanceWindow(t *testing.T) {
	err := ValidateConfig(Config{
		Scanner: ScannerConfig{
			MaintenanceWindow: "bad",
			FilePatterns:      []string{`^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$`},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "maintenanceWindow") {
		t.Fatalf("expected maintenanceWindow error, got %v", err)
	}
}

func TestWithMongoCredentialsAddsAuthSource(t *testing.T) {
	got := withMongoCredentials("mongodb://localhost:27017", "dev_user", "secret", "admin")

	if got != "mongodb://dev_user:secret@localhost:27017/?authSource=admin" {
		t.Fatalf("unexpected URI: %s", got)
	}
}

func TestApplyEnvOverridesUsesExplicitURIAndAppCredentials(t *testing.T) {
	t.Setenv("SERVER_PORT", ":18080")
	t.Setenv("DATABASE_URI", "mongodb://127.0.0.1:27017")
	t.Setenv("DATABASE_NAME", "pics_test")
	t.Setenv("MONGO_APP_USERNAME", "dev_user")
	t.Setenv("MONGO_APP_PASSWORD", "secret")
	t.Setenv("MONGO_APP_AUTH_SOURCE", "admin")

	cfg := &Config{Database: DatabaseConfig{URI: "mongodb://localhost:27017", Name: "media_manager"}}

	applyEnvOverrides(cfg)

	if cfg.Server.Port != ":18080" {
		t.Fatalf("expected server port override, got %q", cfg.Server.Port)
	}
	if cfg.Database.Name != "pics_test" {
		t.Fatalf("expected database name override, got %q", cfg.Database.Name)
	}
	if cfg.Database.URI != "mongodb://dev_user:secret@127.0.0.1:27017/?authSource=admin" {
		t.Fatalf("unexpected database URI: %s", cfg.Database.URI)
	}
}

func TestLoadOptionalEnvFileDoesNotOverrideExistingEnv(t *testing.T) {
	envFile := t.TempDir() + "/mongo.env"
	if err := os.WriteFile(envFile, []byte("MONGO_APP_USERNAME=file_user\nMONGO_APP_PASSWORD=file_secret\n"), 0600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	t.Setenv("PIC_MANAGER_ENV_FILE", envFile)
	t.Setenv("MONGO_APP_USERNAME", "existing_user")

	if err := loadOptionalEnvFile(); err != nil {
		t.Fatalf("loadOptionalEnvFile returned error: %v", err)
	}

	if got := os.Getenv("MONGO_APP_USERNAME"); got != "existing_user" {
		t.Fatalf("expected existing env to win, got %q", got)
	}
	if got := os.Getenv("MONGO_APP_PASSWORD"); got != "file_secret" {
		t.Fatalf("expected password from env file, got %q", got)
	}
}

func TestLoadOptionalEnvFileReturnsErrorForExplicitMissingPath(t *testing.T) {
	t.Setenv("PIC_MANAGER_ENV_FILE", t.TempDir()+"/missing.env")

	err := loadOptionalEnvFile()

	if err == nil || !strings.Contains(err.Error(), "无法读取环境变量文件") {
		t.Fatalf("expected explicit env file error, got %v", err)
	}
}
