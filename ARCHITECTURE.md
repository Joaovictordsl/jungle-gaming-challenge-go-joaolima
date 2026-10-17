# Decisões de arquitetura

## Money

`internal/domain/money` representa dinheiro como `int64` em centavos e moeda
encapsulados. Todos os cálculos e conversões usam inteiros e strings. As operações
devolvem novos valores, preservando os operandos.

### Entrada e normalização

- As moedas suportadas são `BRL` e `USD`, códigos ISO 4217 com escala de duas casas.
- `New` recebe uma string não negativa: parte inteira não vazia, ponto e exatamente
  duas casas decimais, usando apenas dígitos ASCII. Zero é permitido neste tipo;
  as restrições de cada operação financeira serão aplicadas pelo domínio correspondente.
- Sinais, espaços, vírgula, notação científica, `NaN`, `Infinity` e escala diferente
  de dois são rejeitados, sem arredondamento.
- Zeros à esquerda são aceitos: `00025.00` e `25.00` representam o mesmo valor.
  `Amount` normaliza ambos para `25.00`. A futura construção do hash de idempotência
  deve usar esse valor normalizado em ambos os transportes, HTTP e SQS.
- O maior valor externo aceito é `92233720368547758.07`, correspondente a
  `9223372036854775807` centavos. Negativos externos são rejeitados, inclusive `-0.00`.

### Operações e valores internos

- `Zero(currency)` cria um zero inicializado na moeda solicitada.
- `Add`, `Subtract` e `Negate` permitem resultados internos negativos e verificam
  overflow antes do cálculo. O menor valor interno é `-92233720368547758.08`.
- `Compare` compara diretamente os inteiros e retorna `-1`, `0` ou `1`; não calcula
  uma diferença, que poderia ultrapassar o intervalo de `int64`.
- `Money{}` não é um zero monetário válido, pois não tem moeda. `Validate`, as
  operações e a serialização rejeitam valores não inicializados ou com moeda inválida.
- `MinorUnits` e `Currency` são consultas ao estado encapsulado, mantidas para
  compatibilidade. A aceitação de um valor deve passar pelo construtor ou `Validate`.
- Saldo não negativo será uma regra da carteira; `Money` também representa diferenças.

### Saída decimal e JSON

`Amount()` devolve a representação decimal canônica com duas casas.
`MarshalJSON` permite usar `encoding/json.Marshal` para produzir:

```json
{"amount":"25.00","currency":"BRL"}
```

Resultados internos negativos também podem ser serializados, por exemplo para
reconciliação. A formatação trabalha com os dígitos do inteiro, sem tentar negar
numericamente `math.MinInt64`.

O contrato de entrada será um DTO com campos string seguido de `New`. Não há
`UnmarshalJSON` mutável no value object; entradas externas e resultados internos
têm políticas diferentes para números negativos.

### Erros classificáveis

Chamadores devem usar `errors.Is` em vez de comparar mensagens.

| Erro | Significado |
| --- | --- |
| `ErrInvalidAmount` | Formato monetário externo inválido |
| `ErrInvalidCurrency` | Moeda não suportada na criação ou estado interno inválido |
| `ErrCurrencyMismatch` | Operação entre moedas válidas diferentes |
| `ErrUninitialized` | Valor monetário sem moeda |
| `ErrOverflow` | Parsing ou cálculo excederia os limites de `int64` |

## Criação de carteira

`wallet.NewWallet` recebe identidades, saldo inicial e instante de criação. Rejeita
identificadores vazios ou compostos apenas por espaços, valores monetários inválidos,
saldo negativo e instante não inicializado. A carteira começa na versão `1`, com
criação e atualização no mesmo instante em UTC. O instante é fornecido pelo chamador
para permitir testes determinísticos.

O construtor cria o estado do agregado. A persistência da carteira e os lançamentos
e eventos de abertura serão coordenados pelo caso de uso em uma transação SQL.

## Migrations de PostgreSQL

O schema é evoluído por pares SQL `NNNNNN_nome.up.sql` e `NNNNNN_nome.down.sql`,
executados pelo serviço `migrate` do Compose, com `golang-migrate` v4.20.1. O serviço
usa a rede interna (`postgres:5432`), aguarda o banco saudável e monta os arquivos
em modo somente leitura. O controle de versão fica em `public.schema_migrations`.

A primeira migration cria `wallets` com saldo em `BIGINT`, moeda suportada,
versão positiva, timestamps obrigatórios, IDs preenchidos e unicidade de
`(player_id, currency)`. O repositório e a transação financeira ainda serão
implementados. Os comandos de aplicação e reversão estão no `README.md`.

## Conexões, composição e ciclo de vida

O acesso ao PostgreSQL usa `github.com/jackc/pgx/v5`, com `pgxpool` para compartilhar
conexões entre operações de uma instância. A configuração é lida de variáveis de
ambiente; URL obrigatória, timeout positivo e limite positivo de conexões são
validados antes da inicialização.

`internal/app` compõe a aplicação com Uber Fx. O módulo `postgres` fornece o pool
por construtor e registra hooks em `fx.Lifecycle`. `OnStart` faz `Ping` com contexto
e timeout, pois construir um pool não garante que o banco esteja acessível. Uma
falha nessa etapa fecha o pool antes de interromper a inicialização. `OnStop` fecha
as conexões. Os futuros consumidores do pool deverão encerrar antes desse hook,
respeitando a ordem inversa de inicialização do Fx.

`app.Run` utiliza o tratamento de sinais do Fx. Os limites globais são 15 segundos
para inicialização e 10 segundos para encerramento. O timeout de conexão padrão
é 5 segundos; o contexto de inicialização pode impor um limite menor. Os logs são
JSON e os erros de conexão são classificados sem expor a URL ou a senha.

O domínio permanece independente de `pgx`, Fx e configuração de ambiente.
Repositórios com SQL explícito e a delimitação da transação financeira ainda serão
implementados; o pool por si só não oferece atomicidade entre operações de negócio.

## Escopo atual

O domínio implementa `Money` e o construtor de `Wallet`; o ambiente local inclui
PostgreSQL, migrations e a conexão da aplicação com ciclo de vida Fx. Movimentações
de carteira, ledger, repositórios, transações SQL, autenticação, HTTP, SQS e
inbox/outbox serão definidos nas próximas etapas.
