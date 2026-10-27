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

.PHONY: generate test test-integration test-race cover loadtest

# Regenerate the Go code for every service's SQL queries, and for our
# protobuf contracts, as long as they pass buf's lint rules.
generate:
	@sqlc generate
	@buf lint
	@buf generate

# Run part of the suite with PKGS=./internal/wallet TESTFLAGS='-run Transfer'.
PKGS ?= ./...

# Unit tests: seconds, and no Docker. Integration tests skip with -short.
test:
	@go test -short -count=1 $(TESTFLAGS) $(PKGS)

# Every test, on throwaway Postgres, NATS and Redis containers. Four packages
# at a time: each starts its own, and Docker shouldn't start a dozen at once.
test-integration:
	@go test -count=1 -p 4 $(TESTFLAGS) $(PKGS)

# Every test, under the race detector. CI runs this one.
test-race:
	@go test -race -count=1 -p 4 $(TESTFLAGS) $(PKGS)

# Coverage of every package that has tests, then in total. For a closer look:
# go tool cover -html=cover.out
TESTED = $(shell go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./...)
cover:
	@go test -count=1 -p 4 -coverprofile=cover.out $(TESTFLAGS) $(TESTED)
	@go tool cover -func=cover.out | tail -1

# A load test of transfers through the gateway, with every service running.
# k6 runs from its official image: there's nothing to install.
loadtest:
	@docker run --rm -t --add-host=host.docker.internal:host-gateway -v ./loadtest:/loadtest \
		-e BASE_URL=http://host.docker.internal:8080 \
		grafana/k6:2.4.0 run /loadtest/transfers.js

.PHONY: run-wallet run-auth run-transfer run-notification dead-letters replay-dead-letters run-gateway keys reconciliation

# Run the wallet service against the local database, stamped with a git version.
# Extra flags for a service go in ARGS: make run-wallet ARGS=-migrate
VERSION ?= $(shell git describe --always --dirty)

# Every service sends its traces to Jaeger, over OTLP. Tests don't: without
# the variable, tracing is off.
run-%: export OTEL_EXPORTER_OTLP_ENDPOINT = http://localhost:4318

run-wallet:
	@go build -ldflags "-X main.version=$(VERSION)" -o bin/wallet ./cmd/wallet
	@WALLET_DATABASE_URL="$(DB_URL)" \
		WALLET_AUTH_PUBLIC_KEY_FILE=secrets/auth_ed25519.pub.pem ./bin/wallet $(ARGS)

run-auth:
	@go build -ldflags "-X main.version=$(VERSION)" -o bin/auth ./cmd/auth
	@AUTH_DATABASE_URL="postgres://payments:payments@localhost:5432/auth?sslmode=disable" \
		AUTH_PRIVATE_KEY_FILE=secrets/auth_ed25519.pem ./bin/auth $(ARGS)

run-transfer:
	@go build -ldflags "-X main.version=$(VERSION)" -o bin/transfer ./cmd/transfer
	@TRANSFER_DATABASE_URL="postgres://payments:payments@localhost:5432/transfer?sslmode=disable" \
		TRANSFER_AUTH_PUBLIC_KEY_FILE=secrets/auth_ed25519.pub.pem ./bin/transfer $(ARGS)

NOTIFICATION_DB_URL := postgres://payments:payments@localhost:5432/notification?sslmode=disable

run-notification:
	@go build -ldflags "-X main.version=$(VERSION)" -o bin/notification ./cmd/notification
	@NOTIFICATION_DATABASE_URL="$(NOTIFICATION_DB_URL)" ./bin/notification $(ARGS)

# The messages the notification service gave up on, and why.
dead-letters:
	@docker compose exec -T postgres psql -U payments -d notification -c \
		"SELECT stream, seq, deliveries, left(error, 70) AS error FROM dead_letters \
		WHERE replayed_at IS NULL"

# Once whatever broke them is fixed, send them again.
replay-dead-letters:
	@go build -ldflags "-X main.version=$(VERSION)" -o bin/notification ./cmd/notification
	@NOTIFICATION_DATABASE_URL="$(NOTIFICATION_DB_URL)" ./bin/notification -replay-dead-letters

# What the last reconciliation found: nothing at all, or exactly what's off.
reconciliation:
	@docker compose exec -T postgres psql -U payments -d wallet -c \
		"SELECT d.* FROM job_runs, jsonb_to_recordset(summary->'discrepancies') \
		AS d(\"check\" text, ref text, currency text, expected bigint, actual bigint) \
		WHERE id = (SELECT max(id) FROM job_runs WHERE job = 'reconcile' AND status = 'succeeded')"

# The public API on port 8080, in front of the others.
run-gateway:
	@go build -ldflags "-X main.version=$(VERSION)" -o bin/gateway ./cmd/gateway
	@GATEWAY_AUTH_PUBLIC_KEY_FILE=secrets/auth_ed25519.pub.pem ./bin/gateway $(ARGS)

.PHONY: run-webhook

# Webhooks, on port 8085: our users' servers hear about their transfers.
run-webhook:
	@go build -ldflags "-X main.version=$(VERSION)" -o bin/webhook ./cmd/webhook
	@WEBHOOK_DATABASE_URL="postgres://payments:payments@localhost:5432/webhook?sslmode=disable" \
		WEBHOOK_AUTH_PUBLIC_KEY_FILE=secrets/auth_ed25519.pub.pem ./bin/webhook $(ARGS)

# A new Ed25519 key pair for signing access tokens, readable only by you. The
# auth service gets the private key, every other service the public one.
keys:
	@mkdir -p secrets
	@test ! -e secrets/auth_ed25519.pem || (echo "secrets/auth_ed25519.pem exists, keeping it" && exit 1)
	@umask 077 && openssl genpkey -algorithm ed25519 -out secrets/auth_ed25519.pem
	@openssl pkey -in secrets/auth_ed25519.pem -pubout -out secrets/auth_ed25519.pub.pem
	@ls -l secrets

# Certificates for mTLS between our services, from a new development CA: a
# directory each, in secrets/tls, for a week. To renew them, run it again,
# and restart the services.
.PHONY: certs
certs:
	@go run ./cmd/devca -dir secrets/tls gateway wallet auth transfer notification ops
