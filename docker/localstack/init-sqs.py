#!/opt/code/localstack/.venv/bin/python
"""Provisiona SQS local de forma repetível, preservando filas e mensagens.

Executado pelo ready hook do LocalStack; também pode rodar via docker exec.
O endpoint e as credenciais de teste são exclusivos do simulador local.
"""
import json
import os
import pathlib
import shlex

import boto3
from botocore.config import Config


def provision():
    sqs = boto3.client(
        "sqs",
        endpoint_url="http://127.0.0.1:4566",
        region_name=os.environ.get("AWS_DEFAULT_REGION", "us-east-1"),
        aws_access_key_id="test",
        aws_secret_access_key="test",
        config=Config(connect_timeout=3, read_timeout=5, retries={"max_attempts": 3}),
    )
    source_arn, dlq_arn = provision_pair(sqs, "wager-transactions.fifo", "wager-transactions-dlq.fifo")
    events_arn, events_dlq_arn = provision_pair(sqs, "wallet-events.fifo", "wallet-events-dlq.fifo")
    provision_identities(source_arn, dlq_arn, events_arn, events_dlq_arn)
    print("SQS configurado: comandos e eventos com FIFO/DLQ separados; maxReceiveCount=5.")


def provision_pair(sqs, source_name, dlq_name):
    # Criação com apenas os atributos imutáveis permite reconciliar os demais
    # parâmetros por SetQueueAttributes quando a fila já existe.
    dlq = sqs.create_queue(
        QueueName=dlq_name, Attributes={"FifoQueue": "true"}
    )["QueueUrl"]
    source = sqs.create_queue(
        QueueName=source_name, Attributes={"FifoQueue": "true"}
    )["QueueUrl"]
    dlq_arn = sqs.get_queue_attributes(QueueUrl=dlq, AttributeNames=["QueueArn"])["Attributes"]["QueueArn"]
    source_arn = sqs.get_queue_attributes(QueueUrl=source, AttributeNames=["QueueArn"])["Attributes"]["QueueArn"]

    sqs.set_queue_attributes(
        QueueUrl=dlq,
        Attributes={
            "MessageRetentionPeriod": "1209600",
            "ContentBasedDeduplication": "false",
            "RedriveAllowPolicy": json.dumps(
                {"redrivePermission": "byQueue", "sourceQueueArns": [source_arn]}
            ),
        },
    )
    sqs.set_queue_attributes(
        QueueUrl=source,
        Attributes={
            "VisibilityTimeout": "30",
            "ReceiveMessageWaitTimeSeconds": "20",
            "MessageRetentionPeriod": "345600",
            "ContentBasedDeduplication": "false",
            "RedrivePolicy": json.dumps({"deadLetterTargetArn": dlq_arn, "maxReceiveCount": "5"}),
        },
    )
    return source_arn, dlq_arn


def provision_identities(source_arn, dlq_arn, events_arn, events_dlq_arn):
    iam = boto3.client("iam", endpoint_url="http://127.0.0.1:4566", region_name="us-east-1",
                       aws_access_key_id="test", aws_secret_access_key="test")
    directory = pathlib.Path("/var/lib/localstack/jungle")
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    directory.chmod(0o700)
    identities = {
        "consumer": ("jungle-wager-consumer", [
            {"Effect": "Allow", "Action": ["sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:ChangeMessageVisibility"], "Resource": source_arn},
            {"Effect": "Allow", "Action": ["sqs:GetQueueUrl", "sqs:GetQueueAttributes"], "Resource": [source_arn, dlq_arn]},
        ]),
        "provider": ("jungle-provider-a-producer", [
            {"Effect": "Allow", "Action": ["sqs:SendMessage", "sqs:GetQueueUrl"], "Resource": source_arn},
        ]),
    }
    identities["publisher"] = ("jungle-outbox-publisher", [
        {"Effect": "Allow", "Action": ["sqs:SendMessage"], "Resource": events_arn},
        {"Effect": "Allow", "Action": ["sqs:GetQueueUrl", "sqs:GetQueueAttributes"], "Resource": [events_arn, events_dlq_arn]},
    ])
    identities["events"] = ("jungle-event-consumer", [
        {"Effect": "Allow", "Action": ["sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:ChangeMessageVisibility"], "Resource": events_arn},
        {"Effect": "Allow", "Action": ["sqs:GetQueueUrl", "sqs:GetQueueAttributes"], "Resource": [events_arn, events_dlq_arn]},
    ])
    for role, (username, statements) in identities.items():
        try:
            iam.create_user(UserName=username)
        except iam.exceptions.EntityAlreadyExistsException:
            pass
        iam.put_user_policy(UserName=username, PolicyName="jungle-sqs", PolicyDocument=json.dumps({"Version": "2012-10-17", "Statement": statements}))
        cached = directory / (role + ".json")
        existing = iam.list_access_keys(UserName=username)["AccessKeyMetadata"]
        active = {key["AccessKeyId"] for key in existing if key["Status"] == "Active"}
        keys = json.loads(cached.read_text()) if cached.exists() else None
        if keys is None or keys["AccessKeyId"] not in active:
            # Não remove credenciais existentes. O limite de duas chaves do IAM
            # continua valendo se o armazenamento do simulador foi perdido.
            keys = iam.create_access_key(UserName=username)["AccessKey"]
            keys = {name: keys[name] for name in ("AccessKeyId", "SecretAccessKey")}
            cached.write_text(json.dumps(keys))
            cached.chmod(0o600)
        if role == "consumer":
            variables = {"SQS_ENABLED": "false", "SQS_ENDPOINT": "http://127.0.0.1:4566", "SQS_REGION": "us-east-1",
                         "SQS_QUEUE_NAME": "wager-transactions.fifo", "SQS_DLQ_NAME": "wager-transactions-dlq.fifo",
                         "SQS_PROVIDER_ID": "provider-a", "SQS_CONSUMER_NAME": "wager-transactions-provider-a-v1",
                         "SQS_WORKERS": "4", "SQS_ACCESS_KEY_ID": keys["AccessKeyId"], "SQS_SECRET_ACCESS_KEY": keys["SecretAccessKey"]}
        elif role == "publisher":
            variables = {"OUTBOX_ENABLED": "false", "OUTBOX_ENDPOINT": "http://127.0.0.1:4566", "OUTBOX_REGION": "us-east-1",
                         "OUTBOX_QUEUE_NAME": "wallet-events.fifo", "OUTBOX_DLQ_NAME": "wallet-events-dlq.fifo", "OUTBOX_WORKERS": "2",
                         "OUTBOX_ACCESS_KEY_ID": keys["AccessKeyId"], "OUTBOX_SECRET_ACCESS_KEY": keys["SecretAccessKey"]}
        else:
            variables = {"AWS_ACCESS_KEY_ID": keys["AccessKeyId"], "AWS_SECRET_ACCESS_KEY": keys["SecretAccessKey"], "AWS_DEFAULT_REGION": "us-east-1"}
        environment = directory / (role + ".env")
        environment.write_text("".join(name + "=" + shlex.quote(value) + "\n" for name, value in variables.items()))
        environment.chmod(0o600)
    print("IAM local: quatro identidades separadas para comandos/eventos; credenciais em arquivos privados no volume.")


# O PythonScriptRunner do LocalStack usa exec(..., {}) sem definir __name__.
# O hook é um script executável, portanto inicializa também nesse contexto.
provision()
