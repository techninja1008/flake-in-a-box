#!/usr/bin/env bash
# End-to-end tests that run without a VM, using fresh local chroot stores as
# clean clients (`--store DIR`). They reproduce every row of the "Verified
# findings" table of the design brief, then exercise a served bundle in all
# three modes under two hostnames.
#
# Usage: tests/local.sh            (from the repository; needs nix with flakes)
#
# Run as a user that is trusted by the Nix daemon (or root): the clients
# accept flake nixConfig. The multi-user trust rules are covered by the
# NixOS VM test (tests/vm.nix).
set -euo pipefail

REPO=$(cd "$(dirname "$0")/.." && pwd)
# Chroot stores refuse world-writable ancestors such as /tmp.
WORK=${FIAB_TEST_DIR:-$HOME/.cache/fiab-local-test}
rm -rf "$WORK"
mkdir -p "$WORK/stores"
export NO_PROXY="localhost,127.0.0.1${NO_PROXY:+,$NO_PROXY}" no_proxy="localhost,127.0.0.1${no_proxy:+,$no_proxy}"
NIX="nix --extra-experimental-features nix-command --extra-experimental-features flakes"
SYSTEM=$($NIX eval --impure --raw --expr builtins.currentSystem)

pass=0
fail=0
log() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
ok() { printf '  \033[32mPASS\033[0m %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; fail=$((fail + 1)); }

build() { $NIX build --no-link --print-out-paths "$REPO#$1"; }

# client NAME ARGS...: run nix against a fresh, empty store that trusts no
# keys and has no substituters.
client() {
  local store="$WORK/stores/$1"
  shift
  rm -rf "$store"
  $NIX --store "$store" \
    --option substituters '' \
    --option trusted-public-keys '' \
    --option require-sigs true \
    --option narinfo-cache-negative-ttl 0 \
    --option narinfo-cache-positive-ttl 0 \
    --option tarball-ttl 0 \
    --extra-experimental-features fetch-closure \
    "$@"
}

# expect_ok DESC CMD...   /   expect_fail DESC PATTERN CMD...
expect_ok() {
  local desc=$1
  shift
  if out=$("$@" 2>&1); then ok "$desc"; else bad "$desc"; printf '%s\n' "$out" | tail -5 | sed 's/^/      /'; fi
}
expect_fail() {
  local desc=$1 pattern=$2
  shift 2
  if out=$("$@" 2>&1); then
    bad "$desc (unexpectedly succeeded)"
  elif grep -q -- "$pattern" <<<"$out"; then
    ok "$desc"
  else
    bad "$desc (failed differently)"
    printf '%s\n' "$out" | tail -5 | sed 's/^/      /'
  fi
}
expect_output() {
  local desc=$1 pattern=$2
  shift 2
  if out=$("$@" 2>&1) && grep -q -- "$pattern" <<<"$out"; then ok "$desc"; else bad "$desc"; printf '%s\n' "$out" | tail -5 | sed 's/^/      /'; fi
}

log "building"
FIAB=$(build fiab)/bin/fiab
F="legacyPackages.$SYSTEM.fiabTests.fixture"
SRC=$(build "$F.src")
TXT=$(build "$F.txt")
FOD=$(build "$F.fod")
IA=$(build "$F.ia")
STOCK=$(build "$F.stock")
PATCHED=$(build "$F.patched")
BUNDLE=$(build sample-bundle)
NIXPKGS_SRC=$($NIX eval --raw --impure --expr "(builtins.getFlake \"$REPO\").inputs.nixpkgs.outPath")
echo "fiab $FIAB"

$NIX key generate-secret --key-name fiab-local-test-1 >"$WORK/key.sec"
$NIX key convert-secret-to-public <"$WORK/key.sec" >"$WORK/key.pub"
PUB=$(cat "$WORK/key.pub")

PIDS=()
cleanup() { for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done; }
trap cleanup EXIT
serve() { # PORT ARGS...
  local port=$1
  shift
  "$FIAB" serve --listen "127.0.0.1:$port" "$@" >"$WORK/server-$port.log" 2>&1 &
  PIDS+=($!)
  for _ in $(seq 50); do curl -fs "http://127.0.0.1:$port/cache/nix-cache-info" >/dev/null && return; sleep 0.1; done
  echo "server on $port did not start" >&2
  cat "$WORK/server-$port.log" >&2
  exit 1
}
P_STOCK=18081 P_PATCHED=18082 P_SIGNED=18083 P_BUNDLE=18084
serve $P_STOCK --cache "$STOCK"
serve $P_PATCHED --cache "$PATCHED"
serve $P_SIGNED --cache "$PATCHED" --signing-key-file "$WORK/key.sec"
serve $P_BUNDLE --bundle "$BUNDLE" --signing-key-file "$WORK/key.sec"
U_STOCK=http://127.0.0.1:$P_STOCK/cache
U_PATCHED=http://127.0.0.1:$P_PATCHED/cache
U_SIGNED=http://127.0.0.1:$P_SIGNED/cache

# ---------------------------------------------------------------------------
log "verified findings: CA lines"
n=$( (grep -l "^CA: " "$STOCK"/*.narinfo 2>/dev/null || true) | wc -l)
[ "$n" = 0 ] && ok "stock mkBinaryCache writes no CA lines" || bad "stock wrote $n CA lines"
n=$(grep -h '^CA: ' "$PATCHED"/*.narinfo | wc -l)
methods=$(grep -h '^CA: ' "$PATCHED"/*.narinfo | sed 's/^CA: //' | cut -d: -f1,2 | sort | tr '\n' ' ')
[ "$n" = 3 ] && ok "patched writes 3 of 3 CA lines ($methods)" || bad "patched wrote $n CA lines"
[[ "$methods" == "fixed:r fixed:sha256 text:sha256 " ]] && ok "CA methods are correct" || bad "CA methods: $methods"
h=$(basename "$IA" | cut -c1-32)
grep -q '^CA:' "$PATCHED/$h.narinfo" && bad "ia-thing has a CA line" || ok "no CA line on the input-addressed path"

log "verified findings: nix copy and substitution"
expect_fail "stock: nix copy of a CA path is rejected" "lacks a signature" \
  client copy1 copy --from "$U_STOCK" "$SRC"
expect_ok "patched: nix copy of the 3 CA paths is accepted" \
  client copy2 copy --from "$U_PATCHED" "$SRC" "$TXT" "$FOD"
expect_fail "patched: nix copy of the IA path is rejected" "lacks a signature" \
  client copy3 copy --from "$U_PATCHED" "$IA"
expect_ok "patched: substitution of the CA paths is accepted" \
  client subst1 --option substituters "$U_PATCHED" build --no-link "$SRC" "$TXT" "$FOD"

log "verified findings: fetchClosure"
mkflake() { # DIR STORE_URL EXTRA_ATTRS PATH
  mkdir -p "$1"
  cat >"$1/flake.nix" <<EOF
{
  outputs = { self }: {
    packages.$SYSTEM.default = builtins.fetchClosure {
      fromStore = "$2";
      fromPath = $4;
      $3
    };
  };
}
EOF
}
mkflake "$WORK/fc-stock" "$U_STOCK" "" "$SRC"
mkflake "$WORK/fc-patched" "$U_PATCHED" "" "$SRC"
mkflake "$WORK/fc-ia" "$U_PATCHED" "" "$IA"
mkflake "$WORK/fc-ia2" "$U_PATCHED" "inputAddressed = true;" "$IA"
mkflake "$WORK/fc-ia-signed" "$U_SIGNED" "inputAddressed = true;" "$IA"
expect_fail "stock: fetchClosure of a CA path in a pure flake is rejected" "signature" \
  client fc1 build --no-link "path:$WORK/fc-stock"
expect_ok "patched: fetchClosure of a CA path in a pure flake is accepted" \
  client fc2 build --no-link "path:$WORK/fc-patched"
expect_fail "patched, unsigned: fetchClosure of the IA path is rejected" "signature" \
  client fc3 build --no-link "path:$WORK/fc-ia"
expect_fail "patched, unsigned: fetchClosure { inputAddressed = true; } is rejected" "signature" \
  client fc4 build --no-link "path:$WORK/fc-ia2"
TOPATH=$($NIX store make-content-addressed --store "$WORK/stores/mca" --from "$U_STOCK" --json "$IA" 2>/dev/null |
  python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("rewrites", d)[sys.argv[1]])' "$IA" || true)
if [ -n "$TOPATH" ]; then
  mkflake "$WORK/fc-rewrite" "$U_STOCK" "toPath = $TOPATH;" "$IA"
  expect_ok "stock, unsigned: fetchClosure rewrite mode (toPath) is accepted" \
    client fc5 build --no-link "path:$WORK/fc-rewrite"
else
  bad "could not compute toPath with nix store make-content-addressed"
fi

log "verified findings: signing server"
expect_fail "signed, key not trusted: nix copy of the IA path is rejected" "lacks a signature" \
  client sig1 copy --from "$U_SIGNED" "$IA"
expect_ok "signed, key trusted: nix copy of the IA path is accepted (with its CA references)" \
  client sig2 --option trusted-public-keys "$PUB" copy --from "$U_SIGNED" "$IA"
expect_ok "signed, key trusted: fetchClosure { inputAddressed = true; } is accepted" \
  client sig3 --option trusted-public-keys "$PUB" build --no-link "path:$WORK/fc-ia-signed"
expect_output "server signature equals nix store sign (adds 0 signatures)" "added 0 signatures" \
  $NIX store sign --store "$WORK/stores/sig2" -k "$WORK/key.sec" "$IA"

# ---------------------------------------------------------------------------
GREETING="Hello from the flake-in-a-box sample!"
for host in "localhost:$P_BUNDLE" "127.0.0.1:$P_BUNDLE"; do
  base="http://$host"
  tag=${host%%:*}
  log "bundle served as $base"

  expect_output "wrapper flake points at $base" "\"$base/cache\"" \
    sh -c "curl -fsS '$base/flake.tar.gz' | tar xzO flake/flake.nix"
  expect_output "wrapper is locked through its immutable URL" "$base/immutable/" \
    client "meta-$tag" flake metadata --json "$base/flake.tar.gz"

  expect_output "ca-rewrite: nix run with no trusted keys" "$GREETING" \
    client "ca-$tag" --accept-flake-config run "$base/ca-rewrite.tar.gz#greet" -- ca
  expect_output "ca-rewrite: nix shell" "data from the fileset source" \
    client "ca2-$tag" --accept-flake-config shell "$base/flake.tar.gz#fileset-tool" -c fileset-tool
  expect_output "ca-rewrite: explicit legacy variant" "$GREETING" \
    client "ca3-$tag" --accept-flake-config run "$base/ca-rewrite.tar.gz#legacyPackages.$SYSTEM.variants.legacy.greet"
  expect_output "ca-rewrite: metadata survives (nix search)" '"description":"Greets you, via GNU hello"' \
    client "ca4-$tag" --accept-flake-config search --json "$base/ca-rewrite.tar.gz" greet

  expect_fail "ia-signed: rejected when the flake config is not accepted" "signature" \
    client "ia-$tag" --option accept-flake-config false run "$base/ia-signed.tar.gz#greet"
  expect_output "ia-signed: nix run with the flake config accepted" "$GREETING" \
    client "ia2-$tag" --accept-flake-config run "$base/ia-signed.tar.gz#greet" -- ia

  # The filtered flake evaluates nixpkgs from its lock file; seed its source
  # so the client does not need GitHub.
  store="$WORK/stores/filtered-$tag"
  rm -rf "$store"
  $NIX copy --no-check-sigs --to "$store" "$NIXPKGS_SRC" >/dev/null 2>&1
  dry=$($NIX --store "$store" --option substituters '' --option trusted-public-keys '' --option tarball-ttl 0 \
    --accept-flake-config build --dry-run "$base/filtered.tar.gz#greet" 2>&1 || true)
  if grep -q "will be built" <<<"$dry"; then bad "filtered: something would be built"; printf '%s\n' "$dry" | sed 's/^/      /'; else ok "filtered: nothing to build, all substitutable"; fi
  if out=$($NIX --store "$store" --option substituters '' --option trusted-public-keys '' --option tarball-ttl 0 \
    --accept-flake-config run "$base/filtered.tar.gz#greet" -- filtered 2>&1) && grep -q "$GREETING" <<<"$out"; then
    ok "filtered: nix run substitutes from the host's cache"
  else
    bad "filtered: nix run"
    printf '%s\n' "$out" | tail -5 | sed 's/^/      /'
  fi
done

log "result: $pass passed, $fail failed"
[ "$fail" = 0 ]
