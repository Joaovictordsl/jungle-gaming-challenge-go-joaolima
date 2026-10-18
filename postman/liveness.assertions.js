pm.test("Liveness responde HTTP 200", function () {
  pm.response.to.have.status(200);
});

pm.test("Resposta usa application/json", function () {
  pm.expect(pm.response.headers.get("Content-Type")).to.match(/^application\/json(?:;|$)/i);
});

pm.test("Corpo indica processo respondendo", function () {
  pm.expect(pm.response.json()).to.deep.equal({ status: "ok" });
});

pm.test("Resposta não deve ser armazenada em cache", function () {
  pm.expect(pm.response.headers.get("Cache-Control")).to.equal("no-store");
});
