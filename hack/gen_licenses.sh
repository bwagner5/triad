#!/usr/bin/env bash
set -euo pipefail

# Generates ATTRIBUTION.md from Go module dependencies.
# Includes a summary table and full license texts.

go mod download

GOMODCACHE=$(go env GOMODCACHE)
TMPFILE=$(mktemp)
trap 'rm -f "$TMPFILE"' EXIT

# Collect module info: mod|license_type|license_file
go list -m -json all 2>/dev/null | \
  jq -r 'select(.Main != true and .Indirect != true) | "\(.Path)@\(.Version)"' | \
  sort | while IFS= read -r mod; do
    moddir="${GOMODCACHE}/${mod}"
    license_type="Unknown"
    license_file=""
    for f in LICENSE LICENSE.md LICENSE.txt COPYING; do
      if [[ -f "${moddir}/${f}" ]]; then
        license_file="${moddir}/${f}"
        head -5 "$license_file" 2>/dev/null | grep -qi "apache" && license_type="Apache-2.0" && break
        head -5 "$license_file" 2>/dev/null | grep -qi "mit " && license_type="MIT" && break
        head -5 "$license_file" 2>/dev/null | grep -qi "bsd" && license_type="BSD" && break
        license_type="See LICENSE" && break
      fi
    done
    echo "${mod}|${license_type}|${license_file}" >> "$TMPFILE"
  done

{
  echo "# Open Source Software Attribution"
  echo ""
  echo "This software includes the following third-party dependencies:"
  echo ""

  # Summary
  while IFS='|' read -r mod license_type license_file; do
    echo "- **${mod}** — ${license_type}"
  done < "$TMPFILE"

  echo ""
  echo "---"
  echo ""

  # Full license texts
  while IFS='|' read -r mod license_type license_file; do
    echo "## ${mod}"
    echo ""
    if [[ -n "$license_file" && -f "$license_file" ]]; then
      echo '```'
      cat "$license_file"
      echo '```'
    else
      echo "License file not found."
    fi
    echo ""
  done < "$TMPFILE"
} > ATTRIBUTION.md

echo "wrote ATTRIBUTION.md ($(wc -l < ATTRIBUTION.md | tr -d ' ') lines)"
