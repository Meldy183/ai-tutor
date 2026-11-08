#!/bin/sh
set -eu
repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$repo_dir"
for service in backend llm-gateway voice-gateway course-graph; do
    test -f "deployment/$service/prod.yaml" || { printf 'Missing deployment/%s/prod.yaml\n' "$service" >&2; exit 1; }
done
test -f .env || { printf '%s\n' 'Existing infrastructure .env is required.' >&2; exit 1; }
test -f certs/server.pem && test -f certs/server-key.pem || { printf '%s\n' 'Provision TLS certificates in certs/ before deployment.' >&2; exit 1; }
docker compose --env-file .env -f docker-compose.yaml -f docker-compose.prod.yaml config --quiet
exec docker compose --env-file .env -f docker-compose.yaml -f docker-compose.prod.yaml up --no-build "$@"
