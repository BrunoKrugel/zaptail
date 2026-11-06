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
	"fmt"
	"time"

	"github.com/BrunoKrugel/zaptail"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func main() {
	encoderConfig := zap.NewProductionEncoderConfig()
	encoder := zapcore.NewJSONEncoder(encoderConfig)

	logtailCore, err := zaptail.NewCore(encoder, zapcore.InfoLevel, &zaptail.Config{
		APIKey:     "your-logtail-api-key",
		LogtailURL: "https://your-source.logtail.com",
	})
	if err != nil {
		panic(err)
	}
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
- `LogtailURL` (string, required): Your Logtail source URL
- `BatchSize` (int, optional): Number of logs to batch before sending (default: 100)
- `MaxBufferedLogs` (int, optional): Maximum logs held in memory while a batch is in flight; extra logs are dropped and reported (default: 10x `BatchSize`)
- `FlushInterval` (time.Duration, optional): Time interval to flush logs (default: 5 seconds)
- `HTTPClient` (*http.Client, optional): Custom HTTP client (default: 10-second timeout client)
- `Context` (context.Context, optional): Parent context for log delivery; cancelling it stops pending sends
- `ErrorOutput` (zapcore.WriteSyncer, optional): Where delivery failures and dropped-log reports are written

## Reliability

Log delivery never blocks your application: `Write` only buffers, and a background
worker sends batches. Failed sends are retried up to 3 times with exponential
backoff; if delivery still fails (or the buffer is full), the affected logs are
dropped and a report is written to `ErrorOutput` (when configured). `Sync` and
`Close` drain the buffer, and `Close` is safe to call multiple times.

## Combining with Console Output

You can combine Zaptail with console output using `zapcore.NewTee`:

```go
package main

import (
	"os"

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

	logtailCore, err := zaptail.NewCore(encoder, zapcore.InfoLevel, &zaptail.Config{
		APIKey:     "your-logtail-api-key",
		LogtailURL: "https://your-source.logtail.com",
	})
	if err != nil {
		panic(err)
	}
	defer logtailCore.Close()

	core := zapcore.NewTee(consoleCore, logtailCore)
	logger := zap.New(core)
	defer logger.Sync()

	logger.Info("This log goes to both console and Logtail")
}
```

## Features

- Non-blocking batched sending via a background worker
- Automatic periodic flushing
- Retries with exponential backoff and drop reporting
- Bounded in-memory buffer to protect the application under endpoint outages
- Thread-safe operations, including cores derived with `With`
- Graceful shutdown with an idempotent `Close()`

## License

MIT
