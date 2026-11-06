.PHONY: format lint lint-fix test tidy
format: ## Format code
	goimports -w .
	go fmt ./...
	fieldalignment -fix ./...

lint: ## Lint
	golangci-lint run

lint-fix: ## Lint and auto-fix
	golangci-lint run --fix

test: ## Run tests with the race detector
	go test -race -count=1 ./...
	cd example && go test -count=1 ./...

tidy: ## Tidy all modules
	go mod tidy
	cd example && go mod tidy
