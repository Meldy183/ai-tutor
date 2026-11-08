#!/bin/sh
set -eu

# Run against an isolated smoke project after its normal stack checks.
# Forward Compose project/env options as arguments; cleanup belongs to the caller.
override=$(mktemp)
trap 'rm -f "$override"' EXIT INT TERM
cat > "$override" <<'EOF'
services:
  voice-gateway:
    command: ["--config", "/missing-voice-config.yaml"]
EOF

docker compose -f docker-compose.yaml "$@" -f "$override" up -d --no-build --force-recreate voice-gateway
voice_id=$(docker compose -f docker-compose.yaml "$@" -f "$override" ps --all --quiet voice-gateway)
docker inspect "$voice_id" | jq -e '.[0].State.Health.Status != "healthy"' >/dev/null

# Recreate the API to exercise startup, rather than only serving existing traffic.
docker compose -f docker-compose.yaml "$@" -f "$override" up -d --no-build --force-recreate --wait --wait-timeout 120 api
sh ./tests/integration/stack-smoke.sh
printf '%s\n' 'API startup, auth and text catalogs passed with voice gateway unavailable.'
