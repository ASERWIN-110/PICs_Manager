package logger

import (
	"PICs_Manager/config"
	"errors"
	"log/slog"
	"os"
	"strings"
)

// InitLogger 根据 config.yaml 中的配置初始化一个全局的 slog 日志记录器。
func InitLogger() error {
	if config.C == nil {
		return errors.New("配置未初始化")
	}
	var logHandler slog.Handler

	// 从配置中获取日志级别
	logLevel := new(slog.LevelVar)
	if err := setLogLevel(config.C.Logger.Level, logLevel); err != nil {
		return err
	}

	handlerOpts := &slog.HandlerOptions{
		Level: logLevel,
	}

	if strings.EqualFold(strings.TrimSpace(config.C.Logger.Format), "json") {
		logHandler = slog.NewJSONHandler(os.Stdout, handlerOpts)
	} else {
		logHandler = slog.NewTextHandler(os.Stdout, handlerOpts)
	}

	// 创建一个新的 Logger 并设置为默认
	logger := slog.New(logHandler)
	slog.SetDefault(logger)

	return nil
}

// setLogLevel 将字符串形式的日志级别转换为 slog.Level 类型
func setLogLevel(levelStr string, levelVar *slog.LevelVar) error {
	switch strings.ToLower(strings.TrimSpace(levelStr)) {
	case "":
		levelVar.Set(slog.LevelInfo)
	case "debug":
		levelVar.Set(slog.LevelDebug)
	case "info":
		levelVar.Set(slog.LevelInfo)
	case "warn":
		levelVar.Set(slog.LevelWarn)
	case "error":
		levelVar.Set(slog.LevelError)
	default:
		return errors.New("无效的日志级别: " + levelStr)
	}
	return nil
}
