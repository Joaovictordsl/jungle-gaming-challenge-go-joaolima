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
- `FromMinorUnits` reconstrói valores internos diretamente dos centavos, inclusive
  negativos e extremos de `int64`, validando a moeda. A entrada financeira externa
  continua usando `New`; a reconstrução do banco não passa por texto decimal ou float.
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

O contrato de entrada HTTP usa campos string seguidos de `New`. Não há
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
e eventos de abertura são coordenados pelo caso de uso por meio de `OpeningStore`.

`wallet.Rehydrate` reconstrói uma carteira persistida preservando identidade,
saldo, versão e instantes. A função não executa movimentações nem emite eventos;
os instantes são normalizados para UTC sem alterar o momento representado.
Criação e reidratação compartilham `Wallet.Validate`, que também exige versão
positiva e atualização igual ou posterior à criação. Os métodos de consulta
expõem cópias dos valores encapsulados para uso pelo repositório.

## Migrations de PostgreSQL

O schema é evoluído por pares SQL `NNNNNN_nome.up.sql` e `NNNNNN_nome.down.sql`,
executados pelo serviço `migrate` do Compose, com `golang-migrate` v4.20.1. O serviço
usa a rede interna (`postgres:5432`), aguarda o banco saudável e monta os arquivos
em modo somente leitura. O controle de versão fica em `public.schema_migrations`.

A primeira migration cria `wallets` com saldo em `BIGINT`, moeda suportada,
versão positiva, timestamps obrigatórios, IDs preenchidos e unicidade de
`(player_id, currency)`. A segunda cria `wager_transactions`, `wallet_ledger_entries`
e `outbox_events` para a abertura interna. Os comandos de aplicação e reversão
estão no `README.md`.

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
O pool por si só não oferece atomicidade entre operações de negócio.

O módulo `postgres` também fornece `wallet.Repository`, construindo
`WalletRepository` com o pool compartilhado. Os consumidores podem receber a
interface do domínio pelo Fx sem depender da implementação PostgreSQL.

## Persistência de carteiras

`wallet.Repository` define as operações de criação e busca por ID. A implementação
PostgreSQL oferece ambos os métodos: `Create` valida o contexto e o agregado e
executa um `INSERT` com parâmetros separados do SQL. Centavos são persistidos como
`int64`, sem conversões para ponto flutuante. Violações de unicidade (`23505`) são
traduzidas para `wallet.ErrAlreadyExists`; outros erros preservam sua causa.

`FindByID` valida o contexto e o ID, executa `SELECT` parametrizado e traduz a
ausência de uma linha para `wallet.ErrNotFound`. As colunas monetárias são lidas
como `int64` e string e reconstruídas por `money.FromMinorUnits`. `wallet.Rehydrate`
valida e preserva os demais dados, sem movimentação ou nova versão. A busca simples
não adquire lock de escrita; o processamento financeiro terá coordenação própria
por carteira dentro da transação SQL.

O executor `DBTX` pode ser um pool ou uma transação `pgx.Tx`. O repositório não
executa `BEGIN`, `COMMIT` ou `ROLLBACK`; na abertura, `OpeningStore` controla uma
transação compartilhada entre carteira, transação financeira, ledger e outbox.

## Dependências da abertura

`internal/application/wallet` define a entrada e as dependências de `OpenWallet`.
O caso de uso recebe um `IDGenerator`, um `Clock` e um `OpeningStore`. O relógio e
o gerador de IDs são injetados para permitir cenários determinísticos nos testes.

`OpeningStore.SaveOpening` é o contrato de persistência de uma abertura completa:
saldo zero exige somente a carteira; saldo positivo exige também `OPENING` em
`PROCESSED`, crédito no ledger e os eventos `WagerTransactionProcessed` e
`WalletBalanceChanged` na outbox, no mesmo commit, mantendo a versão inicial 1.
Sua implementação PostgreSQL usa `pgxpool.Begin` e constrói o repositório de
carteiras com o `pgx.Tx` recebido. O repositório simples
`wallet.Repository.Create`, isoladamente, não atende a esse contrato financeiro.

`OpenWallet.Execute` verifica o contexto, gera o ID, obtém o instante e constrói a
carteira pelo domínio antes de chamar `SaveOpening`. Erros preservam suas causas
e retornam uma carteira vazia. Após a confirmação bem-sucedida da persistência,
um cancelamento tardio não transforma o sucesso em erro. Testes unitários verificam
essa coordenação com implementações de teste das dependências. Testes adicionais
com PostgreSQL real demonstram rollback inclusive após a primeira inserção de
outbox e na confirmação da transação.

## Persistência atômica da abertura

`OpeningStore.SaveOpening` rejeita carteiras inválidas ou fora do estado inicial
(versão diferente de 1 ou atualização diferente da criação). O domínio `wager.Opening`
cria a origem interna em `PENDING` e valida a transição para `PROCESSED`; somente
o resultado final é persistido, sem aceite intermediário que exija retomada.
`ledger.Entry` valida identidades, valor positivo, moedas, saldos não negativos e
a equação do lançamento com aritmética exata, incluindo overflow.

O ID de carteira usa `crypto/rand.Text`. A abertura usa `opening:<walletId>`; o
ledger usa `ledger:opening:<walletId>`; os dois tipos de evento usam prefixos
distintos sobre essa identidade. Essas derivações são injetivas e estáveis para
uma carteira. Restrições únicas impedem outra abertura ou outro crédito para a
mesma transação. Duas aberturas para `(playerId, currency)` disputam a constraint
de carteiras: uma confirma e a outra recebe `wallet.ErrAlreadyExists`. Não há lock
global nem retry automático de crédito. Erros de commit podem ter resultado
indeterminado em caso de perda da conexão; a consulta por identidade e as
constraints evitam tratar uma repetição como novo crédito.

Os eventos concretos `WagerTransactionProcessed` e `WalletBalanceChanged` têm
estado privado, dados tipados e envelope com versão 1, IDs estáveis, agregado
igual à carteira, correlação e causa iguais à abertura, e timestamp UTC. Dinheiro
é serializado por `Money` como strings decimais. Os snapshots são inseridos na
outbox sem publicação; o publisher será uma etapa posterior. O relógio fornecido
pelo Fx usa resolução de microssegundos para corresponder ao PostgreSQL.

O banco protege ledger e aberturas processadas contra `UPDATE`, `DELETE` e
`TRUNCATE`. Na outbox, identidade, payload e ocorrência são imutáveis; tentativas,
agendamento, lease e confirmação de publicação podem evoluir. Foreign keys
compostas mantêm carteira, jogador, moeda e valor coerentes entre os registros.
A equação do ledger usa cast para `NUMERIC` somente na constraint para não sofrer
overflow; as colunas de dinheiro permanecem `BIGINT`.

Uma constraint trigger adiada até o commit exige que cada `OPENING` tenha o
crédito de zero ao saldo inicial, os dois eventos e o estado inicial correspondente
na carteira. Uma falha aborta a abertura inteira. A limpeza da transação usa um
contexto independente com prazo de 5 segundos, inclusive quando a chamada expira.

Nesta migration, `wager_transactions` admite somente `INTERNAL/OPENING`, sem
campos externos inaplicáveis. A ampliação para os cinco tipos externos e suas
transições exigirá uma nova migration e os respectivos modelos. A proteção de
registros terminais também deverá acompanhar essa evolução.

O Fx fornece `OpeningStore`, gerador de IDs, relógio e `OpenWallet`. Testes de
integração de abertura usam schemas descartáveis para validar commits e falhas
reais sem deixar dados financeiros no schema da aplicação. O ciclo de migrations
também é testado nesses schemas.

## Transporte HTTP

`internal/infrastructure/http` configura Gin e um `net/http.Server` explícito.
`internal/app` compõe o módulo após o PostgreSQL: o pool é iniciado primeiro e
encerrado por último, permitindo que requisições terminem antes de fechar o banco.
A configuração HTTP é carregada separadamente (`config.HTTP`), com endereço
`127.0.0.1:8080` e prazo de shutdown de 5 segundos por padrão.

O hook `OnStart` abre o listener de forma síncrona; porta ocupada falha antes que
o Fx considere a aplicação iniciada. `Serve` executa em goroutine. Um erro inesperado
nesse loop solicita encerramento pelo Fx com código 1. `OnStop` usa `Shutdown`
para drenar requisições e, se o prazo acabar, fecha as conexões restantes com
`Close`. O contexto global do Fx também limita esse prazo.

O servidor limita leitura de headers a 5 segundos, leitura a 10 segundos, escrita
a 15 segundos e conexões ociosas a 60 segundos. O Gin usa modo release, sem confiar
em headers de proxy. Logs de acesso usam método, padrão da rota, status e duração;
não incluem headers, query string ou corpo. O recovery devolve erro JSON genérico
e não escreve o valor do panic nem o dump da requisição.

O contrato em `docs/openapi.yaml` inclui `GET /health/live`, público e sem
dependência de banco ou SQS por requisição. Responde `200 {"status":"ok"}` sem
cache. Métodos não suportados recebem `405`; caminhos desconhecidos recebem `404`,
ambos em JSON. Readiness será acrescentado depois. O script local em
`postman/liveness.assertions.js` permite verificar a resposta de uma instância real
sem publicar resultados no workspace do Postman.

`POST /wallets` exige o cliente interno com `wallets:write`, recebe JSON de até
16 KiB e chama `OpenWallet`. O parser rejeita campos duplicados, desconhecidos,
ausentes e valores monetários que não sejam strings. `Money.New` valida o formato,
a moeda e o limite inteiro. O handler só retorna `201` e `Location` após o commit.
`GET /wallets/:walletId` exige `wallets:read` e consulta `wallet.Repository`,
preservando saldo e versão. Ambos propagam o contexto da requisição ao banco e
retornam um DTO com identidade, jogador, saldo e versão, sem cache.

Conflitos de unicidade produzem `409`; carteiras inexistentes, `404`. O adapter
PostgreSQL preserva a causa dos erros e classifica falhas transitórias de conexão,
cancelamento e alguns SQLSTATEs como `wallet.ErrUnavailable`, mapeado para `503`.
Outras falhas internas retornam `500`. O transporte registra códigos estáveis sem
expor o erro do driver. Uma falha de comunicação no commit pode ter resultado
incerto; a resposta de indisponibilidade não implica que houve rollback.

## Provedor de identidade local

O IdP escolhido é Keycloak, fixado na versão `26.8.1`. Ele implementa OAuth 2.0/OIDC
e permite reproduzir a emissão de tokens sem cadastro de senhas ou emissão própria
na aplicação Go. A configuração local usa `start-dev`, publica somente
`127.0.0.1:8081` e importa o realm `jungle` de um arquivo versionado. Um PostgreSQL
exclusivo, sem porta publicada, persiste as configurações e as chaves. As migrations
da aplicação não gerenciam o schema interno do Keycloak.

O cliente `jungle-api` representa o destinatário dos tokens e suas permissões.
O cliente confidencial `jungle-internal` usa uma conta de serviço e
`client_credentials`; fluxos de navegador e senha de usuário estão desabilitados.
Seu token contém `aud` com `jungle-api`, `azp` igual a `jungle-internal` e os papéis
`wallets:read` e `wallets:write` em `resource_access.jungle-api.roles`. O escopo é
restrito explicitamente a essas permissões. O secret é interpolado de uma variável
do container durante a importação; as credenciais padrão são exemplos locais.

O issuer publicado é `http://localhost:8081/realms/jungle`, correspondente à execução
atual do Go no host. Os endpoints são obtidos por discovery OIDC, e as chaves
públicas são publicadas por JWKS. Os tokens usam assinatura `RS256` e validade de
300 segundos. A futura execução do Go em container precisará manter esse issuer
estável e configurar acesso de rede aos endpoints do IdP.

O health check do container usa `/health/ready` na porta interna `9000`, habilitada
por `KC_HEALTH_ENABLED`. Um script com bash verifica o status HTTP sem acrescentar
curl ou wget à imagem. O startup aguarda o banco exclusivo saudável; o serviço
fica saudável depois que Keycloak e a importação terminam.

Realm existente é preservado em reinicializações: `--import-realm` não sobrescreve
o estado já persistido. Alterações posteriores de clientes, papéis e secrets devem
ser aplicadas pelo console/administração do IdP e refletidas no bootstrap. Variáveis
de bootstrap também não rotacionam senhas existentes no banco automaticamente.

Os testes opcionais `-tags=keycloak` usam a implantação real e validam discovery,
assinatura contra JWKS, claims de identidade/audiência/validade, papéis do serviço
interno e rejeição de credenciais e fluxos desabilitados. Também exercitam o
middleware real com tokens emitidos pelo IdP. Clientes de provedores e seu vínculo autorizado
com `providerId` serão adicionados junto com os casos de uso externos; eles não
receberão os papéis internos de carteira.

Referências: [container](https://www.keycloak.org/server/containers),
[importação](https://www.keycloak.org/server/importExport) e
[endpoints OIDC e client credentials](https://www.keycloak.org/securing-apps/oidc-layers).

## Validação JWT e autorização HTTP

`internal/infrastructure/auth` adapta `go-oidc/v3` para os access tokens do Keycloak.
O módulo descobre o provedor no hook `OnStart` antes que o HTTP abra sua porta;
discovery inacessível ou com issuer/JWKS inesperados impede a inicialização.
O JWKS deve pertencer à mesma origem configurada do issuer, e o transporte não
segue redirects. O cliente HTTP tem prazo configurável, de 3 segundos por padrão.
Na parada, primeiro termina o HTTP, depois o validador cancela suas consultas e
fecha conexões ociosas, e por último o pool do PostgreSQL é fechado.

O verificador aceita somente RS256 e não desabilita as verificações de assinatura,
issuer, audiência ou expiração da biblioteca. Como o verificador OIDC também pode
ser usado com ID tokens, a aplicação exige adicionalmente `typ: Bearer`, `sub` e
`azp` preenchidos, emissão presente, expiração posterior à emissão e tolerância
máxima de 30 segundos para `iat`/`nbf` futuros. As claims só produzem um `Principal`
depois da verificação da assinatura. A identidade tem estado privado e não carrega
o token bruto. A aplicação não precisa do secret do cliente para verificar JWTs.

O middleware Gin aceita um único Bearer header, limitado a 16 KiB, sem tokens de
query string ou cookie. `RequireInternalPermission` combina autenticação, o cliente
interno configurado em `azp` e um papel da audiência da API. Papéis de outro cliente
ou do realm não dão acesso a carteiras. O middleware é aplicado ao registrar a
abertura e a consulta de carteiras no Fx. Liveness permanece público.

Tokens ausentes ou inválidos retornam `401`; falta de cliente/papel autorizado
retorna `403`. Falha ou timeout na obtenção necessária do JWKS retorna `503`.
As mensagens são genéricas, sem token ou resposta do IdP; `401`/`403` incluem o
desafio Bearer e erros de autenticação não são armazenados em cache. O adapter
classifica falhas de I/O na fronteira `KeySet`, antes que a biblioteca formate
erros sem preservar suas causas. Cancelamento da chamada também é respeitado.

Uma instância de `RemoteKeySet` é reutilizada para cache e coordenação de consultas
concorrentes. Chave não conhecida provoca atualização; falha de consulta não libera
o token. Uma chave em cache pode validar tokens ainda não expirados sem consultar
o IdP a cada requisição. Isso preserva disponibilidade sem aceitar claims sem
assinatura verificada.

Testes unitários cobrem a política de claims e o contrato do middleware. A
simulação Postman em `postman/simulations/oidc.sim.yaml` fornece apenas a dependência
OIDC: o Gin e o validador são reais e usam HTTP, com testes de chave incorreta,
algoritmo inseguro, claims inválidas, papéis/clientes incorretos, rotação, cache,
503, timeout e falha de discovery. O mock local deriva do contrato oficial e do
realm do repositório, gera chaves exclusivas dos testes e não é publicado.
Os testes de Keycloak real verificam interoperabilidade com a implantação local.

## Escopo atual

O domínio implementa `Money`, criação e reidratação de `Wallet`, abertura interna
e lançamentos de ledger. O ambiente local inclui PostgreSQL, duas migrations e
ciclo de vida Fx. O repositório possui `Create` e `FindByID`, e a abertura grava
seus registros financeiros atomicamente com a outbox. Débitos/créditos de apostas,
transações externas, rotas HTTP de apostas e consultas financeiras, readiness, SQS, inbox,
publisher e reconciliação ainda serão implementados. O servidor Gin e a rota de
liveness já estão integrados ao ciclo de vida da aplicação. O Keycloak local emite
tokens do serviço interno com configuração reproduzível pelo Compose, e o
middleware JWT/OIDC já está integrado ao Fx. A abertura e a consulta de carteiras
estão disponíveis por HTTP com permissões distintas. Testes com a composição
completa usam Keycloak e PostgreSQL reais em schemas descartáveis; a simulação
OIDC também verifica a separação entre leitura e escrita nas rotas reais.
