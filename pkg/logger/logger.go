package logger

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func New() (*zap.Logger, error) {
	var config zap.Config
	
	if os.Getenv("APP_ENV") == "production" {
		config = zap.NewProductionConfig()
		config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	} else {
		config = zap.NewDevelopmentConfig()
		config.EncoderConfig.EncodeLevel = zapcore.LowercaseColorLevelEncoder
		config.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout("15:04:05")
		config.EncoderConfig.EncodeCaller = zapcore.ShortCallerEncoder
		config.EncoderConfig.ConsoleSeparator = "|" 
		config.EncoderConfig.EncodeName = zapcore.FullNameEncoder
	}

	return config.Build()
}
