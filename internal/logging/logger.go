// Package logger defines the config for logging across the system
package logger

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var Log *zap.Logger

// Init initializes the global logger.
func Init(level zapcore.Level, destination *string) error {
	var err error
	cfg := zap.NewDevelopmentConfig()

	// ensures logs are outputted as JSON
	cfg.Encoding = "json"

	// ensures log are at level specified by level arg
	cfg.Level = zap.NewAtomicLevelAt(level)

	// this ensures that the field names like timestamp, level etc. will match the production style as well
	cfg.EncoderConfig = zap.NewProductionEncoderConfig()

	// If a log path is provided use it
	if destination != nil {
		cfg.OutputPaths = []string{*destination}
	}

	// build the logger
	Log, err = cfg.Build()
	return err
}
