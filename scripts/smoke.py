#!/usr/bin/env python3
"""Smoke local: HTTP autenticado, SQS, publisher, ledger e reconciliação.

Cria uma carteira exclusiva; não modifica carteiras existentes. Não imprime tokens.
"""
import json
import os
import subprocess
import time
import traceback
import urllib.error
import urllib.parse
import urllib.request
import uuid


def run():
    base = os.environ.get("BASE_URL", "http://127.0.0.1:8080").rstrip("/")
    issuer = os.environ.get("OIDC_ISSUER_URL", "http://localhost:8081/realms/jungle")

    def token(client, env, fallback):
        form = urllib.parse.urlencode({"grant_type": "client_credentials", "client_id": client,
                                      "client_secret": os.environ.get(env, fallback)}).encode()
        request = urllib.request.Request(issuer + "/protocol/openid-connect/token", data=form)
        with urllib.request.urlopen(request, timeout=5) as response:
            return json.load(response)["access_token"]

    def request(method, path, expected, body=None, bearer=None, key=None):
        headers = {"Content-Type": "application/json"}
        if bearer:
            headers["Authorization"] = "Bearer " + bearer
        if key:
            headers["Idempotency-Key"] = key
        encoded = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(base + path, data=encoded, headers=headers, method=method)
        try:
            response = urllib.request.urlopen(req, timeout=6)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            status = response.status
            raw = response.read().decode()
        if status != expected:
            raise RuntimeError("status inesperado em " + method + " " + path + ": " + str(status))
        return json.loads(raw) if path != "/metrics" else raw

    assert request("GET", "/health/live", 200)["status"] == "ok"
    ready = request("GET", "/health/ready", 200)
    assert ready == {"status": "ready", "checks": {"postgres": "ok", "sqsConsumer": "ok", "sqsPublisher": "ok"}}
    internal = token("jungle-internal", "KEYCLOAK_INTERNAL_CLIENT_SECRET", "jungle_internal_local")
    provider = token("jungle-provider-a", "KEYCLOAK_PROVIDER_A_CLIENT_SECRET", "jungle_provider_a_local")
    suffix = uuid.uuid4().hex
    wallet = request("POST", "/wallets", 201, {"playerId": "smoke-" + suffix,
                     "initialBalance": {"amount": "100.00", "currency": "BRL"}}, internal)

    def command(kind, name, amount, reference=None):
        data = {"providerId": "provider-a", "externalTransactionId": suffix + "-" + name,
                "walletId": wallet["id"], "playerId": wallet["playerId"], "roundId": "round", "gameId": "game",
                "kind": kind, "money": {"amount": amount, "currency": "BRL"}}
        if reference:
            data["referenceExternalTransactionId"] = suffix + "-" + reference
        return data

    def submit(data, expected=200):
        return request("POST", "/wagering/transactions", expected, data, provider, data["externalTransactionId"])

    bet = command("BET", "bet", "80.00")
    first = submit(bet)
    assert first["status"] == "PROCESSED" and first["balance"]["amount"] == "20.00"
    bet["money"]["amount"] = "00080.00"
    replay = submit(bet)
    assert replay["idempotentReplay"] and replay["transactionId"] == first["transactionId"]
    rejected = submit(command("BET", "reject", "80.00"), 422)
    assert rejected["failureCode"] == "INSUFFICIENT_FUNDS"
    for data in [command("WIN", "win", "10.00"), command("LOSS", "loss", "0.00"),
                 command("REFUND", "refund", "80.00", "bet"), command("ROLLBACK", "rollback", "10.00", "win")]:
        assert submit(data)["status"] == "PROCESSED"

    queued = command("BET", "queued", "1.00")
    queued["idempotencyKey"] = queued["externalTransactionId"]
    envelope = {"messageId": "smoke-" + suffix, "type": "WagerTransactionRequested",
                "occurredAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "data": queued}
    sent = subprocess.run(["docker", "exec", "-i", os.environ.get("LOCALSTACK_CONTAINER_NAME", "localstack-aws"),
                           "/opt/code/localstack/.venv/bin/python", "/etc/localstack/send-wager.py"],
                          input=json.dumps(envelope).encode(), stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=10)
    if sent.returncode:
        raise RuntimeError("envio SQS do smoke falhou; confira o container LocalStack configurado")
    deadline = time.monotonic() + 15
    while True:
        current = request("GET", "/wallets/" + wallet["id"], 200, bearer=internal)
        if current["balance"]["amount"] == "99.00":
            break
        if time.monotonic() >= deadline:
            raise RuntimeError("consumidor SQS não concluiu o comando")
        time.sleep(0.1)
    queued.pop("idempotencyKey")
    assert submit(queued)["idempotentReplay"]
    ledger = request("GET", "/wallets/" + wallet["id"] + "/ledger", 200, bearer=internal)
    assert len(ledger["entries"]) == 6
    reconciliation = request("POST", "/wallets/" + wallet["id"] + "/reconciliation", 200, bearer=internal)
    assert reconciliation["consistent"] and reconciliation["storedBalance"]["amount"] == "99.00"
    while True:
        text = request("GET", "/metrics", 200, bearer=internal)
        for source in ("postgres", "sqs_consumer", "sqs_publisher"):
            assert 'jungle_metrics_source_up{source="' + source + '"} 1\n' in text
        assert 'jungle_wager_duplicates_total{transport="http"}' in text
        assert 'jungle_wager_processing_seconds_bucket' in text
        if "jungle_outbox_unpublished 0\n" in text:
            break
        if time.monotonic() >= deadline:
            raise RuntimeError("publisher não esvaziou a outbox")
        time.sleep(0.1)
    print("Smoke passou: health, OIDC, cinco operações, replay HTTP/SQS, ledger, reconciliação, métricas e outbox publicada.")


if __name__ == "__main__":
    try:
        run()
    except Exception as error:
        # Algumas bibliotecas incluem URLs/corpos em erros. Registre apenas o tipo.
        line = traceback.extract_tb(error.__traceback__)[-1].lineno
        print("Smoke falhou (" + type(error).__name__ + ", linha " + str(line) + "). Confira os serviços, configuração e assertions do script.")
        raise SystemExit(1) from None
