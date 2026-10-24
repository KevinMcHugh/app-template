# See README.md. Code generators and dbmate run with `go run …@version`, so
# their dependency trees stay out of go.mod.
.PHONY: dev generate migrate rollback new-migration key invite build test check help

# Generators run on the toolchain go.mod names, never whichever one go run
# picks: oapi-codegen embeds the spec gzipped, and gzip output differs
# between Go versions, so a different toolchain fails `make check` in CI.
GO_VERSION := $(shell sed -n 's/^go //p' go.mod)
GOTOOL     = GOTOOLCHAIN=go$(GO_VERSION)

SQLC   = $(GOTOOL) go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
OAPI   = $(GOTOOL) go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0
DBMATE = go run github.com/amacneil/dbmate/v2@v2.28.0 --migrations-dir db/migrations --no-dump-schema

dev: migrate ## Migrate, then run the server on :8080 (needs DATABASE_URL)
	go run ./cmd/server serve

generate: ## Regenerate internal/db/gen (sqlc) and internal/api/gen (oapi-codegen) after any SQL, migration or spec change
	# sqlc does not remove output for a deleted query file, so start clean.
	rm -f internal/db/gen/*.go
	$(SQLC) generate
	$(OAPI) --config oapi-codegen.yaml api/openapi.yaml

migrate: ## Apply pending migrations (needs DATABASE_URL)
	$(DBMATE) up

rollback: ## Roll back the last migration (needs DATABASE_URL)
	$(DBMATE) rollback

new-migration: ## Create a migration file (usage: make new-migration NAME=create_widgets)
	@if [ -z "$(NAME)" ]; then echo "usage: make new-migration NAME=<description>"; exit 1; fi
	$(DBMATE) new "$(NAME)"

key: ## Mint an api key (usage: make key NAME=kev)
	@if [ -z "$(NAME)" ]; then echo "usage: make key NAME=<label>"; exit 1; fi
	go run ./cmd/server keys create --name "$(NAME)"

invite: ## Mint a single-use invite for a newcomer (usage: make invite NOTE=ana)
	go run ./cmd/server invites create --note "$(NOTE)"

build: ## Build the binary to bin/server
	go build -o bin/server ./cmd/server

test: ## Run the tests; set TEST_DATABASE_URL to include the Postgres ones
	go test -count=1 ./...

check: ## Fail if generated code is stale (what CI runs)
	$(MAKE) generate
	git diff --exit-code -- internal/db/gen internal/api/gen

.DEFAULT_GOAL := help
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-14s %s\n", $$1, $$2}'
