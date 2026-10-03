#!/bin/sh
# Steady traffic through the gateway, until Ctrl+C, so the dashboards have
# something to show: a new user checks their balance twice a second, just
# under the gateway's limit. Episode 15 load-tests properly, with k6.
set -eu
api=http://localhost:8080
user="{\"email\": \"load-$(date +%s)@example.com\", \"password\": \"a load test password\"}"
curl -sf "$api/v1/auth/register" -d "$user" >/dev/null
token=$(curl -sf "$api/v1/auth/login" -d "$user" | jq -r .access_token)
wallet=$(curl -sf "$api/v1/wallets" -H "Authorization: Bearer $token" -d '{"currency": "USD"}' | jq -r .id)
echo "checking the balance of wallet $wallet twice a second, until Ctrl+C"
while true; do
    curl -s -o /dev/null -H "Authorization: Bearer $token" "$api/v1/wallets/$wallet/balance"
    sleep 0.5
done
