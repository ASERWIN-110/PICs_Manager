package config

import (
	"github.com/spf13/viper"
	"time"
)

type SeriesGroupRule struct {
	Name    string `mapstructure:"name"`
	Pattern string `mapstructure:"pattern"`
}

type ScannerConfig struct {
	ScanPath          string            `mapstructure:"scanPath"`
	StagingPath       string            `mapstructure:"stagingPath"`
	FinalLibraryPath  string            `mapstructure:"finalLibraryPath"`
	BackupPath        string            `mapstructure:"backupPath"`
	QuarantinePath    string            `mapstructure:"quarantinePath"`
	CorruptionLogPath string            `mapstructure:"corruptionLogPath"`
	DuplicatesDir     string            `mapstructure:"duplicatesDir"`
	WorkerCount       int               `mapstructure:"workerCount"`
	BatchSize         int               `mapstructure:"batchSize"`
	FilePatterns      []string          `mapstructure:"filePatterns"`
	SeriesGroupRules  []SeriesGroupRule `mapstructure:"seriesGroupPatterns"`
}

type Config struct {
	Server struct {
		Port    string        `mapstructure:"port"`
		Timeout time.Duration `mapstructure:"timeout"`
	} `mapstructure:"server"`

	Database struct {
		URI  string `mapstructure:"uri"`
		Name string `mapstructure:"name"`
	} `mapstructure:"database"`

	Logger struct {
		Level  string `mapstructure:"level"`
		Format string `mapstructure:"format"`
		Path   string `mapstructure:"path"`
	} `mapstructure:"logger"`

	Scanner ScannerConfig `mapstructure:"scanner"`
}

var C *Config

// LoadConfig 函数保持不变
func LoadConfig(path string) (err error) {
	v := viper.New()
	v.AddConfigPath(path)
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AutomaticEnv()

	if err = v.ReadInConfig(); err != nil {
		return
	}

	err = v.Unmarshal(&C)
	return
}
