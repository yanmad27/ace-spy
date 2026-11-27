#!/usr/bin/env bash
# Usage: scripts/pc.sh                     show clients that checked in and recent commands
#        scripts/pc.sh <client> <action>   queue anydesk-status | anydesk-start | anydesk-restart | anydesk-killall | anydesk-diag | parsec-start
set -euo pipefail
: "${ACE_SPY_URL:=https://ace-spy.vercel.app}"
# shellcheck disable=SC1090
source "${ACE_SPY_SECRETS:-$HOME/.ace-spy-secrets.env}"
[[ -n "${CONTROL_TOKEN:-}" ]] || { echo "CONTROL_TOKEN missing" >&2; exit 1; }
auth=(-H "Authorization: Bearer $CONTROL_TOKEN")

if [[ $# -eq 2 ]]; then
  body=$(jq -n --arg c "$1" --arg a "$2" '{client: $c, action: $a}')
  curl -fsS "${auth[@]}" -H 'Content-Type: application/json' -d "$body" "$ACE_SPY_URL/api/control/commands"
  echo; echo "Queued; the client picks it up within ~60 s. Run $0 to see the result."
  exit
fi
[[ $# -eq 0 ]] || { sed -n 2,3p "$0" >&2; exit 2; }
curl -fsS "${auth[@]}" "$ACE_SPY_URL/api/control" | jq -r '
  .now as $now
  | "CLIENTS", (.clients[] | "  \(.client)  v\(.version)  seen \((($now - .seenAt) / 1000 | floor))s ago\n    \(.status)"),
    "COMMANDS", (.commands[] | "  #\(.id) \(.client) \(.action)  " +
      (if .doneAt > 0 then "done: \(.result)" elif .takenAt > 0 then "running" else "pending" end))'
