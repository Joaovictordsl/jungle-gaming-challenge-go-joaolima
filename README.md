# Jungle Gaming — desafio backend Go

Projeto em desenvolvimento. A etapa atual contém `Money`, o construtor de carteira,
a primeira migration de PostgreSQL e uma aplicação Fx que valida e gerencia o pool
de conexões. HTTP, autenticação e mensageria serão implementados nas próximas etapas.

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

Para reaplicar após uma reversão, execute `up` novamente. A tabela
`public.schema_migrations` registra a versão atual e o estado `dirty`. Uma execução
bem-sucedida da primeira migration deixa `version = 1` e `dirty = false`. Repetir
`up` nessa situação informa `no change`.

Dentro da rede do Compose, a ferramenta usa `postgres:5432`; a porta `5433` é para
conexões a partir do Mac. Uma migration já aplicada deve ser evoluída por um novo
par de arquivos numerados, preservando os arquivos que já foram executados.

## Executar a aplicação Go

Prepare o PostgreSQL e aplique as migrations conforme as etapas acima. Copie o
exemplo de configuração local e exporte as variáveis na sessão do terminal:

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

A aplicação usa Uber Fx para compor configuração, logger JSON e módulo PostgreSQL.
Na inicialização, `Ping` confirma uma conexão real. O processo permanece ativo;
`Ctrl+C` ou `SIGTERM` inicia o encerramento e fecha o pool. Ainda não há servidor
HTTP nesta etapa. A inicialização falha quando a configuração é inválida ou o
PostgreSQL não pode ser acessado. A URL e a senha não são registradas nos logs.

## Testes

```sh
go test ./...
go test -race ./...
go vet ./...
```

Os testes padrão validam domínio e configuração sem exigir banco. Para testar a
composição Fx e o pool com o PostgreSQL real, primeiro inicie o banco e aplique as
migrations. Depois execute:

```sh
TEST_DATABASE_URL='postgres://postgres:postgres_local@localhost:5433/jungle?sslmode=disable' go test -race -tags=integration ./...
```

Os testes de integração verificam consulta real, presença da tabela `wallets`,
limite de conexões, fechamento do pool e rejeição de senha incorreta sem exposição
de credenciais nos logs. Eles usam consultas de leitura e não alteram as carteiras.

As decisões de domínio estão em [ARCHITECTURE.md](ARCHITECTURE.md).
