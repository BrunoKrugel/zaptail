package main

import (
	"fmt"
	"os"
	"time"

	"github.com/BrunoKrugel/zaptail"
	_ "github.com/joho/godotenv/autoload"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func main() {
	apiKey := os.Getenv("LOGTAIL_API_KEY")
	if apiKey == "" {
		fmt.Println("Please set LOGTAIL_API_KEY environment variable")
		os.Exit(1)
	}

	apiURL := os.Getenv("LOGTAIL_API_URL")
	if apiURL == "" {
		fmt.Println("Please set LOGTAIL_API_URL environment variable")
		os.Exit(1)
	}

	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.TimeKey = "timestamp"
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	encoder := zapcore.NewJSONEncoder(encoderConfig)

	consoleCore := zapcore.NewCore(
		zapcore.NewConsoleEncoder(encoderConfig),
		zapcore.AddSync(os.Stdout),
		zapcore.DebugLevel,
	)

	config := zaptail.Config{
		APIKey:        apiKey,
		LogtailURL:    apiURL,
		BatchSize:     10,
		FlushInterval: 3 * time.Second,
	}

	logtailCore := zaptail.NewCore(encoder, zapcore.InfoLevel, config)
	defer logtailCore.Close()

	core := zapcore.NewTee(consoleCore, logtailCore)
	logger := zap.New(core, zap.AddCaller())
	defer logger.Sync()

	logger.Info("Application started",
		zap.String("environment", "production"),
		zap.String("version", "1.0.0"),
	)

	logger.Debug("This is a debug message (only goes to console)")

	logger.Info("User logged in",
		zap.Int("user_id", 12345),
		zap.String("username", "john_doe"),
		zap.String("ip_address", "192.168.1.100"),
	)

	logger.Warn("High memory usage detected",
		zap.Float64("memory_usage_percent", 85.5),
		zap.String("service", "api-gateway"),
	)

	logger.Error("Database connection failed",
		zap.Error(fmt.Errorf("connection timeout")),
		zap.String("database", "users_db"),
		zap.Int("retry_count", 3),
	)

	for i := range 5 {
		logger.Info("Processing batch",
			zap.Int("batch_number", i+1),
			zap.Int("records", (i+1)*100),
		)
		time.Sleep(500 * time.Millisecond)
	}

	logger.Info("Application shutting down gracefully")

	fmt.Println("\nWaiting for logs to flush...")
	time.Sleep(1 * time.Second)
}
