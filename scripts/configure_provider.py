#!/usr/bin/env python3
"""Acrescenta o provedor A ao Keycloak local já inicializado, sem recriar o realm.

Contrato: https://www.keycloak.org/docs-api/latest/rest-api/index.html
Não imprime tokens, secrets ou respostas de erro do IdP.
"""
import json
import os
from pathlib import Path
import sys
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode
from urllib.request import Request, urlopen


def configure():
    base = os.environ.get("KEYCLOAK_ADMIN_URL", "http://localhost:8081").rstrip("/")
    credentials = urlencode({
        "grant_type": "password", "client_id": "admin-cli",
        "username": os.environ.get("KEYCLOAK_ADMIN_USER", "admin"),
        "password": os.environ.get("KEYCLOAK_ADMIN_PASSWORD", "admin_local"),
    }).encode()
    request = Request(base + "/realms/master/protocol/openid-connect/token", data=credentials)
    with urlopen(request, timeout=5) as response:
        token = json.load(response)["access_token"]

    def api(method, path, data=None):
        request = Request(base + "/admin/realms/jungle/" + path,
                          data=None if data is None else json.dumps(data).encode(),
                          headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"},
                          method=method)
        with urlopen(request, timeout=5) as response:
            body = response.read()
            return json.loads(body) if body else None

    api_client = api("GET", "clients?clientId=jungle-api")[0]["id"]
    roles_path = "clients/" + api_client + "/roles"
    roles = []
    existing_roles = {role["name"] for role in api("GET", roles_path)}
    for name in ("wagering:write", "wagering:read"):
        if name not in existing_roles:
            api("POST", roles_path, {"name": name})
        roles.append(api("GET", roles_path + "/" + name))
    existing = api("GET", "clients?clientId=jungle-provider-a")
    if not existing:
        realm = json.loads((Path(__file__).resolve().parent.parent / "docker/keycloak/jungle-realm.json").read_text())
        provider = next(client for client in realm["clients"] if client["clientId"] == "jungle-provider-a")
        provider["secret"] = os.environ.get("KEYCLOAK_PROVIDER_A_CLIENT_SECRET", "jungle_provider_a_local")
        api("POST", "clients", provider)
        existing = api("GET", "clients?clientId=jungle-provider-a")
    provider_id = existing[0]["id"]
    user = api("GET", "clients/" + provider_id + "/service-account-user")["id"]
    api("POST", "users/" + user + "/role-mappings/clients/" + api_client, roles)
    api("POST", "clients/" + provider_id + "/scope-mappings/clients/" + api_client, roles)
    print("Provedor jungle-provider-a configurado com wagering:write e wagering:read. Clientes existentes e seus secrets foram preservados.")


if __name__ == "__main__":
    try:
        configure()
    except HTTPError as error:
        sys.exit("Falha na configuração do Keycloak: HTTP " + str(error.code))
    except (URLError, TimeoutError, KeyError, IndexError, ValueError):
        sys.exit("Falha na configuração do Keycloak: verifique o serviço e as credenciais administrativas locais.")
