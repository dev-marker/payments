#!/bin/sh
# Month end: finance pulls the trial balance, eight at a time, for a minute.
# It's on the wallet's internal port, so it takes our ops tools' certificate.
ops="--cacert secrets/tls/ops/ca.pem --cert secrets/tls/ops/cert.pem --key secrets/tls/ops/key.pem"
url="https://wallet:9091/internal/trial-balance.csv?date=$(date -u +%F)"
end=$(($(date +%s) + 60))
for i in 1 2 3 4 5 6 7 8; do
	while [ "$(date +%s)" -lt "$end" ]; do
		curl -s $ops --connect-to ::localhost: -o /dev/null "$url"
	done &
done
wait
