#!/opt/code/localstack/.venv/bin/python
"""Verifica atributos e redrive com mensagens em filas temporárias.

As filas do projeto são apenas consultadas. As filas temporárias são removidas
ao final, inclusive em falha. Execute no Python interno do LocalStack.
"""
import json
import os
from pathlib import Path
import runpy
import time
import uuid

import boto3
from botocore.config import Config


def verify():
    # O mesmo health check empregado pelo Compose valida as filas persistentes.
    health = runpy.run_path(str(Path(__file__).with_name("jungle-healthcheck.py")))
    if not health["healthy"]():
        raise RuntimeError("As filas do projeto não estão configuradas corretamente")
    sqs = boto3.client(
        "sqs", endpoint_url="http://127.0.0.1:4566",
        region_name=os.environ.get("AWS_DEFAULT_REGION", "us-east-1"),
        aws_access_key_id="test", aws_secret_access_key="test",
        config=Config(connect_timeout=2, read_timeout=5, retries={"max_attempts": 1}),
    )
    configured_url = sqs.get_queue_url(QueueName="wager-transactions.fifo")["QueueUrl"]
    configured = sqs.get_queue_attributes(QueueUrl=configured_url, AttributeNames=["All"])["Attributes"]
    prefix = "jungle-sqs-check-" + uuid.uuid4().hex
    temporary = []
    try:
        dlq = sqs.create_queue(QueueName=prefix + "-dlq.fifo", Attributes={"FifoQueue": "true"})["QueueUrl"]
        temporary.append(dlq)
        arn = sqs.get_queue_attributes(QueueUrl=dlq, AttributeNames=["QueueArn"])["Attributes"]["QueueArn"]
        policy = json.loads(configured["RedrivePolicy"])
        policy["deadLetterTargetArn"] = arn
        source = sqs.create_queue(
            QueueName=prefix + ".fifo",
            Attributes={"FifoQueue": "true", "ContentBasedDeduplication": configured["ContentBasedDeduplication"], "RedrivePolicy": json.dumps(policy)},
        )["QueueUrl"]
        temporary.append(source)
        sent = sqs.send_message(QueueUrl=source, MessageBody="local-redrive-check", MessageGroupId=prefix, MessageDeduplicationId=prefix)
        repeated = sqs.send_message(QueueUrl=source, MessageBody="local-redrive-check", MessageGroupId=prefix, MessageDeduplicationId=prefix)
        if sent["MessageId"] != repeated["MessageId"]:
            raise RuntimeError("A fila não deduplicou o MessageDeduplicationId")
        maximum = int(policy["maxReceiveCount"])
        for attempt in range(1, maximum + 1):
            messages = sqs.receive_message(QueueUrl=source, WaitTimeSeconds=0, VisibilityTimeout=0, MessageSystemAttributeNames=["ApproximateReceiveCount"]).get("Messages", [])
            if len(messages) != 1 or messages[0]["Body"] != "local-redrive-check" or int(messages[0]["Attributes"]["ApproximateReceiveCount"]) != attempt:
                raise RuntimeError("Contagem de recebimentos inesperada")
        # O recebimento seguinte aciona o redrive da mensagem não confirmada.
        if sqs.receive_message(QueueUrl=source, WaitTimeSeconds=0).get("Messages"):
            raise RuntimeError("A mensagem ultrapassou o limite sem ir à DLQ")
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            messages = sqs.receive_message(QueueUrl=dlq, WaitTimeSeconds=0).get("Messages", [])
            if messages:
                if len(messages) != 1 or messages[0]["Body"] != "local-redrive-check":
                    raise RuntimeError("Conteúdo inesperado na DLQ")
                break
            time.sleep(0.2)
        else:
            raise RuntimeError("A DLQ não recebeu a mensagem")
        print("Verificado: duas filas FIFO, atributos, deduplicação e envio à DLQ após 5 recebimentos; filas temporárias removidas ao encerrar.")
    finally:
        for url in reversed(temporary):
            sqs.delete_queue(QueueUrl=url)


if __name__ == "__main__":
    verify()
