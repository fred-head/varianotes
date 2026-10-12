#!/usr/bin/env bash

set -Eeuo pipefail

image="${1:-}"
expected_version="${2:-}"
if [[ -z "${image}" || -z "${expected_version}" ]]; then
  echo "Usage: $0 <image> <expected-version>" >&2
  exit 2
fi

check_id="${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-1}-$$"
if [[ ! "${check_id}" =~ ^[A-Za-z0-9_.-]+$ ]]; then
  echo "Unsafe release-check identifier" >&2
  exit 2
fi

container_name="varianotes-check-${check_id}"
volume_name="varianotes-check-${check_id}"
container_created=false
volume_created=false
cookie_jar=""

cleanup() {
  trap - EXIT INT TERM
  if [[ "${container_created}" == true ]]; then
    docker rm --force "${container_name}" >/dev/null 2>&1 || true
  fi
  if [[ "${volume_created}" == true ]]; then
    docker volume rm "${volume_name}" >/dev/null 2>&1 || true
  fi
  if [[ -n "${cookie_jar}" && "${cookie_jar}" == /tmp/varianotes-auth-cookies.* && -f "${cookie_jar}" && ! -L "${cookie_jar}" ]]; then
    rm -f -- "${cookie_jar}"
  fi
}
trap cleanup EXIT INT TERM

if docker container inspect "${container_name}" >/dev/null 2>&1 || docker volume inspect "${volume_name}" >/dev/null 2>&1; then
  echo "Refusing to reuse an existing release-check container or volume" >&2
  exit 1
fi

configured_user="$(docker image inspect --format '{{.Config.User}}' "${image}")"
if [[ -z "${configured_user}" || "${configured_user}" == "0" || "${configured_user}" == "root" ]]; then
  echo "Image does not configure a non-root user" >&2
  exit 1
fi
configured_entrypoint="$(docker image inspect --format '{{json .Config.Entrypoint}}' "${image}")"
if [[ "${configured_entrypoint}" != '["varianotes"]' ]]; then
  echo "Image entrypoint is not the VariaNotes CLI: ${configured_entrypoint}" >&2
  exit 1
fi
configured_title="$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.title"}}' "${image}")"
if [[ "${configured_title}" != "VariaNotes" ]]; then
  echo "Image does not carry the VariaNotes OCI title: ${configured_title}" >&2
  exit 1
fi
docker volume create "${volume_name}" >/dev/null
volume_created=true

start_container() {
  local auth_mode="${1:-local}"
  docker run --detach \
    --name "${container_name}" \
    --read-only \
    --tmpfs /tmp:rw,nosuid,nodev,noexec,size=64m \
    --cap-drop ALL \
    --security-opt no-new-privileges:true \
    --publish 127.0.0.1::8080 \
    --volume "${volume_name}:/data" \
    --env VARIANOTES_NOTEBOOKS_DIR=/data/notebooks \
    --env VARIANOTES_NOTEBOOK_METADATA=/data/app/notebooks.json \
    --env "VARIANOTES_AUTH_MODE=${auth_mode}" \
    --env VARIANOTES_AUTH_METADATA=/data/app/auth.db \
    --env VARIANOTES_SESSION_COOKIE_SECURE=false \
    "${image}" >/dev/null
  container_created=true
}

wait_for_health() {
  local port response
  port="$(docker port "${container_name}" 8080/tcp | sed -n 's/.*://p' | head -n 1)"
  if [[ ! "${port}" =~ ^[0-9]+$ ]]; then
    echo "Could not determine published health-check port" >&2
    exit 1
  fi
  for _ in {1..30}; do
    if response="$(curl --fail --silent --show-error "http://127.0.0.1:${port}/api/health" 2>/dev/null)"; then
      if [[ "${response}" != *'"status":"ok"'* || "${response}" != *"\"version\":\"${expected_version}\""* ]]; then
        echo "Unexpected health response: ${response}" >&2
        exit 1
      fi
      printf '%s\n' "${port}"
      return
    fi
    sleep 1
  done
  docker logs "${container_name}" >&2 || true
  echo "Container did not become healthy" >&2
  exit 1
}

start_container local
if [[ "$(docker exec "${container_name}" varianotes --version)" != "varianotes ${expected_version}" ]]; then
  echo "Container CLI did not report the expected VariaNotes version" >&2
  exit 1
fi
port="$(wait_for_health)"

for retired_tool in git ssh; do
  if docker exec "${container_name}" /bin/sh -c "command -v ${retired_tool}" >/dev/null 2>&1; then
    echo "Production image still contains retired runtime tool: ${retired_tool}" >&2
    exit 1
  fi
done

runtime_uid="$(docker exec "${container_name}" id -u)"
if [[ "${runtime_uid}" == "0" ]]; then
  echo "Container process is running as root" >&2
  exit 1
fi

bootstrap_output="$(docker exec "${container_name}" varianotes auth bootstrap-token)"
mapfile -t bootstrap_token_lines < <(printf '%s\n' "${bootstrap_output}" | grep -E '^[A-Za-z0-9_-]{43}$' || true)
unset bootstrap_output
if [[ "${#bootstrap_token_lines[@]}" -ne 1 ]]; then
  echo "Container did not return a valid one-time bootstrap token" >&2
  exit 1
fi
bootstrap_token="${bootstrap_token_lines[0]}"
unset bootstrap_token_lines
if docker logs "${container_name}" 2>&1 | grep --fixed-strings --quiet "${bootstrap_token}"; then
  echo "Container wrote the bootstrap token to normal server logs" >&2
  exit 1
fi
auth_status="$(curl --fail --silent --show-error "http://127.0.0.1:${port}/api/auth/status")"
if [[ "${auth_status}" != *'"mode":"local"'* || "${auth_status}" != *'"setupRequired":true'* ]]; then
  echo "Fresh local-auth container did not require owner setup: ${auth_status}" >&2
  exit 1
fi
blocked_status="$(curl --silent --output /dev/null --write-out '%{http_code}' \
  --request POST \
  --header 'Content-Type: application/json' \
  --data '{"path":"Must stay blocked.md","type":"file"}' \
  "http://127.0.0.1:${port}/api/repository/entries")"
if [[ "${blocked_status}" != "403" ]]; then
  echo "Fresh local-auth container exposed protected APIs before setup (HTTP ${blocked_status})" >&2
  exit 1
fi

cookie_jar="$(mktemp /tmp/varianotes-auth-cookies.XXXXXX)"
setup_response="$(curl --fail --silent --show-error \
  --cookie-jar "${cookie_jar}" \
  --request POST \
  --header 'Content-Type: application/json' \
  --data "{\"bootstrapToken\":\"${bootstrap_token}\",\"password\":\"release-check-passphrase\"}" \
  "http://127.0.0.1:${port}/api/auth/setup")"
unset bootstrap_token
csrf_token="$(printf '%s\n' "${setup_response}" | sed -n 's/.*"csrfToken":"\([^"]*\)".*/\1/p')"
if [[ ! "${csrf_token}" =~ ^[A-Za-z0-9_-]{43}$ ]]; then
  echo "Owner setup did not return a valid session-bound CSRF token: ${setup_response}" >&2
  exit 1
fi
if [[ "$(docker exec "${container_name}" stat -c '%a' /data/app/auth.key)" != "600" ]]; then
  echo "MFA encryption key does not have private permissions" >&2
  exit 1
fi
vault_response="$(curl --fail --silent --show-error \
  --cookie "${cookie_jar}" \
  --request POST \
  --header 'Content-Type: application/json' \
  --header "X-CSRF-Token: ${csrf_token}" \
  --data '{"name":"Release check"}' \
  "http://127.0.0.1:${port}/api/notebooks/local")"
vault_id="$(printf '%s\n' "${vault_response}" | sed -n 's/.*"id":"\([0-9a-f]\{32\}\)".*/\1/p')"
unset vault_response
if [[ ! "${vault_id}" =~ ^[0-9a-f]{32}$ ]]; then
  echo "Authenticated owner could not create a filesystem vault" >&2
  exit 1
fi
curl --fail --silent --show-error \
  --cookie "${cookie_jar}" \
  --request POST \
  --header 'Content-Type: application/json' \
  --header "X-CSRF-Token: ${csrf_token}" \
  --data '{"path":"Release check.md","type":"file"}' \
  "http://127.0.0.1:${port}/api/repository/entries" >/dev/null
unset csrf_token setup_response

docker rm --force "${container_name}" >/dev/null
container_created=false

# The authenticated server-side session and canonical note both survive a
# normal container replacement.
start_container local
port="$(wait_for_health)"
curl --fail --silent --show-error --cookie "${cookie_jar}" \
  "http://127.0.0.1:${port}/api/repository/file?path=Release%20check.md" \
  | grep --fixed-strings '"path":"Release check.md"' >/dev/null

# Operator MFA recovery revokes browser sessions but never changes notes.
docker exec "${container_name}" varianotes auth reset-mfa >/dev/null
revoked_status="$(curl --silent --output /dev/null --write-out '%{http_code}' \
  --cookie "${cookie_jar}" \
  "http://127.0.0.1:${port}/api/repository/file?path=Release%20check.md")"
if [[ "${revoked_status}" != "401" ]]; then
  echo "Operator MFA recovery did not revoke the active session (HTTP ${revoked_status})" >&2
  exit 1
fi

docker rm --force "${container_name}" >/dev/null
container_created=false

# Explicit disabled mode remains an intentional migration option and retains
# the same portable notebook data.
start_container disabled
port="$(wait_for_health)"
curl --fail --silent --show-error \
  "http://127.0.0.1:${port}/api/repository/file?path=Release%20check.md" \
  | grep --fixed-strings '"path":"Release check.md"' >/dev/null

docker rm --force "${container_name}" >/dev/null
container_created=false

docker run --rm \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges:true \
  --volume "${volume_name}:/data:ro" \
  --entrypoint /bin/sh \
  "${image}" \
  -c 'test -f "$1" && grep -Fx "# Release check" "$1" >/dev/null' \
  sh "/data/notebooks/${vault_id}/Release check.md"

echo "Container security, health, and persistence checks passed."
