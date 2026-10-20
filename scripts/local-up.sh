#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
umask 077

docker compose up -d --wait postgres keycloak
docker compose run --rm migrate up

case "${LOCALSTACK_MODE:-cli}" in
  cli)
    lstk --config "${LSTK_CONFIG:-.lstk/config.toml}" start --persist
    docker exec "${LOCALSTACK_CONTAINER_NAME:-localstack-aws}" /opt/code/localstack/.venv/bin/python /etc/localstack/verify-iam.py
    docker cp "${LOCALSTACK_CONTAINER_NAME:-localstack-aws}:/var/lib/localstack/jungle/consumer.env" .env.sqs.local
    docker cp "${LOCALSTACK_CONTAINER_NAME:-localstack-aws}:/var/lib/localstack/jungle/publisher.env" .env.outbox.local
    ;;
  compose)
    docker compose up -d --wait localstack
    docker compose exec -T localstack /opt/code/localstack/.venv/bin/python /etc/localstack/verify-iam.py
    docker compose cp localstack:/var/lib/localstack/jungle/consumer.env .env.sqs.local
    docker compose cp localstack:/var/lib/localstack/jungle/publisher.env .env.outbox.local
    ;;
  *) echo 'LOCALSTACK_MODE deve ser cli ou compose.' >&2; exit 1 ;;
esac
chmod 600 .env.sqs.local .env.outbox.local
echo 'Infraestrutura pronta. Inicie a API com: sh scripts/run-local.sh'
