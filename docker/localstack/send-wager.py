#!/opt/code/localstack/.venv/bin/python
"""Envia um envelope pelo produtor IAM local; não usa a identidade administrativa."""
import hashlib
import json
import pathlib
import sys
import boto3

body = sys.stdin.read(16385)
if len(body.encode("utf-8")) > 16384:
    raise SystemExit("Envelope excede 16 KiB")
envelope = json.loads(body)
wallet_id = envelope["data"]["walletId"]
keys = json.loads(pathlib.Path("/var/lib/localstack/jungle/provider.json").read_text())
sqs = boto3.client("sqs", endpoint_url="http://127.0.0.1:4566", region_name="us-east-1",
    aws_access_key_id=keys["AccessKeyId"], aws_secret_access_key=keys["SecretAccessKey"])
queue = sqs.get_queue_url(QueueName="wager-transactions.fifo")["QueueUrl"]
result = sqs.send_message(QueueUrl=queue, MessageBody=body,
    MessageGroupId="wallet:" + hashlib.sha256(wallet_id.encode("utf-8")).hexdigest(),
    MessageDeduplicationId=hashlib.sha256(body.encode("utf-8")).hexdigest())
print("Envelope enviado; broker messageId=" + result["MessageId"])
