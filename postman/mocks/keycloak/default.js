// Derivado de postman/specs/keycloak.oidc.yaml. Fixtures são exclusivas dos testes
// locais: as chaves são geradas a cada execução e nunca pertencem ao Keycloak real.
const http = require('http');
const crypto = require('crypto');
const issuer = 'http://127.0.0.1:4900/keycloak/realms/jungle';
const keys = [0, 1, 2].map(() => crypto.generateKeyPairSync('rsa', { modulusLength: 2048 }));
let state;
function reset() {
  state = { active: 0, discoveryStatus: 200, jwksStatus: 200, jwksDelay: 0,
    discoveryRequests: 0, jwksRequests: 0, authorizationHeaders: 0 };
}
reset();
function json(res, status, body) {
  if (res.destroyed) return;
  res.writeHead(status, { 'Content-Type': 'application/json' });
  res.end(JSON.stringify(body));
}
function encoded(value) { return Buffer.from(JSON.stringify(value)).toString('base64url'); }
function token(kind) {
  const now = Math.floor(Date.now() / 1000);
  const claims = { iss: issuer, aud: 'jungle-api', sub: 'simulation-service-account', azp: 'jungle-internal',
    typ: 'Bearer', iat: now, exp: now + 300,
    resource_access: { 'jungle-api': { roles: ['wallets:read', 'wallets:write'] } } };
  if (kind === 'expired') claims.exp = now - 60;
  if (kind === 'wrong-issuer') claims.iss = 'https://other.example/realms/jungle';
  if (kind === 'wrong-audience') claims.aud = 'other-api';
  if (kind === 'other-client') claims.azp = 'provider-a';
  if (kind === 'read-only') claims.resource_access['jungle-api'].roles = ['wallets:read'];
  if (kind === 'wrong-role-audience') claims.resource_access = { 'other-api': { roles: ['wallets:write'] } };
  if (kind === 'realm-role') { claims.resource_access = {}; claims.realm_access = { roles: ['wallets:write'] }; }
  if (kind === 'missing-subject') delete claims.sub;
  if (kind === 'missing-expiry') delete claims.exp;
  if (kind === 'not-before') claims.nbf = now + 120;
  if (kind === 'future-issued') claims.iat = now + 120;
  if (kind === 'id-token') claims.typ = 'ID';
  const algorithm = kind === 'unsigned' ? 'none' : kind === 'hmac' ? 'HS256' : 'RS256';
  const unsigned = encoded({ alg: algorithm, typ: 'JWT', kid: 'simulation-' + state.active }) + '.' + encoded(claims);
  let signature = '';
  if (algorithm === 'HS256') signature = crypto.createHmac('sha256', 'simulation-only').update(unsigned).digest('base64url');
  if (algorithm === 'RS256') signature = crypto.sign('RSA-SHA256', Buffer.from(unsigned), keys[kind === 'bad-signature' ? 2 : state.active].privateKey).toString('base64url');
  return unsigned + '.' + signature;
}
http.createServer((req, res) => {
  const url = new URL(req.url, 'http://localhost');
  const path = url.pathname.replace(/^\/keycloak(?=\/)/, '');
  if (req.method === 'POST' && path === '/__fixtures/reset') { reset(); return json(res, 200, { reset: true }); }
  if (req.method === 'POST' && path === '/__fixtures/rotate') { state.active = 1; return json(res, 200, { rotated: true }); }
  if (req.method === 'POST' && path === '/__fixtures/failure') {
    state.discoveryStatus = Number(url.searchParams.get('discovery') || 200);
    state.jwksStatus = Number(url.searchParams.get('jwks') || 200);
    state.jwksDelay = Number(url.searchParams.get('delay') || 0);
    return json(res, 200, { configured: true });
  }
  if (req.method === 'GET' && path === '/__fixtures/stats') return json(res, 200, state);
  if (req.method === 'GET' && path === '/__fixtures/token') return json(res, 200, { access_token: token(url.searchParams.get('kind') || 'valid') });
  if (req.headers.authorization) state.authorizationHeaders++;
  if (req.method === 'GET' && path === '/realms/jungle/.well-known/openid-configuration') {
    state.discoveryRequests++;
    return json(res, state.discoveryStatus, state.discoveryStatus === 200 ? {
      issuer, jwks_uri: issuer + '/protocol/openid-connect/certs', id_token_signing_alg_values_supported: ['RS256']
    } : { error: 'temporarily_unavailable' });
  }
  if (req.method === 'GET' && path === '/realms/jungle/protocol/openid-connect/certs') {
    state.jwksRequests++;
    const status = state.jwksStatus;
    const jwk = { ...keys[state.active].publicKey.export({ format: 'jwk' }), kid: 'simulation-' + state.active, alg: 'RS256', use: 'sig' };
    return setTimeout(() => json(res, status, status === 200 ? { keys: [jwk] } : { error: 'temporarily_unavailable' }), state.jwksDelay);
  }
  json(res, 501, { error: 'Endpoint outside this OIDC dependency contract' });
}).listen(process.env.PORT || 4501);
