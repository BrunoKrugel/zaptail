.PHONY: format
format: ## Format code
	goimports -w .
	go fmt ./...
	fieldalignment -fix ./...

.PHONY: lint
lint: ## Lint
	golangci-lint run --fix
