const body = pm.response.json();
const checks = body.checks;
pm.test("Readiness usa status HTTP e estado coerentes", function () {
  pm.expect(pm.response.code).to.be.oneOf([200, 503]);
  pm.expect(body.status).to.equal(pm.response.code === 200 ? "ready" : "not_ready");
});
pm.test("Checks possuem somente os componentes e estados públicos", function () {
  pm.expect(body).to.have.all.keys("status", "checks");
  pm.expect(checks).to.have.all.keys("postgres", "sqsConsumer", "sqsPublisher");
  pm.expect(checks.postgres).to.be.oneOf(["ok", "unavailable"]);
  for (const name of ["sqsConsumer", "sqsPublisher"]) {
    pm.expect(checks[name]).to.be.oneOf(["ok", "unavailable", "disabled"]);
  }
  const ready = checks.postgres === "ok" && ["sqsConsumer", "sqsPublisher"].every(name => ["ok", "disabled"].includes(checks[name]));
  pm.expect(ready).to.equal(pm.response.code === 200);
});
pm.test("Resposta usa application/json", function () {
  pm.expect(pm.response.headers.get("Content-Type")).to.match(/^application\/json(?:;|$)/i);
});
pm.test("Resposta não deve ser armazenada em cache", function () {
  pm.expect(pm.response.headers.get("Cache-Control")).to.equal("no-store");
});
