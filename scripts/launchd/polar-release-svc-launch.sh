#!/usr/bin/env bash
set -euo pipefail
ENV_FILE="${HOME}/.local/share/release-svc.env"
if [[ -f "${ENV_FILE}" ]]; then set -a; source "${ENV_FILE}"; set +a; fi
export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin:${PATH:-}"
exec "${HOME}/.local/bin/release-svc"
