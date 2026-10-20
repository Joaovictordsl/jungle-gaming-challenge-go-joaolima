#!/opt/code/localstack/.venv/bin/python
"""Prova permissões reais; falha se a instância aceitar chamadas sem autorização."""
import json
import pathlib
import boto3
from botocore.exceptions import ClientError
from botocore.config import Config


def verify(quiet=False):
    clients = {}
    for role in ("consumer", "provider", "publisher", "events"):
        keys = json.loads(pathlib.Path("/var/lib/localstack/jungle/" + role + ".json").read_text())
        clients[role] = boto3.client("sqs", endpoint_url="http://127.0.0.1:4566", region_name="us-east-1",
            aws_access_key_id=keys["AccessKeyId"], aws_secret_access_key=keys["SecretAccessKey"],
            config=Config(connect_timeout=2, read_timeout=3, retries={"max_attempts": 0}))
    provider, consumer = clients["provider"], clients["consumer"]
    source = provider.get_queue_url(QueueName="wager-transactions.fifo")["QueueUrl"]
    consumer.get_queue_attributes(QueueUrl=source, AttributeNames=["QueueArn"])
    publisher, events = clients["publisher"], clients["events"]
    output = publisher.get_queue_url(QueueName="wallet-events.fifo")["QueueUrl"]
    publisher.get_queue_attributes(QueueUrl=output, AttributeNames=["QueueArn"])
    events.get_queue_attributes(QueueUrl=output, AttributeNames=["QueueArn"])
    denied = [lambda: publisher.get_queue_url(QueueName="wager-transactions.fifo"),
              lambda: publisher.list_queues(),
              lambda: events.get_queue_url(QueueName="wager-transactions.fifo"),
              lambda: provider.get_queue_url(QueueName="wallet-events.fifo"),
              lambda: provider.get_queue_attributes(QueueUrl=source, AttributeNames=["QueueArn"]),
              lambda: provider.get_queue_url(QueueName="wager-transactions-dlq.fifo"),
              lambda: consumer.list_queues()]
    for call in denied:
        try:
            call()
        except ClientError as error:
            if "AccessDenied" in error.response["Error"]["Code"]:
                continue
            raise RuntimeError("Verificação IAM falhou sem resposta AccessDenied") from None
        raise RuntimeError("IAM não está sendo fiscalizado. Reinicie o LocalStack com ENFORCE_IAM=1 antes de habilitar o consumidor.")
    if not quiet:
        print("IAM verificado: chamadas permitidas e sete negações reais; nenhuma mensagem alterada.")


if __name__ == "__main__":
    verify()
