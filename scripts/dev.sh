#!/usr/bin/env bash

set -Eeuo pipefail

project_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
if [[ -z "${project_root}" || ! -d "${project_root}/frontend" || ! -f "${project_root}/go.mod" ]]; then
  echo "Could not determine the VariaNotes project directory." >&2
  exit 1
fi

default_notebook="${project_root}/testdata/demo-notes"
requested_notebook="${1:-${default_notebook}}"
if [[ "${requested_notebook}" == "${default_notebook}" && ! -d "${requested_notebook}" ]]; then
  mkdir -p -- "${requested_notebook}"
  printf '%s\n' '# Welcome to VariaNotes' '' 'This is a disposable local development notebook.' > "${requested_notebook}/Welcome.md"
fi
if [[ ! -d "${requested_notebook}" ]]; then
  echo "Notebook directory does not exist: ${requested_notebook}" >&2
  echo "Usage: ./scripts/dev.sh [path-to-notebook]" >&2
  exit 1
fi
notebook_root="$(cd -- "${requested_notebook}" && pwd -P)"

development_data="${project_root}/.varianotes-data"
app_directory="${development_data}/app"
notebooks_directory="${development_data}/notebooks"
go_build_cache="${GOCACHE:-${HOME}/.cache/go-build}"
go_module_cache="${GOMODCACHE:-${HOME}/go/pkg/mod}"
go_temp_directory="${GOTMPDIR:-${HOME}/.cache/varianotes/go-tmp}"

mkdir -p -- \
  "${app_directory}" \
  "${notebooks_directory}" \
  "${go_build_cache}" \
  "${go_module_cache}" \
  "${go_temp_directory}"

for command_name in go node npm; do
  if ! command -v "${command_name}" >/dev/null 2>&1; then
    echo "Required command is not installed: ${command_name}" >&2
    exit 1
  fi
done

node_major="$(node -p "Number(process.versions.node.split('.')[0])")"
if [[ ! "${node_major}" =~ ^[0-9]+$ ]] || (( node_major < 24 )) || (( node_major == 25 )); then
  echo "VariaNotes development requires Node.js 24 LTS or Node.js 26+. Found: $(node --version)" >&2
  exit 1
fi

if [[ ! -d "${project_root}/frontend/node_modules" ]]; then
  echo "Installing frontend dependencies…"
  (cd -- "${project_root}/frontend" && npm install)
fi

backend_pid=""
frontend_pid=""
cleanup() {
  trap - EXIT INT TERM
  [[ -n "${frontend_pid}" ]] && kill "${frontend_pid}" 2>/dev/null || true
  [[ -n "${backend_pid}" ]] && kill "${backend_pid}" 2>/dev/null || true
  [[ -n "${frontend_pid}" ]] && wait "${frontend_pid}" 2>/dev/null || true
  [[ -n "${backend_pid}" ]] && wait "${backend_pid}" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

auth_mode="${VARIANOTES_AUTH_MODE:-local}"

echo "Starting VariaNotes backend on http://localhost:8080"
echo "Notebook: ${notebook_root}"
(
  cd -- "${project_root}"
  exec env VARIANOTES_DEV_VAULT_PATH="${notebook_root}" \
  VARIANOTES_NOTEBOOKS_DIR="${notebooks_directory}" \
  VARIANOTES_NOTEBOOK_METADATA="${app_directory}/notebooks.json" \
  VARIANOTES_AUTH_MODE="${auth_mode}" \
  VARIANOTES_AUTH_METADATA="${app_directory}/auth.db" \
  VARIANOTES_SESSION_COOKIE_SECURE="false" \
  GOCACHE="${go_build_cache}" \
  GOMODCACHE="${go_module_cache}" \
  GOTMPDIR="${go_temp_directory}" \
  go run ./cmd/varianotes
) &
backend_pid="$!"

echo "Starting frontend on http://localhost:5173"
echo "Press Ctrl+C to stop both processes."
(
  cd -- "${project_root}/frontend"
  exec npm run dev -- --host 0.0.0.0
) &
frontend_pid="$!"

wait -n "${backend_pid}" "${frontend_pid}"
