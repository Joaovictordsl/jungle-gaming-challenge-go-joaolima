# Jungle Gaming — desafio backend Go

Projeto em desenvolvimento. A etapa atual contém `Money`, carteiras e o caso de uso
de abertura interna, com PostgreSQL e composição via Fx. Uma abertura positiva
confirma carteira, `OPENING`, crédito no ledger e dois eventos na outbox na mesma
transação. O servidor Gin expõe abertura e consulta de carteiras com autenticação
JWT/OIDC e um health check público. O Keycloak local emite tokens para o serviço
interno e para o provedor A. `BET` debita, `WIN` credita e `LOSS` registra o resultado
sem movimentar a carteira. `REFUND` devolve integralmente uma aposta processada,
com proteção contra devolução duplicada. As operações têm idempotência persistente.
`WIN` e `REFUND` com referência ausente aguardam de forma durável e são retomados
por um worker. `ROLLBACK` desfaz integralmente BET, WIN ou REFUND. Consultas de transações, extrato paginado e reconciliação estão disponíveis.
A mensageria está pendente.

## Pré-requisitos

- Go 1.27.2 ou superior.
- Docker com Docker Compose.

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
bem-sucedida das oito migrations deixa `version = 8` e `dirty = false`. Repetir
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
Esse endpoint indica somente que o processo responde; o readiness de PostgreSQL
e SQS será acrescentado posteriormente.
O contrato atual está em [docs/openapi.yaml](docs/openapi.yaml).

Para verificar o endpoint com as quatro asserções salvas do Postman CLI:

```sh
postman request GET http://127.0.0.1:8080/health/live \
  --no-report-events --timeout 5000 \
  --script-post-request @postman/liveness.assertions.js
```

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

A chave fornecida é preservada. O replay devolve o saldo da aposta original mesmo
após outras apostas. Uma rejeição confirmada também é definitiva: reenvios não
tentam novamente o débito. Dados monetários equivalentes, como `00025.00` e
`25.00`, produzem o mesmo hash. Identificadores são preservados e limitados a 256
bytes; nomes vazios ou só com espaços são inválidos. O corpo tem limite de 16 KiB.
Em `BET`, zero, negativos, `OPENING`, outros tipos e referências são rejeitados.

O PostgreSQL bloqueia somente a carteira envolvida com `SELECT FOR UPDATE`.
Resultado terminal, saldo, versão, ledger e eventos são confirmados juntos. Uma
aprovação cria `WagerTransactionProcessed` e `WalletBalanceChanged`; uma rejeição
cria somente `WagerTransactionRejected`. Os eventos permanecem na outbox, aguardando
o futuro publisher SQS. O processamento SQL tem prazo de 5 segundos. Uma falha de
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
a instância; métricas dos demais fluxos ainda serão acrescentadas.

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
pendentes na outbox: o publisher SQS ainda não foi implementado.
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

As decisões de domínio estão em [ARCHITECTURE.md](ARCHITECTURE.md).
