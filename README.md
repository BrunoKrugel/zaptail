# Zaptail

A [Go](https://golang.org/) middleware for [Uber's Zap](https://github.com/uber-go/zap) logger that sends logs to [BetterStack Logtail](https://betterstack.com/logtail).

## Installation

```bash
go get github.com/BrunoKrugel/zaptail
```

## Usage

```go
package main

import (
	"time"

	"github.com/BrunoKrugel/zaptail"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func main() {
	encoderConfig := zap.NewProductionEncoderConfig()
	encoder := zapcore.NewJSONEncoder(encoderConfig)

	config := zaptail.Config{
		APIKey:        "your-logtail-api-key",
		BatchSize:     100,
		FlushInterval: 5 * time.Second,
	}

	logtailCore := zaptail.NewCore(encoder, zapcore.InfoLevel, config)
	defer logtailCore.Close()

	logger := zap.New(logtailCore)
	defer logger.Sync()

	logger.Info("Hello from Zaptail!",
		zap.String("environment", "production"),
		zap.Int("user_id", 12345),
	)

	logger.Error("An error occurred",
		zap.Error(fmt.Errorf("something went wrong")),
		zap.String("context", "user authentication"),
	)
}
```

## Configuration

The `Config` struct accepts the following parameters:

- `APIKey` (string, required): Your BetterStack Logtail API key
- `BatchSize` (int, optional): Number of logs to batch before sending (default: 100)
- `FlushInterval` (time.Duration, optional): Time interval to flush logs (default: 5 seconds)
- `HTTPClient` (*http.Client, optional): Custom HTTP client (default: 10-second timeout client)

## Combining with Console Output

You can combine Zaptail with console output using `zapcore.NewTee`:

```go
package main

import (
	"os"
	"time"

	"github.com/BrunoKrugel/zaptail"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func main() {
	encoderConfig := zap.NewProductionEncoderConfig()
	encoder := zapcore.NewJSONEncoder(encoderConfig)

	consoleCore := zapcore.NewCore(
		zapcore.NewConsoleEncoder(encoderConfig),
		zapcore.AddSync(os.Stdout),
		zapcore.DebugLevel,
	)

	logtailCore := zaptail.NewCore(encoder, zapcore.InfoLevel, zaptail.Config{
		APIKey: "your-logtail-api-key",
	})
	defer logtailCore.Close()

	core := zapcore.NewTee(consoleCore, logtailCore)
	logger := zap.New(core)
	defer logger.Sync()

	logger.Info("This log goes to both console and Logtail")
}
```

## Features

- Batched log sending for better performance
- Automatic periodic flushing
- Configurable batch size and flush interval
- Thread-safe operations
- Graceful shutdown with `Close()`

## License

MIT
