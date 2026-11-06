module github.com/BrunoKrugel/zaptail/example

go 1.25.4

replace github.com/BrunoKrugel/zaptail => ../

require (
	github.com/BrunoKrugel/zaptail v0.0.0-00010101000000-000000000000
	github.com/joho/godotenv v1.5.1
	go.uber.org/zap v1.27.1
)

require go.uber.org/multierr v1.11.0 // indirect
