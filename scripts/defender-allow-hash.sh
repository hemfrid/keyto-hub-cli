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
# What Defender says it stored (non-secret fields only), for the log.
jq -r '"POST response: id=\(.id // "?") action=\(.action // "?") type=\(.indicatorType // "?")"' "$tmp/body" 2>/dev/null || true

# --- 2. Verify Defender returns it --------------------------------------------
# The list API can lag a fresh POST, and it may name the action "Allowed" or
# "Allow" (the portal shows "Allow"), so poll for a while and accept either.
VERIFY_TRIES="${VERIFY_TRIES:-12}"
VERIFY_DELAY="${VERIFY_DELAY:-10}"
verified=false
for ((try = 1; try <= VERIFY_TRIES; try++)); do
  status="$(api GET /api/indicators -G --data-urlencode "\$filter=indicatorValue eq '${HASH}'")" || status="000"
  if [[ "$status" == 2* ]] && jq -e --arg h "$HASH" \
      'any(.value[]; ((.indicatorValue // "") | ascii_downcase) == $h and ((.action // "") | ascii_downcase | IN("allowed", "allow")))' \
      "$tmp/body" >/dev/null 2>&1; then
    verified=true
    break
  fi
  echo "Not listed as Allowed yet (attempt ${try}/${VERIFY_TRIES}, HTTP ${status})"
  if (( try < VERIFY_TRIES )); then sleep "$VERIFY_DELAY"; fi
done
if [[ "$verified" != true ]]; then
  echo "::error::Defender has no Allowed indicator for ${HASH} after submitting it (HTTP ${status})"
  echo "Last read-back for this hash (id, action, type, value):"
  jq -r '.value[]? | "  \(.id) \(.action) \(.indicatorType) \(.indicatorValue)"' "$tmp/body" 2>/dev/null \
    || echo "  (unparseable body: $(head -c 500 "$tmp/body"))"
  exit 1
fi
echo "Verified: Defender lists ${HASH} as Allowed (attempt ${try})."

# --- 3. Prune this workflow's indicators for older releases -------------------
status="$(api GET /api/indicators -G \
  --data-urlencode "\$filter=application eq '${APP_NAME}' and indicatorType eq 'FileSha256'" \
  --data-urlencode "\$top=10000")" || status="000"
if [[ "$status" != 2* ]]; then
  echo "::warning::Skipped pruning: could not list indicators (HTTP $status)"
  exit 0
fi

# Newest first by version in the title; keep the newest $KEEP, never the one just added.
# Don't trust the server-side $filter: the token can delete any indicator in the
# tenant, so re-check application, action and type here. @tsv escapes tabs and
# newlines inside fields, and an empty id becomes "(none)" so read can't shift
# columns (consecutive tabs collapse in IFS).
if ! jq -r --arg p "$TITLE_PREFIX" --arg h "$HASH" --arg app "$APP_NAME" '
  .value[]
  | select(.application == $app and .action == "Allowed" and .indicatorType == "FileSha256")
  | select((.title | type) == "string" and (.title | startswith($p)))
  | select((.indicatorValue // "" | ascii_downcase) != $h)
  | [(.title | ltrimstr("keyto ")),
     (.id | tostring | if . == "" then "(none)" else . end),
     (.indicatorValue // "")]
  | @tsv' "$tmp/body" \
  | sort -t $'\t' -k1,1 -V -r > "$tmp/older"; then
  echo "::warning::Skipped pruning: could not parse the indicator list"
  exit 0
fi

# The current release takes one of the $KEEP slots.
tail -n +"$KEEP" "$tmp/older" > "$tmp/delete"

if [[ ! -s "$tmp/delete" ]]; then
  echo "Nothing to prune (keeping ${KEEP} releases)."
  exit 0
fi

while IFS=$'\t' read -r ver id value; do
  if [[ ! "$id" =~ ^[0-9]+$ ]]; then
    echo "::warning::Skipped indicator with unexpected id '${id}' (keyto ${ver}): not deleting"
    continue
  fi
  status="$(api DELETE "/api/indicators/${id}")" || status="000"
  if [[ "$status" == 2* || "$status" == 404 ]]; then
    echo "Pruned indicator ${id} (keyto ${ver}, ${value})"
  else
    echo "::warning::Could not delete indicator ${id} (keyto ${ver}): HTTP $status"
  fi
done < "$tmp/delete"
