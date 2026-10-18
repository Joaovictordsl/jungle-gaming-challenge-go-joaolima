# Jungle Gaming — desafio backend Go

Projeto em desenvolvimento. A etapa atual contém `Money`, carteiras e o caso de uso
de abertura interna, com PostgreSQL e composição via Fx. Uma abertura positiva
confirma carteira, `OPENING`, crédito no ledger e dois eventos na outbox na mesma
transação. O servidor Gin expõe abertura e consulta de carteiras com autenticação
JWT/OIDC e um health check público. O Keycloak local emite tokens para o serviço
interno. Operações de apostas e mensageria serão implementadas nas próximas etapas.

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

As credenciais são exemplos para este ambiente local. Os valores podem ser
configurados pelas variáveis `KEYCLOAK_ADMIN_USER`, `KEYCLOAK_ADMIN_PASSWORD`,
`KEYCLOAK_DB_PASSWORD` e `KEYCLOAK_INTERNAL_CLIENT_SECRET`, descritas em `.env.example`.
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
bem-sucedida das duas migrations deixa `version = 2` e `dirty = false`. Repetir
`up` nessa situação informa `no change`.

Dentro da rede do Compose, a ferramenta usa `postgres:5432`; a porta `5433` é para
conexões a partir do Mac. Uma migration já aplicada deve ser evoluída por um novo
par de arquivos numerados, preservando os arquivos que já foram executados.

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
pendentes na outbox: o publisher SQS ainda não foi implementado. Nesta etapa,
`wager_transactions` aceita somente a origem interna `OPENING`; operações externas
serão introduzidas com seus metadados e constraints em uma nova migration.

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
a inserção de um evento falha. Os testes HTTP sem banco também cobrem o parser
estrito e o contrato de erros. A simulação OIDC verifica que um token somente de
leitura consulta carteiras e recebe `403` ao tentar abri-las, sem persistência.

Os testes de assinatura e claims com chaves estáticas fazem parte de `go test ./...`.
Para testar também chamadas HTTP ao IdP, cache, rotação e indisponibilidade, execute
a [simulação OIDC local](postman/mocks/keycloak/README.md). O fixture deriva do
contrato do Keycloak e não substitui a API Go. O middleware e o verificador reais
são executados contra esse IdP simulado; os testes com `-tags=keycloak` complementam
essa evidência com a implantação real.

As decisões de domínio estão em [ARCHITECTURE.md](ARCHITECTURE.md).
