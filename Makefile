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

# Regenerate the Go code for every service's SQL queries, and for our
# protobuf contracts, as long as they pass buf's lint rules.
generate:
	@sqlc generate
	@buf lint
	@buf generate

# Integration tests use the local Docker Postgres. Without this URL they skip.
WALLET_TEST_DATABASE_URL ?= postgres://payments:payments@localhost:5432/wallet?sslmode=disable
AUTH_TEST_DATABASE_URL ?= postgres://payments:payments@localhost:5432/auth?sslmode=disable
TRANSFER_TEST_DATABASE_URL ?= postgres://payments:payments@localhost:5432/transfer?sslmode=disable
NOTIFICATION_TEST_DATABASE_URL ?= postgres://payments:payments@localhost:5432/notification?sslmode=disable
# Redis database 1, so tests never touch the services' data in database 0.
REDIS_TEST_URL ?= redis://localhost:6379/1
# Tests create streams of their own, with random names, and delete them.
NATS_TEST_URL ?= nats://localhost:4222
export WALLET_TEST_DATABASE_URL AUTH_TEST_DATABASE_URL TRANSFER_TEST_DATABASE_URL NOTIFICATION_TEST_DATABASE_URL REDIS_TEST_URL NATS_TEST_URL

# Run part of the suite with PKGS=./internal/wallet TESTFLAGS='-run Transfer'.
PKGS ?= ./...

test:
	@go test -count=1 $(TESTFLAGS) $(PKGS)

.PHONY: run-wallet run-auth run-transfer run-notification dead-letters replay-dead-letters run-gateway keys

# Run the wallet service against the local database, stamped with a git version.
VERSION ?= $(shell git describe --always --dirty)

run-wallet:
	@go build -ldflags "-X main.version=$(VERSION)" -o bin/wallet ./cmd/wallet
	@WALLET_DATABASE_URL="$(DB_URL)" \
		WALLET_AUTH_PUBLIC_KEY_FILE=secrets/auth_ed25519.pub.pem ./bin/wallet

run-auth:
	@go build -ldflags "-X main.version=$(VERSION)" -o bin/auth ./cmd/auth
	@AUTH_DATABASE_URL="postgres://payments:payments@localhost:5432/auth?sslmode=disable" \
		AUTH_PRIVATE_KEY_FILE=secrets/auth_ed25519.pem ./bin/auth

run-transfer:
	@go build -ldflags "-X main.version=$(VERSION)" -o bin/transfer ./cmd/transfer
	@TRANSFER_DATABASE_URL="postgres://payments:payments@localhost:5432/transfer?sslmode=disable" \
		TRANSFER_AUTH_PUBLIC_KEY_FILE=secrets/auth_ed25519.pub.pem ./bin/transfer

NOTIFICATION_DB_URL := postgres://payments:payments@localhost:5432/notification?sslmode=disable

run-notification:
	@go build -ldflags "-X main.version=$(VERSION)" -o bin/notification ./cmd/notification
	@NOTIFICATION_DATABASE_URL="$(NOTIFICATION_DB_URL)" ./bin/notification

# The messages the notification service gave up on, and why.
dead-letters:
	@docker compose exec -T postgres psql -U payments -d notification -c \
		"SELECT stream, seq, deliveries, left(error, 70) AS error FROM dead_letters \
		WHERE replayed_at IS NULL"

# Once whatever broke them is fixed, send them again.
replay-dead-letters:
	@go build -ldflags "-X main.version=$(VERSION)" -o bin/notification ./cmd/notification
	@NOTIFICATION_DATABASE_URL="$(NOTIFICATION_DB_URL)" ./bin/notification -replay-dead-letters

# The public API on port 8080, in front of the others.
run-gateway:
	@go build -ldflags "-X main.version=$(VERSION)" -o bin/gateway ./cmd/gateway
	@GATEWAY_AUTH_PUBLIC_KEY_FILE=secrets/auth_ed25519.pub.pem ./bin/gateway

# A new Ed25519 key pair for signing access tokens, readable only by you. The
# auth service gets the private key, every other service the public one.
keys:
	@mkdir -p secrets
	@test ! -e secrets/auth_ed25519.pem || (echo "secrets/auth_ed25519.pem exists, keeping it" && exit 1)
	@umask 077 && openssl genpkey -algorithm ed25519 -out secrets/auth_ed25519.pem
	@openssl pkey -in secrets/auth_ed25519.pem -pubout -out secrets/auth_ed25519.pub.pem
	@ls -l secrets
