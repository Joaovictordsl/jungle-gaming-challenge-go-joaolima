#!/opt/code/localstack/.venv/bin/python
"""Confere as quatro filas, redrive e fiscalização IAM do ambiente local."""
import json
import os
import sys
import runpy
from pathlib import Path

import boto3
from botocore.config import Config


def healthy():
    sqs = boto3.client(
        "sqs",
        endpoint_url="http://127.0.0.1:4566",
        region_name=os.environ.get("AWS_DEFAULT_REGION", "us-east-1"),
        aws_access_key_id="test",
        aws_secret_access_key="test",
        config=Config(connect_timeout=1, read_timeout=1, retries={"max_attempts": 0}),
    )
    for names in (("wager-transactions.fifo", "wager-transactions-dlq.fifo"), ("wallet-events.fifo", "wallet-events-dlq.fifo")):
        if not pair_healthy(sqs, names):
            return False
    runpy.run_path(str(Path(__file__).with_name("verify-iam.py")))["verify"](quiet=True)
    return True


def pair_healthy(sqs, names):
    attributes = []
    for name in names:
        url = sqs.get_queue_url(QueueName=name)["QueueUrl"]
        attributes.append(sqs.get_queue_attributes(QueueUrl=url, AttributeNames=["All"])["Attributes"])
    source, dlq = attributes
    redrive = json.loads(source.get("RedrivePolicy", "{}"))
    allow = json.loads(dlq.get("RedriveAllowPolicy", "{}"))
    return (
        all(a.get("FifoQueue") == "true" and a.get("ContentBasedDeduplication") == "false" for a in attributes)
        and source.get("VisibilityTimeout") == "30"
        and source.get("ReceiveMessageWaitTimeSeconds") == "20"
        and source.get("MessageRetentionPeriod") == "345600"
        and dlq.get("MessageRetentionPeriod") == "1209600"
        and redrive.get("deadLetterTargetArn") == dlq.get("QueueArn")
        and str(redrive.get("maxReceiveCount")) == "5"
        and allow.get("redrivePermission") == "byQueue"
        and allow.get("sourceQueueArns") == [source.get("QueueArn")]
    )


if __name__ == "__main__":
    try:
        sys.exit(0 if healthy() else 1)
    except Exception:
        # O health check não expõe configuração, credenciais ou respostas do SDK.
        sys.exit(1)
