#!/usr/bin/env bash
set -euo pipefail

if ! command -v git-secrets >/dev/null 2>&1; then
  echo "git-secrets is required for PEM scanning" >&2
  exit 1
fi

# Build the forbidden PEM header without embedding it verbatim in the repo.
part="PRIVATE KEY"
pattern="-----BEGIN ${part}-----"

# Ensure the forbidden pattern is registered locally before scanning. Duplicate
# registrations are ignored by git-secrets.
git secrets --add "$pattern" >/dev/null 2>&1 || true

# Scan tracked and staged files for PEM private key headers. The explicit
# pattern avoids false positives from documentation referring to the phrase
# indirectly.
git secrets --scan --cached
