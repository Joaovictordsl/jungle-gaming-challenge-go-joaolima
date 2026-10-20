#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
if [ ! -f .env.sqs.local ] || [ ! -f .env.outbox.local ]; then
  echo 'Execute sh scripts/local-up.sh primeiro.' >&2
  exit 1
fi
set -a
. ./.env.sqs.local
. ./.env.outbox.local
set +a
export DATABASE_URL="${DATABASE_URL:-postgres://postgres:postgres_local@localhost:${POSTGRES_PORT:-5433}/jungle?sslmode=disable}"
export OIDC_ISSUER_URL="${OIDC_ISSUER_URL:-http://localhost:${KEYCLOAK_PORT:-8081}/realms/jungle}"
export SQS_ENDPOINT="http://127.0.0.1:${LOCALSTACK_PORT:-4566}"
export OUTBOX_ENDPOINT="$SQS_ENDPOINT"
export SQS_ENABLED=true OUTBOX_ENABLED=true
exec go run .
