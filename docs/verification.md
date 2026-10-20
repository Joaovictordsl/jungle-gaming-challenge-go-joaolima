# Verificação da entrega

Verificado em 20/10/2026, com Go 1.27.2, Docker 29.0.1, PostgreSQL 18.6,
Keycloak 26.8.1 e LocalStack 2026.10.0 com IAM enforcement ativo.
Referência: [requisitos do desafio](https://github.com/junglegaming/backend-challenge-go).

## Verificações executadas

| Verificação | Resultado |
| --- | --- |
| Suíte completa com `-race` e tags `integration,keycloak,oidcsimulation,sqs` | Passou |
| `go vet` com todas essas tags | Passou |
| `gofmt` e `git diff --check` | Sem pendências |
| Sintaxe de `docs/openapi.yaml` via Postman CLI | Sem problemas |
| Testes padrão `go test -race -p=1 ./...` no clone limpo | Passaram |
| Instalação por `scripts/local-up.sh` com volumes/estado novos | Passou; migrations 1–11, realm/clientes, quatro filas e quatro identidades IAM |
| Verificação IAM no ambiente novo | Acessos permitidos e sete negações reais, sem alterar mensagens |
| `scripts/smoke.py` na API do clone limpo | Passou: OIDC, cinco operações, replay HTTP/SQS, ledger, reconciliação, métricas e outbox publicada |
| Readiness da API nova pelo Postman | HTTP 200, todos os checks habilitados em `ok`, quatro assertions aprovadas |
| Consumidor em processo independente encerrado após commit e antes de DeleteMessage | Passou com `-race`: reentrega nativa, inbox recuperada, um movimento e dois eventos |

Os testes de integração usam bancos, filas e identidades descartáveis. O smoke
cria um jogador/carteira exclusivos. Credenciais geradas e estado do simulador
ficam fora do Git. O clone começou e terminou com o código sem mudanças locais.

## Como repetir

Siga o início rápido no README. A partir das dependências preparadas:

```sh
go test -race ./...
go vet -tags=integration,keycloak,oidcsimulation,sqs ./...
postman spec lint docs/openapi.yaml --issueType=syntax
```

Inicie a simulação OIDC em outro terminal para incluir a tag `oidcsimulation`:

```sh
postman simulation run postman/simulations/oidc.sim.yaml --port 4900 --output ndjson --no-history
```

Então execute:

```sh
TEST_DATABASE_URL='postgres://postgres:postgres_local@localhost:5433/jungle?sslmode=disable' go test -race -tags=integration,keycloak,oidcsimulation,sqs ./...
```

O README detalha as tags e os testes por componente. Para máquinas com poucos
recursos, acrescente `-p=1` ao `go test`; as concorrências internas dos cenários
e os subprocessos continuam sendo executados.

## O que a instalação limpa revelou

O CLI LocalStack pode retornar antes de terminar os ready hooks. O script de
inicialização agora aguarda o health check de filas/IAM antes de copiar as
credenciais. A correção foi verificada iniciando outro estado vazio do simulador.

A primeira tentativa também ficou sem espaço no disco do host e interrompeu o
Docker e a criação dos bancos descartáveis. Após limpar o cache reconstruível do
Go e recuperar o daemon, esses bancos foram recriados e a validação passou.
Não houve alteração de código para contornar essa falha de ambiente.

## Limites declarados

O fluxo local usa Go no host e dependências em containers. Requer licença do
LocalStack com IAM enforcement e espaço para imagens, volumes e compilação.
Tracing, dashboards, testes de carga e partidas dobradas não foram implementados;
são diferenciais opcionais. Decisões e limites financeiros estão em ARCHITECTURE.md.
