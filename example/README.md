# Zaptail Example

This example demonstrates how to use Zaptail to send logs to BetterStack Logtail.

## Setup

1. Get your API key from [BetterStack Logtail](https://betterstack.com/logtail)

2. Set the environment variable:
```bash
export LOGTAIL_API_KEY="your-api-key-here"
```

3. Run the example:
```bash
cd example
go run main.go
```

## What the example does

- Sets up a logger that sends logs to both console and Logtail
- Demonstrates different log levels (Debug, Info, Warn, Error)
- Shows how to add structured fields to logs
- Demonstrates batching with multiple log entries
- Shows proper cleanup with deferred Close() and Sync()

## Expected output

You'll see logs printed to the console, and they will also be sent to your Logtail dashboard (except Debug level logs, which only go to console).
