# Simulação OIDC local

Este mock deriva de `postman/specs/keycloak.oidc.yaml`, dos endpoints oficiais do
Keycloak e do realm versionado em `docker/keycloak/jungle-realm.json`. É uma
dependência local administrada por este projeto; não foi usado um mock publicado
por outra equipe. O mock não é a API Go sob teste.

O handler gerado pelo Postman foi adaptado para gerar chaves RSA e JWTs de teste.
As chaves nascem em memória a cada execução; nenhuma chave do Keycloak real é
copiada. Discovery e JWKS reproduzem os caminhos usados pelo verificador.
Outros endpoints recebem `501`, exceto os controles explícitos `__fixtures` usados
pelos testes para gerar tokens, rotacionar chaves e provocar falhas.

Execute na raiz do projeto, em um terminal:

```sh
postman simulation run postman/simulations/oidc.sim.yaml \
  --port 4900 --output ndjson --no-history
```

Em outro terminal:

```sh
go test -race -tags=oidcsimulation ./tests/oidc
```

A porta `4900` é fixa neste fixture para que o issuer e o JWKS publicados coincidam
com a URL usada pelos testes. O estado é compartilhado entre os casos sequenciais;
execute uma suíte de simulação por vez. Os controles são exclusivos do mock e
nunca são registrados na API Go ou no Keycloak real.

O teste executa Gin, middleware e verificador reais contra o mock por HTTP. Ele
verifica assinatura, algoritmo, claims, permissões, cache, rotação, falha do JWKS,
timeout e falha no discovery durante a inicialização. O log NDJSON deve conter
requisições de discovery e JWKS; não use `--log-body` nem versione logs ou tokens.
O modo local não requer publicação nem registra histórico com `--no-history`.

O teste complementar `go test -race -tags=keycloak ./tests/keycloak` verifica também
a interoperabilidade do middleware com tokens emitidos pelo Keycloak real.
