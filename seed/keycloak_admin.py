"""Cliente mínimo de la Admin REST API de Keycloak para el seed."""

from __future__ import annotations

import time
from typing import Any
from urllib.parse import quote

import requests

from catalog import KEYCLOAK_ROLE_BY_DB, UserSpec


class KeycloakError(RuntimeError):
    """Error controlado al comunicarse con Keycloak."""


class KeycloakAdmin:
    def __init__(
        self,
        base_url: str,
        realm: str,
        admin_username: str,
        admin_password: str,
        *,
        max_attempts: int = 30,
        retry_seconds: float = 2.0,
    ) -> None:
        self.base_url = base_url.rstrip("/")
        self.realm = realm
        self.admin_username = admin_username
        self.admin_password = admin_password
        self.max_attempts = max_attempts
        self.retry_seconds = retry_seconds
        self.session = requests.Session()
        self.access_token = self._fetch_token()

    def _token_url(self) -> str:
        return f"{self.base_url}/realms/master/protocol/openid-connect/token"

    def _fetch_token(self) -> str:
        try:
            response = self.session.post(
                self._token_url(),
                data={
                    "client_id": "admin-cli",
                    "username": self.admin_username,
                    "password": self.admin_password,
                    "grant_type": "password",
                },
                timeout=20,
            )
        except requests.RequestException as exc:
            raise KeycloakError(f"No se pudo conectar a Keycloak: {exc}") from exc

        if response.status_code != 200:
            raise KeycloakError(
                "No se pudo obtener un token de Keycloak. "
                f"HTTP {response.status_code}: {response.text[:500]}"
            )

        token = response.json().get("access_token")
        if not token:
            raise KeycloakError("Keycloak no devolvió access_token")
        return token

    def _request(
        self,
        method: str,
        path: str,
        *,
        expected: tuple[int, ...] = (200,),
        **kwargs: Any,
    ) -> requests.Response:
        url = (
            f"{self.base_url}/admin/realms/"
            f"{quote(self.realm, safe='')}{path}"
        )
        last_error: Exception | None = None

        for attempt in range(1, self.max_attempts + 1):
            try:
                response = self.session.request(
                    method,
                    url,
                    headers={"Authorization": f"Bearer {self.access_token}"},
                    timeout=20,
                    **kwargs,
                )
            except requests.RequestException as exc:
                last_error = exc
                if attempt == self.max_attempts:
                    raise KeycloakError(
                        f"No se pudo conectar a Keycloak en {url}: {exc}"
                    ) from exc
                time.sleep(self.retry_seconds)
                continue

            if response.status_code == 401 and attempt < self.max_attempts:
                self.access_token = self._fetch_token()
                continue

            if response.status_code >= 500 and attempt < self.max_attempts:
                last_error = KeycloakError(
                    f"Keycloak respondió {response.status_code}: {response.text[:500]}"
                )
                time.sleep(self.retry_seconds)
                continue

            if response.status_code not in expected:
                raise KeycloakError(
                    f"{method} {url} falló con HTTP {response.status_code}: "
                    f"{response.text[:500]}"
                )

            return response

        raise KeycloakError(f"No se pudo completar {method} {url}: {last_error}")

    def verify_realm(self) -> None:
        try:
            self._request("GET", "", expected=(200,))
        except KeycloakError as exc:
            raise KeycloakError(
                f"El realm {self.realm!r} no está disponible. "
                "Verifica id-provider/import/medicitas-realm.json."
            ) from exc

    def ensure_clinica_attribute(self) -> None:
        """Registra clinica_id en el perfil de usuario si el realm no lo trae.

        Keycloak 26 descarta atributos no declarados en el perfil de usuario.
        El realm ya define el mapper del token, pero no la definición del
        atributo; el seed la agrega de forma idempotente antes de crear users.
        """
        profile = self._request(
            "GET",
            "/users/profile",
            expected=(200,),
        ).json()

        attributes = profile.setdefault("attributes", [])
        if any(item.get("name") == "clinica_id" for item in attributes):
            return

        attributes.append(
            {
                "name": "clinica_id",
                "displayName": "Clínica",
                "permissions": {
                    "view": ["admin", "user"],
                    "edit": ["admin"],
                },
                "multivalued": False,
            }
        )
        self._request(
            "PUT",
            "/users/profile",
            json=profile,
            expected=(200,),
        )

    def get_role(self, role_name: str) -> dict[str, Any]:
        try:
            return self._request(
                "GET",
                f"/roles/{quote(role_name, safe='')}",
                expected=(200,),
            ).json()
        except KeycloakError as exc:
            raise KeycloakError(
                f"Falta el rol {role_name!r} en el realm {self.realm!r}. "
                "Revisa el realm importado."
            ) from exc

    def find_user(self, username: str) -> dict[str, Any] | None:
        response = self._request(
            "GET",
            "/users",
            params={"username": username, "exact": "true"},
            expected=(200,),
        )
        for user in response.json():
            if user.get("username") == username:
                return user
        return None

    def create_user(self, payload: dict[str, Any]) -> str:
        response = self._request(
            "POST",
            "/users",
            json=payload,
            expected=(201,),
        )
        location = response.headers.get("Location")
        if location:
            return location.rstrip("/").rsplit("/", 1)[-1]

        user = self.find_user(payload["username"])
        if user is None:
            raise KeycloakError(
                f"Keycloak creó el usuario {payload['username']!r} "
                "pero no devolvió su id."
            )
        return user["id"]

    def update_user(self, user_id: str, payload: dict[str, Any]) -> None:
        self._request(
            "PUT",
            f"/users/{quote(user_id, safe='')}",
            json=payload,
            expected=(204,),
        )

    def assign_realm_role(self, user_id: str, role: dict[str, Any]) -> None:
        current = self._request(
            "GET",
            f"/users/{quote(user_id, safe='')}/role-mappings/realm",
            expected=(200,),
        ).json()

        if any(item.get("name") == role["name"] for item in current):
            return

        self._request(
            "POST",
            f"/users/{quote(user_id, safe='')}/role-mappings/realm",
            json=[role],
            expected=(204,),
        )

    def ensure_user(self, spec: UserSpec, password: str) -> str:
        payload: dict[str, Any] = {
            "username": spec.username,
            "enabled": True,
            "email": spec.email,
            "emailVerified": True,
            "firstName": spec.first_name,
            "lastName": spec.last_name,
            "attributes": (
                {} if spec.clinic is None else {"clinica_id": [spec.clinic]}
            ),
        }

        existing = self.find_user(spec.username)
        if existing is not None:
            user_id = existing["id"]
            self.update_user(user_id, payload)
        else:
            payload["credentials"] = [
                {
                    "type": "password",
                    "value": password,
                    "temporary": False,
                }
            ]
            user_id = self.create_user(payload)

        role = self.get_role(KEYCLOAK_ROLE_BY_DB[spec.db_role])
        self.assign_realm_role(user_id, role)
        return user_id
