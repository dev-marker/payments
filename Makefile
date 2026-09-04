# Database migrations. Pick another service with SERVICE=auth, for example.
SERVICE ?= wallet
DB_URL  ?= postgres://payments:payments@localhost:5432/$(SERVICE)?sslmode=disable
MIGRATE := migrate -path db/$(SERVICE)/migrations -database "$(DB_URL)"

.PHONY: migrate-up migrate-down migrate-version migrate-new

migrate-up:
	@$(MIGRATE) up

migrate-down:
	@$(MIGRATE) down 1

migrate-version:
	@$(MIGRATE) version

migrate-new:
	@test -n "$(name)" || (echo "usage: make migrate-new name=add_something" && exit 1)
	migrate create -ext sql -dir db/$(SERVICE)/migrations -seq $(name)

.PHONY: generate test

# Regenerate the Go code for every service's SQL queries.
generate:
	@sqlc generate

# Integration tests use the local Docker Postgres. Without this URL they skip.
WALLET_TEST_DATABASE_URL ?= postgres://payments:payments@localhost:5432/wallet?sslmode=disable
export WALLET_TEST_DATABASE_URL

# Run part of the suite with PKGS=./internal/wallet TESTFLAGS='-run Transfer'.
PKGS ?= ./...

test:
	@go test -count=1 $(TESTFLAGS) $(PKGS)

.PHONY: run-wallet

# Run the wallet service against the local database, stamped with a git version.
VERSION ?= $(shell git describe --always --dirty)

run-wallet:
	@go build -ldflags "-X main.version=$(VERSION)" -o bin/wallet ./cmd/wallet
	@WALLET_DATABASE_URL="$(DB_URL)" ./bin/wallet
