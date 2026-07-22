#!/usr/bin/env sh
# Cloak installer: the privacy layer for AI.
#   curl -fsSL https://raw.githubusercontent.com/PrateekKumar1709/cloak/main/scripts/install.sh | sh
set -e

REPO="${CLOAK_REPO:-https://github.com/PrateekKumar1709/cloak}"
PREFIX="${CLOAK_PREFIX:-$HOME/.local}"
BIN="$PREFIX/bin"

say() { printf "\033[1;32m›\033[0m %s\n" "$1"; }

if ! command -v go >/dev/null 2>&1; then
  echo "Cloak builds from source and needs Go 1.25+."
  echo "Install Go from https://go.dev/dl/ then re-run this script."
  exit 1
fi

GO_MAJOR=$(go env GOVERSION 2>/dev/null | sed -n 's/^go\([0-9][0-9]*\)\.\([0-9][0-9]*\).*/\1/p')
GO_MINOR=$(go env GOVERSION 2>/dev/null | sed -n 's/^go\([0-9][0-9]*\)\.\([0-9][0-9]*\).*/\2/p')
if [ -z "$GO_MAJOR" ] || [ -z "$GO_MINOR" ] || [ "$GO_MAJOR" -lt 1 ] || { [ "$GO_MAJOR" -eq 1 ] && [ "$GO_MINOR" -lt 25 ]; }; then
  echo "Cloak needs Go 1.25+ (found: $(go env GOVERSION 2>/dev/null || echo unknown))."
  exit 1
fi

mkdir -p "$BIN"

# Prefer building from a local checkout, else clone.
if [ -f "./go.mod" ] && grep -q "PrateekKumar1709/cloak" ./go.mod 2>/dev/null; then
  say "Building Cloak from local checkout…"
  go build -ldflags "-s -w" -o "$BIN/cloak" ./cmd/cloak
else
  TMP="$(mktemp -d)"
  say "Cloning $REPO…"
  git clone --depth 1 "$REPO" "$TMP/cloak"
  say "Building Cloak…"
  ( cd "$TMP/cloak" && go build -ldflags "-s -w" -o "$BIN/cloak" ./cmd/cloak )
  rm -rf "$TMP"
fi

say "Installed: $BIN/cloak"
case ":$PATH:" in
  *":$BIN:"*) : ;;
  *) echo "  Add to PATH:  export PATH=\"$BIN:\$PATH\"" ;;
esac
echo
say "Next steps:"
echo "  cloak doctor        # check Lemonade + models"
echo "  cloak demo          # Lemonade + mock cloud + dashboard"
echo "  open http://127.0.0.1:7777"
