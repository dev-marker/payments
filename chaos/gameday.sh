# A game day: one fault after another, while go run ./cmd/chaos sends
# transfers. Every line this prints shows up on the harness's timeline.
set -eu
wallet=http://localhost:8474/proxies/wallet/toxics

sleep 10
echo "the wallet crashes"
docker compose kill wallet
sleep 15
docker compose start wallet
echo "the wallet is back"

sleep 10
echo "the transfer service crashes"
docker compose kill transfer
sleep 10
docker compose start transfer
echo "the transfer service is back"

sleep 10
echo "NATS goes down"
docker compose stop nats
sleep 30
docker compose start nats
echo "NATS is back"

sleep 10
echo "the network to the wallet slows down"
curl -sf -o /dev/null $wallet --json '{"name": "slow", "type": "latency", "attributes": {"latency": 2500}}'
sleep 20
curl -sf -X DELETE $wallet/slow
echo "the network is fast again"

sleep 10
echo "the network to the wallet is cut"
curl -sf -o /dev/null $wallet --json '{"name": "cut", "type": "timeout", "attributes": {"timeout": 0}}'
sleep 15
curl -sf -X DELETE $wallet/cut
echo "the network is back"

sleep 10
echo "Postgres restarts"
docker compose restart postgres
echo "Postgres is back"
