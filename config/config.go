package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type SeriesGroupRule struct {
	Name    string `mapstructure:"name" json:"name" yaml:"name"`
	Pattern string `mapstructure:"pattern" json:"pattern" yaml:"pattern"`
}

type MediaTypeConfig struct {
	Type         string   `mapstructure:"type" json:"type" yaml:"type"`
	Extensions   []string `mapstructure:"extensions" json:"extensions" yaml:"extensions"`
	FilePatterns []string `mapstructure:"filePatterns" json:"filePatterns" yaml:"filePatterns"`
}

type ScannerConfig struct {
	Mode              string            `mapstructure:"mode" json:"mode" yaml:"mode"`
	ScanPath          string            `mapstructure:"scanPath" json:"scanPath" yaml:"scanPath"`
	StagingPath       string            `mapstructure:"stagingPath" json:"stagingPath" yaml:"stagingPath"`
	FinalLibraryPath  string            `mapstructure:"finalLibraryPath" json:"finalLibraryPath" yaml:"finalLibraryPath"`
	BackupPath        string            `mapstructure:"backupPath" json:"backupPath" yaml:"backupPath"`
	QuarantinePath    string            `mapstructure:"quarantinePath" json:"quarantinePath" yaml:"quarantinePath"`
	CorruptionLogPath string            `mapstructure:"corruptionLogPath" json:"corruptionLogPath" yaml:"corruptionLogPath"`
	DuplicatesDir     string            `mapstructure:"duplicatesDir" json:"duplicatesDir" yaml:"duplicatesDir"`
	WorkerCount       int               `mapstructure:"workerCount" json:"workerCount" yaml:"workerCount"`
	BatchSize         int               `mapstructure:"batchSize" json:"batchSize" yaml:"batchSize"`
	IOThrottleMs      int               `mapstructure:"ioThrottleMs" json:"ioThrottleMs" yaml:"ioThrottleMs"`
	MaintenanceWindow string            `mapstructure:"maintenanceWindow" json:"maintenanceWindow" yaml:"maintenanceWindow"`
	MaxFilesPerDir    int               `mapstructure:"maxFilesPerDir" json:"maxFilesPerDir" yaml:"maxFilesPerDir"`
	FollowSymlinks    bool              `mapstructure:"followSymlinks" json:"followSymlinks" yaml:"followSymlinks"`
	FilePatterns      []string          `mapstructure:"filePatterns" json:"filePatterns" yaml:"filePatterns"`
	MediaTypes        []MediaTypeConfig `mapstructure:"mediaTypes" json:"mediaTypes" yaml:"mediaTypes"`
	SeriesGroupRules  []SeriesGroupRule `mapstructure:"seriesGroupPatterns" json:"seriesGroupPatterns" yaml:"seriesGroupPatterns"`
}

type SecurityConfig struct {
	Enabled              bool     `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
	StorePath            string   `mapstructure:"storePath" json:"storePath" yaml:"storePath"`
	DefaultPairingTTL    string   `mapstructure:"defaultPairingTTL" json:"defaultPairingTTL" yaml:"defaultPairingTTL"`
	AllowLocalAdmin      bool     `mapstructure:"allowLocalAdmin" json:"allowLocalAdmin" yaml:"allowLocalAdmin"`
	CORSAllowedOrigins   []string `mapstructure:"corsAllowedOrigins" json:"corsAllowedOrigins" yaml:"corsAllowedOrigins"`
	RequireViewerForRead bool     `mapstructure:"requireViewerForRead" json:"requireViewerForRead" yaml:"requireViewerForRead"`
}

type SchedulerConfig struct {
	Enabled      bool   `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
	Interval     string `mapstructure:"interval" json:"interval" yaml:"interval"`
	Mode         string `mapstructure:"mode" json:"mode" yaml:"mode"`
	RunOnStartup bool   `mapstructure:"runOnStartup" json:"runOnStartup" yaml:"runOnStartup"`
}

type RunRetentionConfig struct {
	MaxRuns    int `mapstructure:"maxRuns" json:"maxRuns" yaml:"maxRuns"`
	MaxAgeDays int `mapstructure:"maxAgeDays" json:"maxAgeDays" yaml:"maxAgeDays"`
}

type ServerConfig struct {
	Port             string        `mapstructure:"port" json:"port" yaml:"port"`
	Timeout          time.Duration `mapstructure:"timeout" json:"timeout" yaml:"timeout"`
	MaintenanceToken string        `mapstructure:"maintenanceToken" json:"maintenanceToken" yaml:"maintenanceToken"`
}

func (s ServerConfig) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Port             string `json:"port"`
		Timeout          string `json:"timeout"`
		MaintenanceToken string `json:"maintenanceToken,omitempty"`
	}{
		Port:             s.Port,
		Timeout:          s.Timeout.String(),
		MaintenanceToken: s.MaintenanceToken,
	})
}

func (s *ServerConfig) UnmarshalJSON(data []byte) error {
	var wire struct {
		Port             string          `json:"port"`
		Timeout          json.RawMessage `json:"timeout"`
		MaintenanceToken string          `json:"maintenanceToken"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}

	s.Port = wire.Port
	s.MaintenanceToken = wire.MaintenanceToken
	if len(wire.Timeout) == 0 || string(wire.Timeout) == "null" {
		return nil
	}

	var timeoutStr string
	if err := json.Unmarshal(wire.Timeout, &timeoutStr); err == nil {
		timeout, parseErr := time.ParseDuration(timeoutStr)
		if parseErr != nil {
			return fmt.Errorf("invalid server.timeout %q: %w", timeoutStr, parseErr)
		}
		s.Timeout = timeout
		return nil
	}

	var timeoutNanos int64
	if err := json.Unmarshal(wire.Timeout, &timeoutNanos); err == nil {
		s.Timeout = time.Duration(timeoutNanos)
		return nil
	}

	return fmt.Errorf("invalid server.timeout: %s", strconv.Quote(string(wire.Timeout)))
}

func (s ServerConfig) MarshalYAML() (interface{}, error) {
	return struct {
		Port             string `yaml:"port"`
		Timeout          string `yaml:"timeout"`
		MaintenanceToken string `yaml:"maintenanceToken,omitempty"`
	}{
		Port:             s.Port,
		Timeout:          s.Timeout.String(),
		MaintenanceToken: s.MaintenanceToken,
	}, nil
}

type DatabaseConfig struct {
	URI  string `mapstructure:"uri" json:"uri" yaml:"uri"`
	Name string `mapstructure:"name" json:"name" yaml:"name"`
}

type LoggerConfig struct {
	Level  string `mapstructure:"level" json:"level" yaml:"level"`
	Format string `mapstructure:"format" json:"format" yaml:"format"`
	Path   string `mapstructure:"path" json:"path" yaml:"path"`
}

type Config struct {
	Server       ServerConfig       `mapstructure:"server" json:"server" yaml:"server"`
	Security     SecurityConfig     `mapstructure:"security" json:"security" yaml:"security"`
	Scheduler    SchedulerConfig    `mapstructure:"scheduler" json:"scheduler" yaml:"scheduler"`
	RunRetention RunRetentionConfig `mapstructure:"runRetention" json:"runRetention" yaml:"runRetention"`
	Database     DatabaseConfig     `mapstructure:"database" json:"database" yaml:"database"`
	Logger       LoggerConfig       `mapstructure:"logger" json:"logger" yaml:"logger"`
	Scanner      ScannerConfig      `mapstructure:"scanner" json:"scanner" yaml:"scanner"`
}

var C *Config

func LoadConfig(path string) (err error) {
	if err = loadOptionalEnvFile(); err != nil {
		return err
	}

	v := viper.New()
	v.AddConfigPath(path)
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AutomaticEnv()

	if err = v.ReadInConfig(); err != nil {
		return
	}

	if err = v.Unmarshal(&C); err != nil {
		return
	}
	applyEnvOverrides(C)
	return ValidateConfig(*C)
}

func loadOptionalEnvFile() error {
	envFile := strings.TrimSpace(os.Getenv("PIC_MANAGER_ENV_FILE"))
	explicitPath := envFile != ""
	if envFile == "" {
		envFile = "/home/darkman/dev/mongodb/config/.env"
	}
	file, err := os.Open(envFile)
	if err != nil {
		if explicitPath {
			return fmt.Errorf("无法读取环境变量文件 %s: %w", envFile, err)
		}
		return nil
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key == "" || os.Getenv(key) != "" {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("设置环境变量 %s 失败: %w", key, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取环境变量文件 %s 失败: %w", envFile, err)
	}
	return nil
}

func applyEnvOverrides(cfg *Config) {
	if cfg == nil {
		return
	}
	if port := firstEnv("SERVER_PORT", "PIC_MANAGER_SERVER_PORT"); port != "" {
		cfg.Server.Port = port
	}
	if token := firstEnv("MAINTENANCE_TOKEN", "PIC_MANAGER_MAINTENANCE_TOKEN"); token != "" {
		cfg.Server.MaintenanceToken = token
	}
	if uri := firstEnv("DATABASE_URI", "MONGO_URI"); uri != "" {
		cfg.Database.URI = uri
	}
	if dbName := firstEnv("DATABASE_NAME", "MONGO_DATABASE"); dbName != "" {
		cfg.Database.Name = dbName
	}

	username := os.Getenv("MONGO_APP_USERNAME")
	password := os.Getenv("MONGO_APP_PASSWORD")
	if username == "" || password == "" {
		return
	}
	cfg.Database.URI = withMongoCredentials(
		cfg.Database.URI,
		username,
		password,
		firstEnv("MONGO_APP_AUTH_SOURCE", "MONGO_AUTH_SOURCE"),
	)
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func SecurityStorePath(cfg *Config) string {
	if cfg == nil {
		return ""
	}
	if path := strings.TrimSpace(cfg.Security.StorePath); path != "" {
		return path
	}
	logRoot := strings.TrimSpace(cfg.Logger.Path)
	if logRoot == "" {
		logRoot = "."
	}
	return logRoot + string(os.PathSeparator) + "auth" + string(os.PathSeparator) + "devices.json"
}

func withMongoCredentials(rawURI, username, password, authSource string) string {
	if strings.TrimSpace(rawURI) == "" {
		rawURI = "mongodb://localhost:27017"
	}
	parsed, err := url.Parse(rawURI)
	if err != nil {
		return rawURI
	}
	parsed.User = url.UserPassword(username, password)
	if strings.TrimSpace(authSource) == "" {
		authSource = "admin"
	}
	query := parsed.Query()
	if query.Get("authSource") == "" {
		query.Set("authSource", authSource)
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func ValidateConfig(cfg Config) error {
	loggerFormat := strings.ToLower(strings.TrimSpace(cfg.Logger.Format))
	if loggerFormat != "" && loggerFormat != "text" && loggerFormat != "json" {
		return fmt.Errorf("logger.format must be text or json, got %q", cfg.Logger.Format)
	}

	mode := strings.TrimSpace(cfg.Scanner.Mode)
	if mode != "" && mode != "full" && mode != "classifyOnly" {
		return fmt.Errorf("scanner.mode must be full or classifyOnly, got %q", cfg.Scanner.Mode)
	}
	if cfg.Scanner.WorkerCount < 0 {
		return fmt.Errorf("scanner.workerCount must be >= 0, got %d", cfg.Scanner.WorkerCount)
	}
	if cfg.Scanner.BatchSize < 0 {
		return fmt.Errorf("scanner.batchSize must be >= 0, got %d", cfg.Scanner.BatchSize)
	}
	if cfg.Scanner.IOThrottleMs < 0 {
		return fmt.Errorf("scanner.ioThrottleMs must be >= 0, got %d", cfg.Scanner.IOThrottleMs)
	}
	if cfg.Scanner.MaxFilesPerDir < 0 {
		return fmt.Errorf("scanner.maxFilesPerDir must be >= 0, got %d", cfg.Scanner.MaxFilesPerDir)
	}
	if cfg.RunRetention.MaxRuns < 0 {
		return fmt.Errorf("runRetention.maxRuns must be >= 0, got %d", cfg.RunRetention.MaxRuns)
	}
	if cfg.RunRetention.MaxAgeDays < 0 {
		return fmt.Errorf("runRetention.maxAgeDays must be >= 0, got %d", cfg.RunRetention.MaxAgeDays)
	}
	if strings.TrimSpace(cfg.Scanner.MaintenanceWindow) != "" {
		if err := validateMaintenanceWindow(cfg.Scanner.MaintenanceWindow); err != nil {
			return err
		}
	}
	if strings.TrimSpace(cfg.Security.DefaultPairingTTL) != "" {
		if _, err := time.ParseDuration(cfg.Security.DefaultPairingTTL); err != nil {
			return fmt.Errorf("security.defaultPairingTTL must be a duration, got %q: %w", cfg.Security.DefaultPairingTTL, err)
		}
	}
	if strings.TrimSpace(cfg.Scheduler.Interval) != "" {
		if _, err := time.ParseDuration(cfg.Scheduler.Interval); err != nil {
			return fmt.Errorf("scheduler.interval must be a duration, got %q: %w", cfg.Scheduler.Interval, err)
		}
	}
	schedulerMode := strings.TrimSpace(cfg.Scheduler.Mode)
	if schedulerMode != "" && schedulerMode != "full" && schedulerMode != "classifyOnly" {
		return fmt.Errorf("scheduler.mode must be full or classifyOnly, got %q", cfg.Scheduler.Mode)
	}
	for _, pattern := range cfg.Scanner.FilePatterns {
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("scanner.filePatterns contains invalid regex %q: %w", pattern, err)
		}
	}
	for i, mediaType := range cfg.Scanner.MediaTypes {
		typ := strings.TrimSpace(mediaType.Type)
		if typ == "" {
			return fmt.Errorf("scanner.mediaTypes[%d].type is required", i)
		}
		if len(mediaType.Extensions) == 0 {
			return fmt.Errorf("scanner.mediaTypes[%d].extensions must not be empty", i)
		}
		for _, ext := range mediaType.Extensions {
			if strings.TrimSpace(ext) == "" {
				return fmt.Errorf("scanner.mediaTypes[%d].extensions contains an empty extension", i)
			}
		}
		patterns := mediaType.FilePatterns
		if strings.EqualFold(typ, "image") && len(patterns) == 0 {
			patterns = cfg.Scanner.FilePatterns
		}
		if len(patterns) == 0 {
			return fmt.Errorf("scanner.mediaTypes[%d].filePatterns must not be empty", i)
		}
		for _, pattern := range patterns {
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("scanner.mediaTypes[%d].filePatterns contains invalid regex %q: %w", i, pattern, err)
			}
		}
	}
	if len(cfg.Scanner.MediaTypes) == 0 && len(cfg.Scanner.FilePatterns) == 0 {
		return fmt.Errorf("scanner.filePatterns or scanner.mediaTypes must be configured")
	}
	for i, rule := range cfg.Scanner.SeriesGroupRules {
		if strings.TrimSpace(rule.Pattern) == "" {
			return fmt.Errorf("scanner.seriesGroupPatterns[%d].pattern is required", i)
		}
		if _, err := regexp.Compile(rule.Pattern); err != nil {
			return fmt.Errorf("scanner.seriesGroupPatterns[%d].pattern contains invalid regex %q: %w", i, rule.Pattern, err)
		}
	}
	return nil
}

func validateMaintenanceWindow(window string) error {
	parts := strings.Split(strings.TrimSpace(window), "-")
	if len(parts) != 2 {
		return fmt.Errorf("scanner.maintenanceWindow must use HH:MM-HH:MM, got %q", window)
	}
	for _, part := range parts {
		if _, err := time.Parse("15:04", strings.TrimSpace(part)); err != nil {
			return fmt.Errorf("scanner.maintenanceWindow must use HH:MM-HH:MM, got %q: %w", window, err)
		}
	}
	return nil
}
