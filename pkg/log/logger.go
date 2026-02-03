package log

import (
	"strings"

	"go.uber.org/zap"
)

type Config struct {
	Level           string   `mapstructure:"level" yaml:"level"`
	Encoding        string   `mapstructure:"encoding" yaml:"encoding"`
	OutputPaths     []string `mapstructure:"output_paths" yaml:"output_paths"`
	ErrorOutputPaths []string `mapstructure:"error_output_paths" yaml:"error_output_paths"`
}

func Init(cfg Config) error {
	zcfg := zap.NewProductionConfig()
	zcfg.Encoding = "console"
	if cfg.Encoding != "" {
		zcfg.Encoding = cfg.Encoding
	}

	level := zap.InfoLevel
	if cfg.Level != "" {
		if err := level.Set(strings.ToLower(cfg.Level)); err != nil {
			return err
		}
	}
	zcfg.Level = zap.NewAtomicLevelAt(level)

	if len(cfg.OutputPaths) > 0 {
		zcfg.OutputPaths = cfg.OutputPaths
	}
	if len(cfg.ErrorOutputPaths) > 0 {
		zcfg.ErrorOutputPaths = cfg.ErrorOutputPaths
	}

	logger, err := zcfg.Build(zap.AddCaller())
	if err != nil {
		return err
	}
	zap.ReplaceGlobals(logger)
	return nil
}

func Sync() {
	_ = zap.L().Sync()
}

func Base() *zap.Logger {
	return zap.L()
}
