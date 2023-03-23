package controllers

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"os"
)

func NewLoggerCore() zapcore.Core {
	// info level enabler
	infoLevel := zap.LevelEnablerFunc(func(level zapcore.Level) bool {
		return level == zapcore.InfoLevel
	})

	// error and fatal level enabler
	errorFatalLevel := zap.LevelEnablerFunc(func(level zapcore.Level) bool {
		return level == zapcore.ErrorLevel || level == zapcore.FatalLevel
	})

	// write syncers
	stdoutSyncer := zapcore.Lock(os.Stdout)
	stderrSyncer := zapcore.Lock(os.Stderr)

	// tee core
	core := zapcore.NewTee(
		zapcore.NewCore(
			zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
			stdoutSyncer,
			infoLevel,
		),
		zapcore.NewCore(
			zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
			stderrSyncer,
			errorFatalLevel,
		),
	)

	return core
}
