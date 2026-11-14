# Payments backend in Go: microservices, built from scratch

The code from the YouTube course **Build a Production-Grade Payments Backend with Go Microservices**. Every line was typed on camera and every decision is explained in the videos.

- ▶ **Part 1** (episodes 1–14): _link coming soon_
- ▶ **Part 2** (episodes 15–24): _link coming soon_
- ▶ **Full course in chapters**, with your progress saved: https://www.fiston.dev/courses/build-a-production-grade-payments-backend-in-go

## Follow along: one branch per episode

Each episode ends with a commit, and each episode has its own branch holding the code **as it is at the end of that episode**. To start an episode where the video starts, check out the previous episode's branch:

```bash
git clone https://github.com/dev-marker/payments.git
cd payments
git checkout ep05   # the code at the end of episode 5, where episode 6 begins
```

`main` holds the latest episode plus this README.

The Go module is named `github.com/fiston-user/payments`, as typed in the videos. It's only a name: everything builds and runs from this clone as is.

| Branch | Episode | What we build |
|---|---|---|
| [`ep01`](https://github.com/dev-marker/payments/tree/ep01) | 1. Build a Production-Grade Payments Backend | Project setup and wallet service skeleton |
| [`ep02`](https://github.com/dev-marker/payments/tree/ep02) | 2. Designing a Double-Entry Ledger | Double-entry ledger schema and migrations |
| [`ep03`](https://github.com/dev-marker/payments/tree/ep03) | 3. Type-Safe SQL with sqlc + pgx | Sqlc queries, pgx pool and ledger store |
| [`ep04`](https://github.com/dev-marker/payments/tree/ep04) | 4. Config, Logging & Errors | Config, slog logging, request IDs, middleware and HTTP errors |
| [`ep05`](https://github.com/dev-marker/payments/tree/ep05) | 5. The Wallet API | Wallet API with deposits, validation and keyset pagination |
| [`ep06`](https://github.com/dev-marker/payments/tree/ep06) | 6. Moving Money Safely | Concurrency-safe transfers with row locks, lock ordering and retries |
| [`ep07`](https://github.com/dev-marker/payments/tree/ep07) | 7. Idempotency Keys | Idempotency keys for deposits and transfers, with a Redis fast path |
| [`ep08`](https://github.com/dev-marker/payments/tree/ep08) | 8. The Auth Service | Auth service with argon2id passwords, EdDSA access tokens and rotating refresh tokens |
| [`ep09`](https://github.com/dev-marker/payments/tree/ep09) | 9. gRPC Between Services | GRPC API with buf, interceptors, error codes and a client with deadlines |
| [`ep10`](https://github.com/dev-marker/payments/tree/ep10) | 10. The API Gateway | API gateway with JWT at the edge, Redis rate limits, CORS and timeouts; wallets owned by users |
| [`ep11`](https://github.com/dev-marker/payments/tree/ep11) | 11. Events with NATS JetStream | Events on NATS JetStream through a transactional outbox, with a relay and a durable consumer |
| [`ep12`](https://github.com/dev-marker/payments/tree/ep12) | 12. Transfer Service & Sagas | Transfer service running transfers as sagas, with holds in the wallet, retries and compensation |
| [`ep13`](https://github.com/dev-marker/payments/tree/ep13) | 13. Notification Service | Notification service with idempotent consumers, retries with backoff and a dead-letter queue |
| [`ep14`](https://github.com/dev-marker/payments/tree/ep14) | 14. Observability | OpenTelemetry tracing across HTTP, gRPC and NATS, Prometheus metrics and Grafana dashboards |
| [`ep15`](https://github.com/dev-marker/payments/tree/ep15) | 15. Testing Like a Pro | Integration tests on testcontainers, table tests, ledger invariants, race and fuzz fixes, and a k6 load test |
| [`ep16`](https://github.com/dev-marker/payments/tree/ep16) | 16. Shipping It | Distroless images, probes with draining, embedded migrations, CI, and the system in compose |
| [`ep17`](https://github.com/dev-marker/payments/tree/ep17) | 17. Outbound Webhooks | Signed webhook deliveries with retries, a delivery log, replay, and a verifying receiver |
| [`ep18`](https://github.com/dev-marker/payments/tree/ep18) | 18. Multi-currency & FX | Currency conversion with quotes that expire, exact rates and rounding, four-entry postings, and fees |
| [`ep19`](https://github.com/dev-marker/payments/tree/ep19) | 19. Fraud & Risk Checks | Velocity limits in Redis, a rules engine, and manual review of risky transfers |
| [`ep20`](https://github.com/dev-marker/payments/tree/ep20) | 20. Reconciliation & Reporting | Scheduled jobs with leader election, a nightly ledger reconciliation, and CSV statements and trial balances |
| [`ep21`](https://github.com/dev-marker/payments/tree/ep21) | 21. Security Hardening | MTLS between services, least-privilege database roles, reviewers from their sign-in, an audit log, and govulncheck |
| [`ep22`](https://github.com/dev-marker/payments/tree/ep22) | 22. Performance | A load test that tells the truth, pprof behind mTLS, a proxy buffer pool, an index for statements, and a bulkhead for reports |
| [`ep23`](https://github.com/dev-marker/payments/tree/ep23) | 23. Kubernetes | A kind cluster, one Helm chart for six services, rolling updates under load, and autoscaling |
| [`ep24`](https://github.com/dev-marker/payments/tree/ep24) | 24. Chaos & the Final Demo | A chaos harness that proves the books balance, Toxiproxy for network faults, and a game day |

## The system

Six Go services in one module, with one PostgreSQL database per service, talking over HTTP, gRPC and NATS JetStream:

| Service | Port | What it does |
|---|---|---|
| gateway | 8080 (internal 9080) | the only public entry point: JWT checks, rate limits, routing |
| wallet | 8081, gRPC 9081 | the double-entry ledger, holds, idempotency, FX conversion |
| auth | 8082 | users, argon2id passwords, EdDSA access tokens, rotating refresh tokens |
| transfer | 8083 | transfers as sagas: hold, capture or release, with retries and compensation |
| notification | 8084 | emails from events, idempotent consumers, a dead-letter queue |
| webhook | 8085 | signed webhook deliveries with retries, a delivery log and replay |

Infrastructure (Docker Compose): PostgreSQL 17, Redis, NATS JetStream, Mailpit, Jaeger, Prometheus and Grafana.

Some rules the code never breaks:

- Money is an `int64` count of minor units (cents), never a float.
- Balances are derived from ledger entries, and every transaction's entries sum to zero per currency.
- Every write that moves money is idempotent, and events leave through a transactional outbox.

## Run it

You need Go 1.26, Docker, and `make`. The [`golang-migrate`](https://github.com/golang-migrate/migrate), [`sqlc`](https://sqlc.dev) and [`buf`](https://buf.build) CLIs are only needed to create migrations or regenerate code.

```bash
make keys                                         # throwaway signing keys for local development, in secrets/
docker compose --profile app up -d --build --wait # infrastructure, migrations and all six services
```

Then open the `.http` files in [`api/`](api) with the VS Code [REST Client](https://marketplace.visualstudio.com/items?itemName=humao.rest-client) extension and send the requests, starting with `api/gateway.http`.

| UI | URL |
|---|---|
| Mailpit (emails) | http://localhost:8025 |
| Jaeger (traces) | http://localhost:16686 |
| Prometheus | http://localhost:9090 |
| Grafana | http://localhost:3000 |

To run a single service from source while you work on it, start the infrastructure with `docker compose up -d`, then `make run-wallet` (or `run-auth`, `run-transfer`, `run-notification`, `run-gateway`, `run-webhook`).

## Tests

```bash
make test             # unit tests only, fast
make test-integration # against real PostgreSQL, Redis and NATS in throwaway containers (needs Docker)
make test-race        # the same with the race detector
make loadtest         # a k6 load test of transfers through the gateway (the app profile must be running)
```

## License

[MIT](LICENSE). This is teaching code: read it, run it, build on it. Treat it as a starting point rather than a drop-in system for moving real money.
