package logger

import (
	"PICs_Manager/config"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
)

// InitLogger 根据 config.yaml 中的配置初始化一个全局的 slog 日志记录器。
func InitLogger() error {
	var logHandler slog.Handler

	// 从配置中获取日志级别
	logLevel := new(slog.LevelVar)
	if err := setLogLevel(config.C.Logger.Level, logLevel); err != nil {
		return err
	}

	handlerOpts := &slog.HandlerOptions{
		Level: logLevel,
		// AddSource: true, // 如果需要输出源码位置（文件名和行号），取消此行注释
	}

	// 根据配置选择日志格式 (text 或 json)
	if config.C.Logger.Format == "json" {
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
	switch levelStr {
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

// CtxWithLogger 将一个带有特定字段的 logger 附加到 context 中。
// 这对于在请求处理链中传递带有请求ID等信息的 logger 非常有用。
func CtxWithLogger(ctx context.Context, attrs ...slog.Attr) context.Context {
	// 这个函数暂时作为高级用法的占位符，我们初期可能用不到。
	// 它展示了如何扩展日志功能以适应更复杂的微服务场景。
	return ctx
}

// Discard 返回一个丢弃所有日志的 logger，主要用于测试，避免不必要的日志输出。
func Discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
