#!/usr/bin/env bash
# Allow a released keyto.exe in Microsoft Defender for Endpoint (hemfrid tenant).
#
# Adds an "Allowed" FileSha256 custom indicator for the new Windows binary,
# checks that Defender returns it, then deletes this workflow's indicators for
# older releases beyond the newest $KEEP, so users who have not updated yet
# keep working.
#
# Only indicators this script created (application == $APP_NAME) are pruned;
# indicators added by hand in the portal are never touched.
#
# Env:
#   DEFENDER_TOKEN        bearer token for the Defender API (Ti.ReadWrite.All)   required
#   VERSION               release tag, e.g. v0.9.1                               required
#   HASH                  sha256 of keyto_windows_amd64.exe (64 lowercase hex)  required
#   KEEP                  releases to keep allowed (default 5)
#   RBAC_GROUPS           comma-separated Defender device groups; empty = all devices
#   DEFENDER_API          API base (default https://api.security.microsoft.com)
#
# Fails (exit 1) if the indicator cannot be added or verified. Pruning problems
# only warn: an extra old indicator does no harm, a blocked release does.
set -euo pipefail

: "${DEFENDER_TOKEN:?DEFENDER_TOKEN is required}"
: "${VERSION:?VERSION is required}"
: "${HASH:?HASH is required}"
KEEP="${KEEP:-5}"
RBAC_GROUPS="${RBAC_GROUPS:-}"
DEFENDER_API="${DEFENDER_API:-https://api.security.microsoft.com}"
APP_NAME="keyto CLI"
TITLE_PREFIX="keyto v"

[[ "$HASH" =~ ^[0-9a-f]{64}$ ]] || { echo "::error::HASH is not a lowercase sha256: '$HASH'"; exit 1; }
[[ "$VERSION" =~ ^v[0-9] ]] || { echo "::error::VERSION must look like v1.2.3: '$VERSION'"; exit 1; }
[[ "$KEEP" =~ ^[1-9][0-9]*$ ]] || { echo "::error::KEEP must be a positive integer: '$KEEP'"; exit 1; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# api METHOD PATH [curl args...] -> prints HTTP status; body in $tmp/body
api() {
  local method="$1" path="$2"; shift 2
  curl -sS -o "$tmp/body" -w '%{http_code}' -X "$method" \
    -H "Authorization: Bearer ${DEFENDER_TOKEN}" \
    -H 'Accept: application/json' \
    "$@" "${DEFENDER_API}${path}"
}

# --- 1. Submit (or update) the indicator --------------------------------------
payload="$(jq -n \
  --arg value "$HASH" \
  --arg app "$APP_NAME" \
  --arg title "keyto ${VERSION}" \
  --arg desc "keyto CLI ${VERSION} (keyto_windows_amd64.exe) from github.com/hemfrid/keyto-hub-cli. Added by its release workflow; removed automatically once ${KEEP} newer releases exist." \
  --arg groups "$RBAC_GROUPS" \
  '{
     indicatorValue: $value,
     indicatorType: "FileSha256",
     action: "Allowed",
     application: $app,
     title: $title,
     description: $desc,
     severity: "Informational",
     generateAlert: false
   }
   + (if $groups == "" then {} else {rbacGroupNames: ($groups | split(",") | map(gsub("^\\s+|\\s+$"; "")) | map(select(. != "")))} end)')"

status="$(api POST /api/indicators -H 'Content-Type: application/json' --data "$payload")"
if [[ "$status" != 2* ]]; then
  echo "::error::Defender rejected the indicator (HTTP $status): $(head -c 2000 "$tmp/body")"
  exit 1
fi
echo "Submitted Allowed indicator for keyto ${VERSION}: ${HASH} (scope: ${RBAC_GROUPS:-all devices})"

# --- 2. Verify Defender returns it --------------------------------------------
status="$(api GET /api/indicators -G --data-urlencode "\$filter=indicatorValue eq '${HASH}'")"
if [[ "$status" != 2* ]]; then
  echo "::error::Could not read back the indicator (HTTP $status): $(head -c 2000 "$tmp/body")"
  exit 1
fi
if ! jq -e --arg h "$HASH" 'any(.value[]; (.indicatorValue | ascii_downcase) == $h and .action == "Allowed")' "$tmp/body" >/dev/null; then
  echo "::error::Defender has no Allowed indicator for ${HASH} after submitting it"
  exit 1
fi
echo "Verified: Defender lists ${HASH} as Allowed."

# --- 3. Prune this workflow's indicators for older releases -------------------
status="$(api GET /api/indicators -G \
  --data-urlencode "\$filter=application eq '${APP_NAME}' and indicatorType eq 'FileSha256'" \
  --data-urlencode "\$top=10000")" || status="000"
if [[ "$status" != 2* ]]; then
  echo "::warning::Skipped pruning: could not list indicators (HTTP $status)"
  exit 0
fi

# Newest first by version in the title; keep the newest $KEEP, never the one just added.
jq -r --arg p "$TITLE_PREFIX" --arg h "$HASH" '
  .value[]
  | select((.title // "") | startswith($p))
  | select((.indicatorValue | ascii_downcase) != $h)
  | "\(.title | ltrimstr("keyto "))\t\(.id)\t\(.indicatorValue)"' "$tmp/body" \
  | sort -t $'\t' -k1,1 -V -r > "$tmp/older"

# The current release takes one of the $KEEP slots.
tail -n +"$KEEP" "$tmp/older" > "$tmp/delete"

if [[ ! -s "$tmp/delete" ]]; then
  echo "Nothing to prune (keeping ${KEEP} releases)."
  exit 0
fi

while IFS=$'\t' read -r ver id value; do
  status="$(api DELETE "/api/indicators/${id}")" || status="000"
  if [[ "$status" == 2* || "$status" == 404 ]]; then
    echo "Pruned indicator ${id} (keyto ${ver}, ${value})"
  else
    echo "::warning::Could not delete indicator ${id} (keyto ${ver}): HTTP $status"
  fi
done < "$tmp/delete"
