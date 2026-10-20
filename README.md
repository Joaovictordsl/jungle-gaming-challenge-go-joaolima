# Jungle Gaming — desafio backend Go

API de carteira e operações de apostas em Go, com PostgreSQL e composição via Fx.
Uma abertura positiva
confirma carteira, `OPENING`, crédito no ledger e dois eventos na outbox na mesma
transação. O servidor Gin expõe abertura e consulta de carteiras com autenticação
JWT/OIDC e um health check público. O Keycloak local emite tokens para o serviço
interno e para o provedor A. `BET` debita, `WIN` credita e `LOSS` registra o resultado
sem movimentar a carteira. `REFUND` devolve integralmente uma aposta processada,
com proteção contra devolução duplicada. As operações têm idempotência persistente.
`WIN` e `REFUND` com referência ausente aguardam de forma durável e são retomados
por um worker. `ROLLBACK` desfaz integralmente BET, WIN ou REFUND. Consultas de transações, extrato paginado e reconciliação estão disponíveis.
O consumidor SQS usa os mesmos casos de uso da API e confirma inbox, operação
financeira, ledger e outbox em uma transação. O Compose provisiona FIFO/DLQ e
identidades IAM separadas. O publisher publica eventos confirmados em `wallet-events.fifo`, com retry e recuperação de reservas abandonadas.

## Pré-requisitos

- Go 1.27.2 ou superior.
- Docker com Docker Compose.
- Python 3 para o smoke de ponta a ponta.
- LocalStack com licença que permita IAM enforcement: CLI autenticado com `lstk login`
  ou token pessoal no shell para a alternativa Compose. Nenhum token acompanha o projeto.

## Início rápido a partir de um checkout limpo

Com Docker ativo e o CLI LocalStack autenticado, na raiz do repositório:

```sh
go mod download
sh scripts/local-up.sh
sh scripts/run-local.sh
```

`local-up.sh` cria os bancos, aguarda os health checks, aplica as onze migrations,
inicializa o realm/clientes do Keycloak e as quatro filas/identidades IAM do
LocalStack. Copia somente as credenciais locais geradas para `.env.sqs.local` e
`.env.outbox.local`, ignorados pelo Git e com permissão 600. `run-local.sh` carrega
essas credenciais e inicia a API com consumidor e publisher habilitados. Os scripts
não carregam `.env` automaticamente: personalizações devem ser exportadas no shell.

Em outro terminal, confirme o fluxo completo:

```sh
python3 scripts/smoke.py
```

O smoke cria um jogador/carteira exclusivos e verifica OIDC, as cinco operações,
replays, entrada pela fila, ledger, reconciliação, métricas e esvaziamento da outbox.
Ele não usa carteiras já existentes. Encerre a API com Ctrl+C.

Para usar o LocalStack pelo Compose, configure `LOCALSTACK_AUTH_TOKEN` no shell,
pare a instância CLI que ocupa a porta 4566 e execute:

```sh
LOCALSTACK_MODE=compose sh scripts/local-up.sh
sh scripts/run-local.sh
LOCALSTACK_CONTAINER_NAME="$(docker compose ps -q localstack)" python3 scripts/smoke.py
```

Não é necessário Dockerfile: o fluxo documentado executa Go no host e as dependências
em containers. `POSTGRES_PORT`, `KEYCLOAK_PORT` e `LOCALSTACK_PORT` permitem portas
alternativas no Compose e nos scripts. No modo CLI, ajuste também `port` e
`container_name` em um perfil separado e informe `LSTK_CONFIG` e
`LOCALSTACK_CONTAINER_NAME`. Use `COMPOSE_PROJECT_NAME` distinto para volumes isolados.
O endpoint da API pode ser alterado com `HTTP_ADDR`; no smoke, use `BASE_URL` e
`OIDC_ISSUER_URL` correspondentes.

## Métricas

`GET /metrics` exige token interno com `wallets:read` e expõe Prometheus:

| Série | Significado |
| --- | --- |
| `jungle_wager_responses_total{transport,kind,status}` | Resultados de atendimento HTTP/SQS, incluindo replays e ERROR |
| `jungle_wager_duplicates_total{transport}` | Respostas recuperadas por idempotência financeira ou inbox |
| `jungle_wager_conflicts_total{transport,reason}` | Conflitos de chave, ID externo, serialização SQL e deadlock |
| `jungle_wager_processing_seconds` | Histograma de execução do caso de uso, incluindo locks e persistência |
| `jungle_wager_transactions{kind,status}` | Operações únicas persistidas; replay não aumenta esse total |
| `jungle_reference_attempts` | Soma das tentativas de referências confirmadas no banco |
| `jungle_outbox_unpublished`, `jungle_outbox_lag_seconds` | Backlog e idade do evento pendente mais antigo, consultados no scrape |
| `jungle_sqs_dlq_messages{queue,state}` | Quantidades aproximadas nas DLQs de comandos/eventos, sem receber mensagens |
| `jungle_metrics_source_up{source}` | Disponibilidade da consulta de banco/DLQ naquele scrape |

Também permanecem os contadores de retry/consumo SQS, publicação da outbox e
divergências de reconciliação descritos adiante. Contadores/histogramas reiniciam
com o processo; snapshots de banco/filas são compartilhados e devem ser agregados
por `max`, não somados entre réplicas. O scrape consulta fontes em paralelo com
deadline global de 2s. Fonte indisponível produz `source_up=0` e omite suas amostras,
preservando HTTP 200 e contadores disponíveis. Módulos SQS desabilitados não são
consultados. Não há labels por carteira, jogador ou provedor.

## SQS local com LocalStack

O serviço `localstack` usa a imagem fixa `localstack/localstack-pro:2026.10.0`,
publicada somente em `127.0.0.1:4566`. Configure seu token de ativação em
`LOCALSTACK_AUTH_TOKEN` no `.env` local ou no shell. O `.env` é ignorado pelo Git;
`.env.example` contém somente o nome da variável, sem credencial. Consulte a
[configuração oficial do token](https://docs.localstack.cloud/aws/getting-started/auth-token/).

Na raiz do projeto, você também pode continuar usando o CLI já autenticado:

```sh
lstk start --persist
docker exec localstack-aws /opt/code/localstack/.venv/bin/python /etc/localstack/verify-iam.py
```

O perfil versionado `.lstk/config.toml` fixa a imagem, habilita IAM/STS, monta os
hooks e persiste o estado em `.localstack/`, ignorado pelo Git. Ele usa a sessão
que você já salvou com `lstk login`, sem copiar token para o repositório. Execute
`lstk` na raiz do projeto para carregar esse perfil. A configuração de ambientes
segue a [documentação oficial do CLI](https://docs.localstack.cloud/aws/developer-tools/running-localstack/lstk/configuration/).

Como alternativa, configure o token local e use o serviço do Compose:

```sh
lstk stop
docker compose up -d localstack
docker compose ps localstack
```

Use uma única instância na porta 4566. O CLI e o Compose têm volumes distintos;
para trocar de ambiente conservando recursos, salve/carregue um snapshot local.
Nos exemplos `docker compose exec localstack ...`, quem usa o perfil CLI deve
substituir por `docker exec localstack-aws ...`.

O hook `docker/localstack/init-sqs.py` é executado automaticamente quando o
LocalStack fica pronto. Cria ou atualiza, em `us-east-1`, estas filas:

| Configuração | Valor |
| --- | --- |
| Fila principal | `wager-transactions.fifo` |
| Fila de falhas | `wager-transactions-dlq.fifo` |
| Tipo | FIFO nas duas filas |
| Visibility timeout da principal | 30 segundos |
| Long polling da principal | 20 segundos |
| Limite de recebimentos sem confirmação | 5; o recebimento seguinte encaminha à DLQ |
| Retenção na principal | 4 dias |
| Retenção na DLQ | 14 dias |
| RedriveAllowPolicy da DLQ | Permite somente a fila principal |
| ContentBasedDeduplication | Desabilitada; o produtor deverá enviar MessageDeduplicationId |

O script pode ser repetido sem apagar filas ou mensagens; usa CreateQueue com o
atributo FIFO imutável e SetQueueAttributes para os parâmetros. `PERSISTENCE=1`
e o volume `localstack_data` habilitam persistência na instância do Compose.
O health check verifica as quatro filas (comandos/eventos e suas DLQs), FIFO,
redrive, visibilidade, retenção, long polling, deduplicação e fiscalização IAM real. O Python utilizado é o do
ambiente interno da imagem, que inclui boto3. Não precisa instalar esse SDK no Mac.

Para listar as filas:

```sh
docker compose exec -T localstack awslocal --region us-east-1 sqs list-queues
```

Para repetir a inicialização:

```sh
docker compose exec -T localstack /opt/code/localstack/.venv/bin/python /etc/localstack/init/ready.d/10-init-sqs.py
```

Para verificar a configuração, deduplicação e o encaminhamento real à DLQ:

```sh
docker compose exec -T localstack /opt/code/localstack/.venv/bin/python /etc/localstack/verify-sqs.py
```

A verificação consulta as filas do projeto e cria filas temporárias para enviar
mensagens de teste. Confere cinco recebimentos e redrive, removendo as filas
temporárias ao final. Não recebe nem remove mensagens das filas do projeto.
O bootstrap e o teste das filas usam a identidade administrativa de teste `test`,
na conta simulada `000000000000`. Produtor e consumidor usam identidades IAM
próprias, com permissões restritas. O token de ativação do LocalStack é separado
dessas credenciais AWS locais. A outbox usa um publisher IAM próprio e a fila de
eventos possui uma identidade de leitura, sem acesso à fila de comandos.

## Consumidor SQS e inbox

O Compose inicia o LocalStack com `ENFORCE_IAM=1` e os serviços `sqs,iam,sts`.
STS é necessário para a avaliação de identidade usada pela fiscalização IAM.
O hook cria `jungle-provider-a-producer` e `jungle-wager-consumer`, com políticas
restritas aos ARNs das filas. O produtor pode enviar e resolver a URL da principal;
o consumidor pode receber, remover e mudar visibilidade da principal, e consultar
metadados das duas filas. Não possui permissão para publicar, purgar ou criar filas.
As chaves geradas ficam em arquivos privados no volume. Repetir o hook conserva
chaves ativas, filas e mensagens.

Uma instância iniciada separadamente por `lstk start` precisa também fiscalizar
IAM. Políticas cadastradas não bastam quando `ENFORCE_IAM` está desligado.
Antes de habilitar o consumidor, execute a verificação real:

```sh
docker compose exec -T localstack /opt/code/localstack/.venv/bin/python /etc/localstack/verify-iam.py
```

Ela confirma acessos permitidos e sete negações reais, sem consumir mensagens. Se falhar
por ausência de fiscalização, inicie o simulador com `ENFORCE_IAM=1`, como no
Compose, e repita a verificação. A disponibilidade dessa função depende da licença
LocalStack. Os testes de fluxo SQS não comprovam a fiscalização IAM por si só.

Com PostgreSQL, migrations e Keycloak preparados, copie as credenciais locais sem
imprimi-las e exporte a configuração do consumidor no terminal da aplicação:

```sh
docker compose cp localstack:/var/lib/localstack/jungle/consumer.env .env.sqs.local
chmod 600 .env.sqs.local
set -a
source .env.sqs.local
set +a
SQS_ENABLED=true go run .
```

O arquivo é ignorado pelo Git e mantém `SQS_ENABLED=false` até a ativação explícita
no comando. O Go usa o AWS SDK v2 com essas credenciais próprias e assinatura
SigV4. Não usa o token de ativação LocalStack ou as credenciais do Keycloak.
As variáveis estão em `.env.example`; `SQS_WORKERS` aceita 1 a 16, padrão 4.
A inicialização confere acesso às duas filas, FIFO, visibilidade e redrive.
Com SQS desabilitado, a aplicação não precisa conectar ao broker.

O contrato está em [docs/sqs-message.schema.json](docs/sqs-message.schema.json).
O envelope tem `messageId`, `type=WagerTransactionRequested`, `occurredAt` RFC3339
e `data` com os mesmos campos financeiros da API e `idempotencyKey`.
Money continua como strings decimais; JSON numérico, chaves repetidas, campos
inesperados e corpos acima de 16 KiB são rejeitados. OPENING não entra pela fila.

Para enviar um exemplo, substitua `walletId` e `playerId` no arquivo e use o
produtor IAM local, que calcula os identificadores FIFO automaticamente:

```sh
docker compose exec -T localstack /opt/code/localstack/.venv/bin/python /etc/localstack/send-wager.py < docs/sqs-message.example.json
```

O `MessageGroupId` é `wallet:` seguido do SHA-256 hexadecimal do ID da carteira em
UTF-8. O consumidor valida esse grupo e vincula a fila a `SQS_PROVIDER_ID`, padrão
`provider-a`; o corpo não escolhe a identidade autorizada. Outro provedor deve ter
fila, identidade IAM e binding próprios. O helper usa SHA-256 do corpo completo
como `MessageDeduplicationId`, incluindo `messageId`. Ao repetir uma entrega,
conserve também `occurredAt` e o conteúdo. A deduplicação temporária FIFO não
substitui a idempotência SQL.

Há duas identidades persistentes: `(consumerName,messageId)` na inbox e
`(providerId,idempotencyKey)` na operação financeira, com unicidade adicional de
`(providerId,externalTransactionId)`. O hash da inbox abrange o envelope normalizado;
o financeiro exclui metadados de transporte e a chave. Reutilizar `messageId` com
outro conteúdo leva à DLQ. HTTP e SQS recuperam a mesma operação financeira sem
reaplicar saldo, ledger ou eventos. Mantenha `SQS_CONSUMER_NAME` estável entre
instâncias e reinícios: ele identifica o consumidor lógico, não o processo.

O ACK é `DeleteMessage` e acontece apenas após o commit. Se a remoção falha, a
reentrega encontra o resultado salvo. `REJECTED` de negócio também é confirmado
após persistência. `PENDING_REFERENCE` é confirmado quando a pendência está durável;
o worker de referências continua sua resolução sem manter a mensagem recebida.
A inbox preserva o resultado daquele recebimento; o GET da transação acompanha
sua evolução posterior.

Falhas transitórias revertem a inbox e os efeitos financeiros e usam visibilidade
com backoff de 1, 2, 4 e 8 segundos. Na quinta falha, se o banco permite o commit,
a inbox registra `FAILED/attempts_exhausted` e, para um comando financeiro válido
com carteira compatível, confirma a transação financeira em
`FAILED/INFRASTRUCTURE_RETRIES_EXHAUSTED`; a mensagem segue para redrive.
Falhas permanentes válidas são auditadas e liberadas para redrive sem backoff.
JSON inválido e falhas anteriores à identificação do envelope ficam auditáveis
pela mensagem original na DLQ. Se o banco estiver indisponível até o limite,
a DLQ conserva a mensagem, mas não há garantia de auditoria SQL desse erro.
O FAILED financeiro reserva provedor/chave e provedor/ID externo. Repetir com
outro `messageId`, reiniciar o processo ou usar HTTP preserva o mesmo resultado,
sem saldo, versão, ledger ou eventos adicionais. O POST HTTP retorna `500` com
o resultado `FAILED`, `transactionId`, `failureCode`, saldo observado e
`idempotentReplay=true`; o GET da transação retorna `200` com a auditoria.
Conflitos, carteira inexistente e envelope inválido não criam FAILED financeiro.
Uma falha transitória HTTP continua permitindo repetir a mesma chave: receber
um `500/503` sem resultado financeiro não estabelece uma falha terminal.
O broker move para a DLQ no recebimento após as cinco tentativas; o consumidor
não publica uma cópia nem apaga a mensagem com erro.

O Fx encerra HTTP e consumidor antes de fechar o pool. No shutdown, cancela
long polling e processamento em curso, reverte trabalho não confirmado e libera
a visibilidade dos recebimentos. Uma confirmação ambígua é resolvida pela inbox
na reentrega. Os logs registram IDs, estado e códigos, sem corpos ou credenciais.
O `/metrics`, protegido pela permissão interna de leitura, inclui contadores
`jungle_sqs_{received,completed,inbox_replayed,retried,failed_delivery,delete_failed}_total`.
São contadores por entrega/processo; não representam diretamente o número de
operações financeiras únicas. A publicação da outbox está descrita na próxima seção.

## Publisher da outbox

O fluxo agora é: HTTP/SQS confirma a operação no PostgreSQL; depois, um worker
independente envia o snapshot da outbox para `wallet-events.fifo`. Essa fila é
separada de `wager-transactions.fifo`, que recebe comandos financeiros.
O destino tem sua própria DLQ `wallet-events-dlq.fifo`, FIFO, visibilidade 30s,
retenção 4/14 dias e redrive após cinco recebimentos sem confirmação.

O hook cria `jungle-outbox-publisher`, que só envia para a fila de eventos e lê
metadados dessa fila/DLQ. Ele não pode consumir eventos nem acessar comandos.
`jungle-event-consumer` recebe/remove eventos, sem publicar ou acessar comandos.
As políticas são verificadas por `verify-iam.py`. Para preparar o publisher com
as credenciais locais, depois de aplicar a migration 10 e verificar IAM:

```sh
docker exec localstack-aws /opt/code/localstack/.venv/bin/python /etc/localstack/verify-iam.py
docker cp localstack-aws:/var/lib/localstack/jungle/consumer.env .env.sqs.local
docker cp localstack-aws:/var/lib/localstack/jungle/publisher.env .env.outbox.local
chmod 600 .env.sqs.local .env.outbox.local
set -a
source .env.sqs.local
source .env.outbox.local
set +a
SQS_ENABLED=true OUTBOX_ENABLED=true go run .
```

Ambos os arquivos são ignorados pelo Git e mantêm as funções desabilitadas até a
ativação explícita. As configurações do publisher são independentes: `OUTBOX_ENABLED`,
`OUTBOX_ENDPOINT`, `OUTBOX_REGION`, `OUTBOX_QUEUE_NAME`, `OUTBOX_DLQ_NAME`,
`OUTBOX_ACCESS_KEY_ID`, `OUTBOX_SECRET_ACCESS_KEY` e `OUTBOX_WORKERS` (padrão 2,
limite 1..16). No Compose, use `docker compose exec/cp localstack` nos comandos.
O serviço Go lê variáveis exportadas no terminal, sem carregar esses arquivos sozinho.

A migration 10 adiciona uma sequência imutável aos eventos. Para registros
anteriores, a ordem inicial é `(occurred_at,id)`. Depois, a sequência é alocada
nas inserções. Sob o lock financeiro por carteira, os registros de operações
posteriores são inseridos após o commit anterior. Cada publisher reserva o primeiro
evento ainda não publicado de uma carteira usando `FOR UPDATE SKIP LOCKED`, com
commit curto e token exclusivo daquela tentativa. Carteiras distintas avançam em
paralelo; um evento em backoff retém os posteriores daquela carteira.

A reserva expira em 30s. O envio tem timeout de 5s; a confirmação/limpeza SQL tem
prazo próprio de 3s. Nenhuma conexão ou lock SQL fica preso durante a chamada ao
broker. Apenas o dono da reserva ainda válida pode marcar `published_at`.
Um processo antigo não confirma ou adia o trabalho assumido por outro. Uma queda
antes do envio deixa o evento disponível após a expiração da reserva.

Se o broker confirmar e o processo cair antes da confirmação SQL, outro publisher
poderá enviar novamente o mesmo snapshot com o mesmo `eventId`. A garantia é
**pelo menos uma vez**. O `MessageGroupId` é o mesmo hash por carteira da entrada;
o `MessageDeduplicationId` é SHA-256 hexadecimal do `eventId` em UTF-8. A
[janela de deduplicação FIFO](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/FIFO-key-terms.html)
não substitui a deduplicação durável do assinante por `(consumerName,eventId)`.
Durante recuperação de uma reserva expirada, um emissor antigo pode chegar tarde:
o assinante precisa tratar duplicatas e conferir `walletVersion` ao manter projeções.

O corpo é o JSON persistido na outbox, sem consultar o saldo atual para reconstruí-lo.
Os quatro contratos versão 1 estão em
[docs/wallet-events.schema.json](docs/wallet-events.schema.json):

| Evento | Conteúdo |
| --- | --- |
| `WagerTransactionProcessed` | Resultado processado, tipo, Money, saldo e versão; inclui OPENING e LOSS |
| `WagerTransactionRejected` | Resultado rejeitado, `failureCode`, Money, saldo e versão |
| `WalletBalanceChanged` | Carteira, transação, direção, Money, saldos anterior/posterior e versão |
| `WagerTransactionPendingReference` | Operação e referência externa que ainda está sendo aguardada |

O envelope mantém `eventId`, `eventType`, `aggregateId`, `correlationId`, `causationId`
opcional, `occurredAt` UTC, `version=1` e `data` tipado. Atributos SQS adicionais
`eventType`, `version` e `publicationSequence` permitem roteamento e auditoria.
O assinante deve usar o tipo/versão para escolher o handler e confirmar sua própria
inbox e efeito durável antes de `DeleteMessage`. A fila de saída comunica resultados;
seu consumidor pertence à integração que utiliza esses eventos.

Falha de publicação mantém o evento no PostgreSQL, com backoff de 1, 2, 4, 8, 16,
32 e no máximo 60s entre tentativas, sem descartá-lo. Credenciais ou payload
incorretos exigem correção operacional; não são marcados como publicados.
A DLQ de saída trata falhas de consumo após a entrega ao SQS, não falhas de envio.
O shutdown cancela novos claims e o envio em curso, confirma um envio conhecido
ou libera a reserva para outra instância. O pool fecha depois dos workers.
Payload, identidade, sequência e ocorrência são imutáveis; `published_at` e demais
metadados tornam-se imutáveis após a confirmação. O down da migration recusa
remover histórico de tentativas/reservas/publicação.

`/metrics` inclui `jungle_outbox_{claimed,recovered,published,publish_failures,lease_lost}_total`.
A cada cinco segundos, o worker atualiza `jungle_outbox_pending`,
`jungle_outbox_oldest_pending_age_seconds` e
`jungle_outbox_backlog_last_refresh_timestamp_seconds`. Esses gauges só aparecem
após uma consulta bem-sucedida; o timestamp permite detectar informação antiga.
São métricas por processo; várias instâncias observam o mesmo backlog e não se
deve somar esses gauges entre elas. Logs registram IDs/códigos sem payload financeiro.

## Banco local

Na raiz do projeto:

```sh
docker compose up -d postgres
docker compose ps
```

O PostgreSQL fica disponível no Mac em `localhost:5433`, banco `jungle`, usuário
`postgres` e senha `postgres_local`. São credenciais de exemplo para desenvolvimento.
Os dados ficam no volume `postgres_data`.

## Keycloak local

Inicie o provedor de identidade e aguarde a importação automática do realm:

```sh
docker compose up -d --wait keycloak
```

O Compose inicia também `keycloak-db`, um PostgreSQL exclusivo do Keycloak, sem
porta publicada. Seus dados e chaves ficam no volume `keycloak_postgres_data`.
O banco financeiro continua separado no volume `postgres_data`.

| Configuração | Valor local |
| --- | --- |
| Console administrativo | `http://localhost:8081/admin/` |
| Administrador de exemplo | `admin` / `admin_local` |
| Realm | `jungle` |
| Issuer | `http://localhost:8081/realms/jungle` |
| Cliente interno | `jungle-internal` |
| Secret de exemplo | `jungle_internal_local` |
| Audiência da API | `jungle-api` |
| Permissões do cliente interno | `wallets:read`, `wallets:write` |
| Cliente do provedor A | `jungle-provider-a` |
| Secret de exemplo do provedor A | `jungle_provider_a_local` |
| Permissões do provedor | `wagering:write` para envio e `wagering:read` para consulta |

As credenciais são exemplos para este ambiente local. Os valores podem ser
configurados pelas variáveis `KEYCLOAK_ADMIN_USER`, `KEYCLOAK_ADMIN_PASSWORD`,
`KEYCLOAK_DB_PASSWORD`, `KEYCLOAK_INTERNAL_CLIENT_SECRET` e
`KEYCLOAK_PROVIDER_A_CLIENT_SECRET`, descritas em `.env.example`.
O Compose lê `.env` automaticamente; o processo Go usa as variáveis exportadas
no terminal, como explicado abaixo.

O arquivo [docker/keycloak/jungle-realm.json](docker/keycloak/jungle-realm.json)
configura uma conta de serviço com `client_credentials`. Não há cadastro de senha
de jogador nem emissão de token pela aplicação Go. Para obter um token local:

```sh
curl --fail-with-body --silent --show-error \
  http://localhost:8081/realms/jungle/protocol/openid-connect/token \
  --data-urlencode 'grant_type=client_credentials' \
  --data-urlencode 'client_id=jungle-internal' \
  --data-urlencode "client_secret=${KEYCLOAK_INTERNAL_CLIENT_SECRET:-jungle_internal_local}"
```

O JSON contém `access_token`, `token_type` e `expires_in`. O token expira em até
5 minutos. A configuração OIDC pode ser consultada em
`http://localhost:8081/realms/jungle/.well-known/openid-configuration`.

A importação na inicialização ignora um realm já existente. Alterar o JSON ou
uma variável de bootstrap não atualiza automaticamente clientes ou credenciais
já persistidos; nesse caso, faça a alteração no console administrativo e mantenha
a configuração de bootstrap sincronizada. O ambiente usa `start-dev` e HTTP local.

Se o realm já existia antes da inclusão de `BET`, acrescente o provedor sem recriar
o ambiente, usando Python 3 e as credenciais administrativas locais:

```sh
python3 scripts/configure_provider.py
```

O script cria o cliente e a permissão ausentes e atribui o papel à conta de serviço
e ao escopo do cliente. Não altera os secrets de clientes existentes nem concede
permissões internas ao provedor. Para emitir seu token, use o mesmo comando de
`client_credentials` acima com `client_id=jungle-provider-a` e o secret do provedor.

O health check é público. A abertura e a consulta de carteiras exigem o token do
cliente interno e a permissão correspondente, antes de executar seus handlers.

## Migrations

O serviço `migrate` usa `golang-migrate` v4.20.1. Ele aguarda o healthcheck do
PostgreSQL e acessa os arquivos da pasta `migrations` em modo somente leitura.
O perfil `tools` permite executá-lo sob demanda; o nome explícito no comando `run`
ativa esse serviço.

Aplicar as migrations pendentes:

```sh
docker compose run --rm migrate up
```

Consultar a versão atual:

```sh
docker compose run --rm migrate version
```

Reverter apenas a última migration:

```sh
docker compose run --rm migrate down 1
```

O `down` da primeira migration remove a tabela `wallets` e seus dados. O comando
`down 1` é destinado a uma reversão deliberada; não faz parte da inicialização.
O `down` da segunda remove transações de abertura, ledger e outbox, preservando
as carteiras; por isso, também não deve ser usado em dados financeiros que precisam
ser mantidos.

Para reaplicar após uma reversão, execute `up` novamente. A tabela
`public.schema_migrations` registra a versão atual e o estado `dirty`. Uma execução
bem-sucedida das onze migrations deixa `version = 11` e `dirty = false`. Repetir
`up` nessa situação informa `no change`.

Dentro da rede do Compose, a ferramenta usa `postgres:5432`; a porta `5433` é para
conexões a partir do Mac. Uma migration já aplicada deve ser evoluída por um novo
par de arquivos numerados, preservando os arquivos que já foram executados.

A terceira migration acrescenta `BET`, seus metadados externos, unicidade de
idempotência e proteção das movimentações de carteira. Seu `down` recusa a reversão
quando existem transações externas, preservando o histórico financeiro. Nesse caso,
evolua o schema com uma nova migration em vez de remover operações confirmadas.

A quarta migration acrescenta `WIN`, referências e sua retomada durável. Seu
`down` também recusa reverter quando existe qualquer histórico de `WIN`.

A quinta acrescenta `LOSS`, permitindo zero somente nesse tipo externo e mantendo
a versão da carteira. Seu `down` recusa reverter quando existe histórico de LOSS.

A sexta acrescenta `REFUND`, exige referência e valor integral da BET, e impede
duas devoluções confirmadas para a mesma aposta. Seu `down` também preserva qualquer
histórico de REFUND, inclusive pendências.

A sétima acrescenta `ROLLBACK`, valida a direção inversa da referência no banco
e preserva a mesma proteção de devolução única por transação. O `down` recusa
reverter quando existe qualquer histórico ROLLBACK.

A oitava acrescenta o índice de leitura do extrato por carteira e versão financeira.
Seu `down` remove somente esse índice; preserva todos os registros.

## Executar a aplicação Go

Prepare o PostgreSQL, aplique as migrations e inicie o Keycloak conforme as etapas
acima. Copie o exemplo de configuração local e exporte as variáveis na sessão do
terminal:

```sh
cp .env.example .env
set -a
source .env
set +a
go run .
```

O arquivo `.env` é ignorado pelo Git e é carregado explicitamente pelo terminal.

| Variável | Significado | Padrão |
| --- | --- | --- |
| `DATABASE_URL` | Conexão do PostgreSQL | Obrigatória |
| `DATABASE_CONNECT_TIMEOUT` | Prazo para validar a conexão na inicialização | `5s` |
| `DATABASE_MAX_CONNS` | Máximo de conexões por instância da aplicação | `10` |
| `HTTP_ADDR` | Endereço de escuta HTTP | `127.0.0.1:8080` |
| `HTTP_SHUTDOWN_TIMEOUT` | Prazo de encerramento das requisições HTTP | `5s` |
| `OIDC_ISSUER_URL` | Issuer exato usado no discovery e nos tokens | `http://localhost:8081/realms/jungle` |
| `OIDC_AUDIENCE` | Destinatário obrigatório do access token | `jungle-api` |
| `OIDC_INTERNAL_CLIENT_ID` | Cliente autorizado a operar carteiras | `jungle-internal` |
| `OIDC_HTTP_TIMEOUT` | Prazo das consultas de discovery e JWKS | `3s` |
| `OIDC_PROVIDER_CLIENTS` | Objeto JSON que vincula `azp` a `providerId` | `{"jungle-provider-a":"provider-a"}` |

A aplicação usa Uber Fx para compor configuração, logger JSON e módulo PostgreSQL.
Na inicialização, `Ping` confirma uma conexão real e o servidor abre a porta HTTP.
`Ctrl+C` ou `SIGTERM` encerra primeiro o servidor HTTP, aguardando as requisições
ativas dentro do prazo, e depois fecha o pool. A inicialização falha quando a
configuração é inválida, o PostgreSQL ou o discovery OIDC não pode ser acessado,
ou a porta está ocupada.
A URL e a senha do banco não são registradas nos logs.

Com a aplicação em execução, em outro terminal:

```sh
curl -i http://127.0.0.1:8080/health/live
```

A resposta é HTTP `200`, JSON `{"status":"ok"}` e `Cache-Control: no-store`.
Esse endpoint indica somente que o processo responde.
O contrato atual está em [docs/openapi.yaml](docs/openapi.yaml).

Para verificar o endpoint com as quatro asserções salvas do Postman CLI:

```sh
postman request GET http://127.0.0.1:8080/health/live \
  --no-report-events --timeout 5000 \
  --script-post-request @postman/liveness.assertions.js
```

O endpoint público `GET /health/ready` verifica as dependências habilitadas:

```sh
curl -i http://127.0.0.1:8080/health/ready
```

Quando PostgreSQL, consumidor SQS e publisher estão acessíveis, responde:

```json
{"status":"ready","checks":{"postgres":"ok","sqsConsumer":"ok","sqsPublisher":"ok"}}
```

A resposta é `200` quando todas as dependências necessárias passam; falha ou timeout
retorna `503` com `status=not_ready` e o componente afetado como `unavailable`.
SQS desabilitado por configuração aparece como `disabled` e não bloqueia a
prontidão do modo HTTP. PostgreSQL é sempre obrigatório.

As três verificações rodam em paralelo com prazo total de 2s, incluindo espera
por conexão do pool. Cada chamada usa `Ping` no PostgreSQL e `GetQueueAttributes`
nas filas/DLQs habilitadas. Confere FIFO, visibilidade e ligação de redrive sem
alterar URLs compartilhadas com os workers. Não recebe/publica/remove mensagens,
não movimenta saldo nem escreve inbox/ledger/outbox. Não testa permissões de envio
ou consumo separadamente, a execução futura dos workers, migrations ou Keycloak.
Erros internos, endpoints e credenciais não aparecem na resposta ou no log HTTP.
A resposta também usa `Cache-Control: no-store` e independe de token.

O liveness continua retornando `200` quando uma dependência falha. Readiness é uma
observação atual; não interrompe os workers nem impede chamadas financeiras por si só.
Para validar status, corpo e cabeçalhos com as asserções reutilizáveis:

```sh
postman request GET http://127.0.0.1:8080/health/ready \
  --no-report-events --timeout 5000 \
  --script-post-request @postman/readiness.assertions.js
```

O script aceita `200/ready` ou `503/not_ready` e verifica coerência com os checks.
Os testes Go exercitam falhas, cancelamento e prazo global. A integração confirma
saturação/fechamento/recuperação do pool real e degradação/recuperação de filas no
LocalStack com IAM. Também verifica chamadas concorrentes sem consumir a mensagem
presente na fila de teste. Os testes não alteram as filas da aplicação.

## Middleware de autenticação

`internal/infrastructure/auth` valida assinatura RS256, issuer, audiência e
expiração com `go-oidc`. Também exige um access token Keycloak (`typ: Bearer`),
subject e cliente preenchidos e instantes de emissão/not-before coerentes. A
tolerância para emissão ou not-before no futuro é de 30 segundos; expiração
continua sendo verificada. HTTP é aceito no issuer somente em loopback local.

`internal/infrastructure/http/authentication.go` recebe tokens exclusivamente em
um único header `Authorization: Bearer <token>`. Cookies e query strings não são
usados para autenticação. Os papéis são lidos apenas de
`resource_access[OIDC_AUDIENCE].roles`.

As rotas de carteira usam `RequireInternalPermission("wallets:read")` ou
`RequireInternalPermission("wallets:write")`. Esse middleware autentica, compara o
`azp` com `OIDC_INTERNAL_CLIENT_ID` e exige o papel antes de executar o handler.
`AuthenticatedPrincipal` permite consultar a identidade já verificada sem expor
o token bruto. `Authenticate()` fica disponível para rotas que precisem somente
da autenticação; suas regras de autorização devem ser adicionadas explicitamente.

| Resultado | HTTP | Corpo |
| --- | --- | --- |
| Credencial ausente ou inválida | `401` | `{"error":"unauthorized"}` |
| Cliente ou papel sem autorização | `403` | `{"error":"forbidden"}` |
| Consulta necessária ao IdP falha ou expira | `503` | `{"error":"authentication_unavailable"}` |

Respostas `401`/`403` incluem `WWW-Authenticate`. As respostas de erro não expõem
tokens ou detalhes da biblioteca. As chaves públicas são compartilhadas em cache:
um token com chave conhecida pode continuar sendo verificado localmente durante
uma falha do IdP; uma chave desconhecida exige consulta e nunca libera acesso se
essa consulta falhar. O verificador descobre o JWKS somente na origem do issuer,
respeita timeout/cancelamento e fecha seus recursos depois do HTTP e antes do banco.

## Abertura e consulta de carteiras

| Rota | Permissão | Sucesso |
| --- | --- | --- |
| `POST /wallets` | `wallets:write` | `201`, carteira e header `Location` |
| `GET /wallets/{walletId}` | `wallets:read` | `200`, carteira |

Use um `access_token` emitido pelo Keycloak na variável `ACCESS_TOKEN`:

```sh
curl -i http://127.0.0.1:8080/wallets \
  -H "Authorization: Bearer ${ACCESS_TOKEN}" \
  -H 'Content-Type: application/json' \
  --data '{"playerId":"player-1","initialBalance":{"amount":"100.00","currency":"BRL"}}'
```

A resposta contém `id`, `playerId`, `balance` (`amount` e `currency`) e `version`.
Para consultar, faça `GET` no endereço retornado em `Location`, com o mesmo header
de autenticação. A abertura persiste os registros financeiros antes de retornar
sucesso; saldo zero cria somente a carteira. Uma combinação de jogador e moeda
já existente retorna `409`; a consulta de ID inexistente retorna `404`.

O corpo da abertura aceita somente os campos do exemplo e tem limite de 16 KiB.
Valores monetários devem ser strings com duas casas; números JSON, campos
duplicados, campos desconhecidos, campos ausentes e JSON extra são rejeitados.
Falhas de formato retornam `400`, corpo grande `413`, tipo de conteúdo diferente
de `application/json` `415`, indisponibilidade de persistência `503` e falhas
internas `500`. Os erros não expõem SQL ou credenciais. Uma falha de comunicação
durante o commit pode deixar seu resultado incerto; `503` não garante rollback.

## Apostas BET

`POST /wagering/transactions` exige token do provedor com `wagering:write` e um
vínculo autorizado em `OIDC_PROVIDER_CLIENTS`. O cliente interno não pode apostar,
e o provedor não pode acessar as rotas internas de carteira. O `providerId` do
corpo deve coincidir com o vínculo do cliente autenticado antes de qualquer replay.

Com o token de `jungle-provider-a` na variável `PROVIDER_ACCESS_TOKEN` e uma carteira
já aberta para o jogador:

```sh
curl -i http://127.0.0.1:8080/wagering/transactions \
  -H "Authorization: Bearer ${PROVIDER_ACCESS_TOKEN}" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: provider-a:bet-1' \
  --data '{"providerId":"provider-a","externalTransactionId":"bet-1","playerId":"player-1","walletId":"SUBSTITUA_PELO_ID_DA_CARTEIRA","roundId":"round-1","gameId":"game-1","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}'
```

| Situação | HTTP | Resultado |
| --- | --- | --- |
| Débito confirmado | `200` | `PROCESSED`, saldo observado e `idempotentReplay: false` |
| Saldo insuficiente | `422` | `REJECTED`, `failureCode: INSUFFICIENT_FUNDS`, saldo preservado |
| Mesma chave e conteúdo | `200` ou `422` | Resultado original, `idempotentReplay: true` |
| Chave com outro conteúdo | `409` | `idempotency_conflict` |
| Mesmo ID externo com outra chave | `409` | `external_transaction_conflict` |
| Entrada inválida ou carteira incompatível com jogador/moeda | `400` | Nenhuma transação aceita |
| Carteira inexistente | `404` | Nenhuma transação aceita |
| Falha transitória/timeout | `503` | Repetir a mesma chave e conteúdo |
| Replay de falha definitiva de infraestrutura | `500` | `FAILED/INFRASTRUCTURE_RETRIES_EXHAUSTED`, resultado persistido, sem reaplicar |

A chave fornecida é preservada. O replay devolve o saldo da aposta original mesmo
após outras apostas. Uma rejeição confirmada também é definitiva: reenvios não
tentam novamente o débito. Dados monetários equivalentes, como `00025.00` e
`25.00`, produzem o mesmo hash. Identificadores são preservados e limitados a 256
bytes; nomes vazios ou só com espaços são inválidos. O corpo tem limite de 16 KiB.
Em `BET`, zero, negativos, `OPENING`, outros tipos e referências são rejeitados.

O PostgreSQL bloqueia somente a carteira envolvida com `SELECT FOR UPDATE`.
Resultado terminal, saldo, versão, ledger e eventos são confirmados juntos. Uma
aprovação cria `WagerTransactionProcessed` e `WalletBalanceChanged`; uma rejeição
cria somente `WagerTransactionRejected`. Os eventos são gravados na outbox e enviados pelo publisher independente. O processamento SQL tem prazo de 5 segundos. Uma falha de
comunicação no commit pode deixar seu resultado incerto; a repetição com a mesma
chave consulta o resultado confirmado ou processa uma tentativa que sofreu rollback.

## Prêmios WIN

Use a mesma rota, autenticação e chave de idempotência, com `kind: "WIN"` e
`externalTransactionId` próprio. Um `WIN` positivo credita o prêmio, incrementa a
versão, grava ledger `CREDIT` e os eventos `WagerTransactionProcessed` e
`WalletBalanceChanged` no mesmo commit. Zero é inválido. Se a soma exceder o limite
de `int64`, retorna `422`, `REJECTED` e `failureCode: BALANCE_OVERFLOW`, sem crédito.
Essa rejeição é persistida e definitiva, inclusive se o saldo diminuir depois.

Opcionalmente acrescente `"referenceExternalTransactionId":"bet-1"`. A referência
é resolvida dentro do provedor autenticado e deve ser uma `BET` processada do mesmo
jogador, carteira, moeda, rodada e jogo. O valor do prêmio pode ser diferente do
valor apostado. Referência incompatível retorna `REFERENCE_MISMATCH`; aposta
rejeitada retorna `REFERENCE_REJECTED`. Ambos são resultados auditáveis com HTTP 422.
Campo ausente significa prêmio sem referência; null, string vazia e autorreferência
são entradas inválidas. A referência, quando presente, participa do hash.

Se a referência não chegou ou ainda está pendente, a API retorna `202` com
`status: PENDING_REFERENCE`, sem movimentação financeira. A pendência e seu evento
`WagerTransactionPendingReference` são confirmados juntos. Repita a mesma requisição
e chave para acompanhar: o mesmo `transactionId` passa a retornar `200` após o
crédito ou `422` após uma rejeição definitiva. O saldo de uma resposta `202` é
somente o observado na última tentativa, não uma confirmação de crédito.

O worker inicia com o Fx e consulta o PostgreSQL a cada segundo, sem depender
de SQS. Divide pendências entre instâncias com `FOR UPDATE SKIP LOCKED` e bloqueia
somente a carteira envolvida. A primeira tentativa ocorre após 1s; novas esperas
usam backoff de 2s, 4s, 8s, 16s, 30s e limite de 30s. Após 8 tentativas ou TTL de
5 minutos, uma referência ainda indisponível vira `REJECTED/REFERENCE_NOT_FOUND`.
Datas e tentativas ficam no banco, permitindo retomada por outro processo.

Na parada, o HTTP encerra antes do worker e o pool fecha depois dele. Trabalho
interrompido sofre rollback e permanece disponível; erros de banco não consomem
uma tentativa confirmada. BET e WIN compartilham as constraints de idempotência:
trocar o tipo usando a mesma chave é conflito, sem nova movimentação.

## Resultados LOSS

Na mesma rota, envie `kind: "LOSS"` com `money: {"amount":"0.00","currency":"BRL"}`
e identificadores/chave próprios. A moeda deve coincidir com a carteira, mesmo com
valor zero. LOSS não exige saldo disponível e não aceita referência. Valores
positivos, negativos, números JSON e formatos monetários inválidos retornam `400`.
Zeros à esquerda são normalizados, como nos demais tipos.

O resultado retorna `200`, `PROCESSED`, saldo observado e `idempotentReplay`. A
transação e somente `WagerTransactionProcessed` são confirmados juntos. Não há
ledger, `WalletBalanceChanged`, alteração de saldo, versão ou timestamps da carteira.
Podem existir vários LOSS distintos na mesma versão. O débito já pertence ao BET;
registrar a derrota não debita novamente.

Replays consultam o resultado persistido, incluindo o saldo original mesmo após
outras movimentações. Chaves e IDs externos compartilham a unicidade de BET/WIN:
trocar tipo ou conteúdo com a mesma chave retorna conflito. A autorização do
provedor é aplicada antes da consulta de replay. A operação usa lock por carteira
para obter um snapshot coerente com débitos/créditos concorrentes. Um WIN que
referencie um LOSS é rejeitado com `REFERENCE_MISMATCH`, pois LOSS não é uma BET.

## Devoluções REFUND

Na mesma rota, envie os identificadores da carteira, jogador, provedor, rodada e
jogo, uma chave e ID externo próprios, `kind: "REFUND"`, `money` com o valor
integral da aposta e `referenceExternalTransactionId` com o ID externo da BET:

```json
{
  "providerId": "provider-a",
  "externalTransactionId": "refund-1",
  "walletId": "<ID da carteira>",
  "playerId": "player-1",
  "roundId": "round-1",
  "gameId": "game-1",
  "kind": "REFUND",
  "money": {"amount": "25.00", "currency": "BRL"},
  "referenceExternalTransactionId": "bet-1"
}
```

A referência é obrigatória e deve ser uma BET processada do mesmo provedor,
jogador, carteira, moeda, rodada e jogo. O crédito devolve o valor integral,
incrementa a versão e confirma transação, ledger e dois eventos na mesma transação
SQL. A resposta é `200/PROCESSED`. Não há devolução parcial: um valor diferente
produz `422/REFERENCE_AMOUNT_MISMATCH`, sem crédito.

Repetir a mesma chave e conteúdo retorna o resultado original. Outra operação
para a mesma BET recebe `422/ALREADY_REVERSED`; a unicidade também existe no banco
para proteger instâncias independentes. Chaves e IDs externos são compartilhados
por BET, WIN, LOSS e REFUND, dentro de cada provedor.

Se a BET ainda não chegou, o REFUND retorna `202/PENDING_REFERENCE`, sem crédito,
e usa o mesmo worker, backoff, limite de 8 tentativas e TTL de 5 minutos do WIN.
Repita a mesma requisição para acompanhar o resultado. Referência incompatível ou
rejeitada produz `422`; esgotamento gera `REFERENCE_NOT_FOUND`. Campo de referência
ausente, vazio, null, autorreferência ou valor não positivo resulta em `400`.

A política de reversões permite uma única devolução confirmada por transação
referenciada, compartilhando a mesma restrição com ROLLBACK de BET. Não se
pode somar REFUND e ROLLBACK para devolver duas vezes a mesma aposta. O estorno de REFUND é uma nova operação
de débito referenciando o próprio REFUND, mantendo o histórico imutável.

## Estornos ROLLBACK

Na mesma rota e com a mesma autenticação, envie `kind: "ROLLBACK"`, chave e ID
externo próprios, `money` com valor positivo igual ao da operação original e
`referenceExternalTransactionId` obrigatório. A referência precisa ser BET, WIN
ou REFUND processado do mesmo provedor, jogador, carteira, moeda, rodada e jogo.

- ROLLBACK de BET devolve o débito por um crédito integral.
- ROLLBACK de WIN retira o prêmio por um débito integral.
- ROLLBACK de REFUND retira a devolução por um débito integral.

O sucesso retorna `200/PROCESSED`, incrementa a versão e confirma resultado,
carteira, ledger e dois eventos na mesma transação SQL. A operação original
permanece imutável. LOSS e ROLLBACK não são referências válidas.

Se faltar saldo para desfazer um crédito, a operação termina em
`422/REVERSAL_INSUFFICIENT_FUNDS`, com evento de rejeição e sem movimentação.
Esse código é diferente do `INSUFFICIENT_FUNDS` de BET. Repetir a mesma chave
preserva a rejeição mesmo depois de um novo crédito; uma nova solicitação com
outros identificadores pode tentar novamente, pois uma rejeição não reserva a
referência. Um estorno parcial é rejeitado com `REFERENCE_AMOUNT_MISMATCH`.

Referências ainda ausentes ou pendentes produzem `202/PENDING_REFERENCE` e são
retomadas pelo mesmo worker durável, com backoff, 8 tentativas e TTL de 5 minutos.
O replay consulta o resultado persistido. Outra reversão de uma referência já
desfeita recebe `ALREADY_REVERSED`. REFUND e ROLLBACK de uma BET competem pela
mesma unicidade; desfazer o REFUND não libera a aposta para nova devolução.

## Consultas de transações, extrato e reconciliação

Os contratos completos estão em [docs/openapi.yaml](docs/openapi.yaml).

| Método e rota | Permissão | Resultado |
| --- | --- | --- |
| `GET /wagering/transactions/:transactionId` | Provedor com `wagering:read` ou interno com `wallets:read` | Estado e resultado financeiro persistido |
| `GET /providers/:providerId/wagering/transactions/:externalTransactionId` | Mesma autorização | Busca pelo par provedor/ID externo |
| `GET /wallets/:walletId/ledger?limit=50&cursor=...` | Interno com `wallets:read` | Extrato com cursor opaco |
| `POST /wallets/:walletId/reconciliation` | Interno com `wallets:read` | Confere saldo com o ledger; sem corpo |
| `GET /metrics` | Interno com `wallets:read` | Contadores de reconciliação em formato Prometheus |

Atualize um Keycloak local já inicializado com `python3 scripts/configure_provider.py`
e obtenha um token novo do provedor. O script acrescenta `wagering:read` sem recriar
o realm nem modificar secrets. A permissão de envio é independente da de leitura.

Um provedor só consulta suas transações. ID interno de outro provedor ou de uma
abertura interna retorna `404/transaction_not_found`; `providerId` de outra
identidade na rota externa retorna `403` antes da busca. O serviço interno pode
consultar todas, inclusive OPENING. A consulta devolve tipo, estado, Money, saldo
observado na execução, versão, timestamps e referências/código de rejeição quando
aplicáveis. Não expõe chave de idempotência ou hash. Use GET para acompanhar
`PENDING_REFERENCE`; a resposta continua `200` enquanto o campo `status` muda pelo
worker. As consultas nunca reaplicam movimentos e usam `Cache-Control: no-store`.

O extrato inclui abertura positiva e todos os lançamentos confirmados, em ordem
crescente de `walletVersion` e ID. LOSS, rejeições e operações ainda pendentes não
criam ledger. A resposta tem `walletId`, `snapshotVersion`, `entries` e, quando
houver outra página, `nextCursor`. Sem lançamentos, `entries` é `[]`.

A primeira página captura a versão superior da carteira. Envie o `nextCursor`
recebido na próxima chamada; ele preserva esse limite e pertence à mesma carteira.
Movimentações posteriores aparecem ao iniciar uma navegação nova. Cada página lê
carteira e ledger em uma transação somente leitura com REPEATABLE READ. O cursor
é Base64URL de uma estrutura versionada e canônica; é opaco por contrato, não é
uma credencial ou segredo. O servidor valida formato, carteira, posição e limites.
`limit` aceita 1 a 100, padrão 50; parâmetros duplicados/desconhecidos, cursor
inválido ou pertencente a outra carteira resultam em `400`.

A reconciliação executa uma única consulta SQL para enxergar saldo e ledger na
mesma visão consistente, mesmo com escritores concorrentes. Soma créditos menos
débitos, incluindo OPENING, e retorna:

```json
{
  "walletId": "<ID da carteira>",
  "storedBalance": {"amount": "75.00", "currency": "BRL"},
  "calculatedBalance": {"amount": "75.00", "currency": "BRL"},
  "difference": {"amount": "0.00", "currency": "BRL"},
  "consistent": true,
  "checkedEntries": 2
}
```

`difference` é saldo armazenado menos saldo calculado. PostgreSQL NUMERIC e
inteiros de precisão arbitrária evitam overflow nas somas e na auditoria de
valores corrompidos; a resposta preserva strings decimais, inclusive diferenças
negativas. Entradas e saldos financeiros continuam em Money/int64. A auditoria
não corrige o saldo. Divergência retorna `200` com `consistent=false`, escreve um
log sem valores financeiros ou credenciais e incrementa
`jungle_reconciliation_divergences_total`. `jungle_reconciliation_checks_total`
conta reconciliações concluídas. Os contadores são por processo e reiniciam com
a instância. Os contadores do consumidor SQS também estão disponíveis nessa rota.

## Testes

```sh
go test ./...
go test -race ./...
go vet ./...
```

Os testes padrão validam domínio, configuração, roteamento e ciclo de vida HTTP
sem exigir banco. Os testes HTTP abrem portas locais temporárias. Para testar a
composição Fx e o pool com o PostgreSQL real, primeiro inicie o banco e aplique as
migrations. Depois execute:

```sh
TEST_DATABASE_URL='postgres://postgres:postgres_local@localhost:5433/jungle?sslmode=disable' go test -race -tags=integration ./...
```

Os testes de integração verificam consulta real, presença da tabela `wallets`,
limite de conexões, fechamento do pool e rejeição de senha incorreta sem exposição
de credenciais nos logs. Também verificam o `INSERT` de carteiras, precisão dos
valores persistidos, conflitos de unicidade, cancelamento e rollback. Os dados
criados pelos testes do repositório ficam em transações revertidas ao final.
A busca por ID também é testada com preservação de versão e timestamps, carteira
inexistente, estado inválido e isolamento de dados ainda não confirmados.

Os testes de `OpeningStore` criam schemas temporários, aplicam as migrations e
confirmam transações reais. Verificam saldo zero, valor máximo de `int64`, eventos
com dinheiro em strings, conflitos concorrentes, proteção do ledger e rollback
em falhas de inserção e commit. Também verificam timeout durante uma espera por
lock e o ciclo `up/down/up` da segunda migration. O usuário de teste precisa poder
criar e remover esses schemas; a limpeza não remove dados das tabelas da aplicação.

O `OpeningStore` e o `OpenWallet` estão registrados no Fx. Os eventos ficam
registrados na outbox e são publicados pelo worker quando `OUTBOX_ENABLED=true`.
`wager_transactions` aceita `INTERNAL/OPENING` e `EXTERNAL/BET/WIN/LOSS/REFUND/ROLLBACK`. Os cinco tipos externos estão implementados.

Os testes de HTTP verificam o contrato do health check, `404` e `405`, recovery sem
exposição de credenciais, porta ocupada, espera por requisições ativas e fechamento
forçado após timeout. Uma falha inesperada do listener solicita encerramento pelo
Fx. O teste da aplicação completa verifica que o HTTP encerra antes do pool.

Com o Keycloak saudável, execute também:

```sh
go test -race -tags=keycloak ./tests/keycloak
```

Esses testes usam o IdP real e verificam discovery, emissão por `client_credentials`,
assinatura RSA usando o JWKS, issuer, audiência, validade, permissões e rejeição de
secret incorreto e do grant de senha. Eles não imprimem tokens ou secrets.
Também exercitam o middleware real com tokens do Keycloak, incluindo `401`,
`403` e sucesso em uma rota registrada exclusivamente no teste. Se o secret foi
personalizado, exporte
`KEYCLOAK_INTERNAL_CLIENT_SECRET` antes de executá-los. O issuer de teste pode ser
sobrescrito por `TEST_KEYCLOAK_ISSUER_URL`.

Para testar as rotas pela composição completa da aplicação, com PostgreSQL e
Keycloak reais:

```sh
TEST_DATABASE_URL='postgres://postgres:postgres_local@localhost:5433/jungle?sslmode=disable' go test -race -tags=integration,keycloak ./tests/walletapi
```

Esse teste usa um schema descartável e verifica abertura, consulta, unicidade,
precisão monetária, rejeição sem autenticação e rollback de toda a abertura quando
a inserção de um evento falha. Também cobre `BET` com tokens reais do provedor,
replays de aprovação/rejeição, conflitos, saldo original e retry após rollback.
Configure o provedor local antes de executar; se necessário, exporte
`KEYCLOAK_PROVIDER_A_CLIENT_SECRET`. Os testes HTTP sem banco também cobrem o parser
estrito e o contrato de erros. A simulação OIDC verifica que um token somente de
leitura consulta carteiras e recebe `403` ao tentar abri-las, sem persistência.

Também testa WIN com Keycloak e PostgreSQL reais, incluindo o worker que retoma
um prêmio recebido antes da aposta referenciada. Os testes de domínio/banco cobrem
overflow, referências incompatíveis/rejeitadas, backoff persistente, TTL, limite de
tentativas, disputa entre workers e proteção do histórico. Um teste separado com
três processos disputa um WIN, um BET e uma duplicata, verificando crédito/débito
sem lost updates e replay após o término dos processos anteriores.

Os testes de LOSS verificam 50 duplicatas concorrentes com uma única transação e
evento, preservação integral da carteira, múltiplos resultados na mesma versão,
replay após BET, rollback em falhas da outbox/commit e proteções SQL. Há teste da
API com Keycloak/PostgreSQL reais e um cenário de três processos disputando LOSS,
BET e uma duplicata, seguido de replays em processos novos.

Os testes de REFUND verificam devolução integral, valor diferente, referências
inválidas, proteção contra crédito duplicado, replay após outra aposta, retomada
e expiração, rollback da outbox/commit e ciclo de migration com histórico anterior.
A API usa Keycloak/PostgreSQL reais. Três processos independentes disputam duas
devoluções da mesma BET e uma duplicata: um crédito, uma rejeição e um replay.
Novos processos reconstroem os mesmos resultados sem nova movimentação.

Os testes de ROLLBACK cobrem as três referências suportadas, débitos até saldo
zero, insuficiência com código próprio, overflow, valor parcial, referência
inválida/rejeitada/pendente, expiração e replay após outras movimentações.
A integração verifica atomicidade em falhas de outbox/commit, a direção inversa
imposta pelo PostgreSQL, o ciclo de migration e a preservação do histórico.
Três processos disputam REFUND, ROLLBACK e uma duplicata da mesma BET; apenas
uma devolução é confirmada, e novos processos preservam os resultados.
A API e o worker também são exercitados com Keycloak e PostgreSQL reais.

Os testes de integração de `BET` também verificam isolamento entre carteiras,
disputa da mesma chave em carteiras diferentes, cancelamento e constraints SQL.
Um teste inicia três processos independentes com pools próprios, disputa duas
apostas de 80.00 BRL sobre saldo de 100.00 BRL e confirma um débito, uma rejeição
e saldo de 20.00 BRL. Em seguida, encerra esses processos e usa três novos para
confirmar replays duráveis sem alterações financeiras.

Os testes de assinatura e claims com chaves estáticas fazem parte de `go test ./...`.
Para testar também chamadas HTTP ao IdP, cache, rotação e indisponibilidade, execute
a [simulação OIDC local](postman/mocks/keycloak/README.md). O fixture deriva do
contrato do Keycloak e não substitui a API Go. O middleware e o verificador reais
são executados contra esse IdP simulado; os testes com `-tags=keycloak` complementam
essa evidência com a implantação real.

Os testes de leitura cobrem os seis tipos persistidos, pendência/rejeição,
isolamento por provedor, resultado histórico, extrato vazio, ordem estável,
novos lançamentos entre páginas, limites e cursores de outra carteira.
A reconciliação é verificada com escritores concorrentes, somas além do limite
intermediário de int64 e divergência injetada somente em schemas descartáveis.
A API usa Keycloak/PostgreSQL reais; a simulação OIDC testa papéis de leitura e
escrita separados. Divergência é verificada na resposta, no log e na métrica.

Com LocalStack na porta 4566, PostgreSQL e Keycloak saudáveis, inclua a integração
SQS real:

```sh
TEST_DATABASE_URL='postgres://postgres:postgres_local@localhost:5433/jungle?sslmode=disable' go test -race -tags=integration,keycloak,sqs ./...
```

Os testes criam filas FIFO/DLQ e schemas descartáveis com identidade administrativa
local; não recebem mensagens das filas da aplicação. Exercitam os cinco tipos,
HTTP antes/depois de SQS, duplicatas, pendência, rejeição de negócio, binding de
provedor, conflito de inbox e redrive nativo de mensagens inválidas. Injeções de
falha verificam commit seguido de remoção indisponível, rollback na outbox,
recuperação e esgotamento com `FAILED` sem efeitos financeiros. O teste da inbox
com três processos independentes disputa entrada direta e envelopes sobre a mesma
carteira e recupera os resultados em processos novos. A fiscalização IAM é
verificada separadamente por `verify-iam.py`, com as credenciais restritas.

Os testes de FAILED financeiro cobrem os cinco tipos, rollback dos efeitos antes
da auditoria, replay com outro messageId e pelos stores HTTP, saldo histórico após
novas movimentações, conflitos de chave/ID externo, claims concorrentes entre pools,
referências imediatas e pendentes a uma operação FAILED e proteção do histórico.
A API real verifica POST 500 com o resultado persistido e GET 200 da auditoria;
o teste SQS verifica esgotamento e redrive nativo com FAILED na inbox e na transação.

Os testes do publisher exigem IAM fiscalizado e criam usuário/chave/política e filas
próprias, removidos ao final. Verificam a negação de consumo para a credencial de
publicação, os quatro tipos de evento e igualdade com o snapshot SQL. Processos
independentes são interrompidos após a reserva e após o envio; instâncias novas
recuperam os mesmos eventIds. Três publishers também disputam eventos, e uma
conexão recusada pelo broker mantém os eventos para retry. PostgreSQL real cobre
fencing, ordenação por carteira, paralelismo entre carteiras, backoff, imutabilidade
e ciclo da migration. O ciclo Fx cobre publicação, shutdown e retomada por nova instância.

As decisões de domínio estão em [ARCHITECTURE.md](ARCHITECTURE.md).
