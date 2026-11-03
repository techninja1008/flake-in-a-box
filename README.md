# flake-in-a-box

Publish a Nix flake **and** a pre-built binary cache for it from one HTTP
host. Consumers point `nix` at a single URL and get every package without
building it and without downloading unrelated repository contents.

From an existing flake, `flake-in-a-box.lib.mkBundle` builds:

1. **dist flakes** of the source flake, in one or more modes:
   - `ca-rewrite` (default): a new flake with no inputs. Each package is a
     `builtins.fetchClosure` of its runtime closure, **rewritten to
     content-addressed store paths**, so consumers need no trusted key.
   - `ia-signed`: the same kind of flake, but fetching the original
     input-addressed paths (`fetchClosure { inputAddressed = true; }`),
     signed by the server on the fly.
   - `filtered`: the source flake itself, trimmed to the files its outputs
     use (detected automatically, plus a `lib.fileset` you supply).
2. a **binary cache** of the runtime closure of every bundled package,
   including paths that nixpkgs (cache.nixos.org) already has.

`fiab serve` serves the bundle from a single web server. It derives a
per-host **wrapper flake** from the HTTP `Host` header, setting
`nixConfig.extra-substituters` to that host's `/cache`. It also signs
input-addressed narinfos as they are served; content-addressed ones need no
signature.

```
                       ┌──────────────────────── fiab serve ────────────────────────┐
nix build http://H/flake.tar.gz#pkg                                                   │
   │  GET /flake.tar.gz  ──────────► wrapper flake, templated from Host: H            │
   │                                  nixConfig.extra-substituters = http://H/cache   │
   │                                  inputs.bundle = http://H/dist/<mode>-<h>.tar.gz │
   │  GET /dist/…        ──────────► static main flake (same bytes for every host)   │
   │  GET /cache/<h>.narinfo ──────► narinfo (+ Sig: if input-addressed)             │
   │  GET /cache/nar/…   ──────────► NAR                                              │
                       └────────────────────────────────────────────────────────────┘
```

## Using it

Add flake-in-a-box to the flake that has the packages to publish:

```nix
{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-in-a-box.url = "github:techninja1008/flake-in-a-box";
    flake-in-a-box.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs = { self, nixpkgs, flake-in-a-box }:
    let
      pkgs = nixpkgs.legacyPackages.x86_64-linux;
      bundle = flake-in-a-box.lib.mkBundle {
        inherit pkgs;
        flake = self;            # or any flake input
        name = "my-project";
        description = "My project's packages";
        modes = [ "ca-rewrite" "ia-signed" "filtered" ];
        # filtered mode: files read at evaluation time (imports, readFile)
        extraFiles = pkgs.lib.fileset.unions [
          (pkgs.lib.fileset.fileFilter (f: f.hasExt "nix") ./.)
          ./version.txt
        ];
      };
    in {
      packages.x86_64-linux = {
        # ... your packages ...
        bundle = bundle;
        serve = flake-in-a-box.lib.mkServer { inherit pkgs bundle; };
        image = flake-in-a-box.lib.mkImage { inherit pkgs bundle; name = "my-project-cache"; };
      };
      # Optional (uses import-from-derivation): the trimmed flake evaluates
      # to exactly the same derivations.
      checks.x86_64-linux.bundle-filtered = bundle.verify;
    };
}
```

Generate a signing key once, keep it secret, and start the server:

```bash
nix key generate-secret --key-name my-project-1 > signing-key.sec
```

```bash
nix run .#serve -- --listen :8080 --signing-key-file signing-key.sec
```

Or run the OCI image, which has the bundle built in:

```bash
nix build .#image && docker load < result
```

```bash
docker run -p 8080:8080 -e FIAB_SIGNING_KEY="$(cat signing-key.sec)" my-project-cache
```

`GET /` on the server prints the URLs and what each mode needs.

### `mkBundle` arguments

| Argument | Default | Meaning |
| --- | --- | --- |
| `pkgs` | (required) | Package set used for the build tools |
| `flake` | (required) | The source flake (`self`, or an input) |
| `name`, `description` | `"bundle"`, `""` | Shown in wrappers and on the index page |
| `systems` | all in `flake.packages` | Systems to bundle. Building a closure needs a builder for that system. |
| `includePackage` | everything | `system: name: pkg: bool` |
| `modes` | `[ "ca-rewrite" ]` | Any of `ca-rewrite`, `ia-signed`, `filtered`. The first is also served as `/flake.tar.gz`. |
| `caVariants` | `[ "positions" "legacy" ]` | CA hashing variants to build (see [below](#content-addressed-paths-and-nix-235)) |
| `compression` | `"zstd"` | NAR compression: `zstd`, `xz` or `none` |
| `src` | `flake.outPath` | `filtered` mode: the tree to trim |
| `extraFiles` | every `.nix` file | `filtered` mode: files to keep on top of the detected ones. `flake.nix` and `flake.lock` are always kept. |

The result is a directory holding `bundle.json`, `dist/*.tar.gz` and
`cache/*`. It has passthru attributes `dists`, `caches`, `filteredTree` and
`verify`.

Only `packages` outputs are bundled, and the cache holds their runtime
closures (not build-time inputs).

## Consuming a bundle

| URL | Mode | The consumer needs |
| --- | --- | --- |
| `http://H/ca-rewrite.tar.gz` | content-addressed `fetchClosure` | `fetch-closure` enabled. No trusted key. |
| `http://H/ia-signed.tar.gz` | input-addressed `fetchClosure` | `fetch-closure`, plus the host's key trusted |
| `http://H/filtered.tar.gz` | the trimmed source flake | The host's key trusted. The flake's own inputs (e.g. nixpkgs) are fetched normally. |
| `http://H/flake.tar.gz` | the first mode | |

```bash
nix run --extra-experimental-features fetch-closure --accept-flake-config 'http://H/flake.tar.gz#hello'
```

Packages in the `ca-rewrite` and `ia-signed` flakes are small
`builtin:buildenv` derivations, which build locally on any system with no
shell. They link to the fetched store paths and carry the original `meta`
(`description`, `license`, `mainProgram`, …), `pname` and `version`, so
`nix build`, `nix run`, `nix shell` and `nix search` work. The fetched paths
of every output are available as `pkg.storePaths.<output>`.

Trust, in practice:

- **`ca-rewrite`** works for any user, including untrusted users on a
  multi-user install, with no keys configured. The store path is the hash of
  the content, so integrity comes from whoever wrote the hash: the lock file
  and the server you fetched the flake from (the same model as `fetchurl`
  with a pinned hash).
- **`ia-signed`** and **`filtered`** need the host's key trusted. A flake's
  `nixConfig` only supplies it to trusted users (or caches an admin has
  already allowed). For everyone else the daemon ignores
  `extra-substituters` and `extra-trusted-public-keys`.

## Server

```
fiab serve --bundle DIR [--listen :8080] [--signing-key-file FILE]
           [--base-url URL] [--trust-forwarded] [--allowed-hosts h1,h2]
           [--cache DIR ...]
```

| Flag | Environment | Meaning |
| --- | --- | --- |
| `--bundle` | `FIAB_BUNDLE` | Bundle directory (`mkServer` and `mkImage` build it in) |
| `--listen` | `FIAB_LISTEN` | Listen address |
| `--signing-key-file` | `FIAB_SIGNING_KEY_FILE`, or the key itself in `FIAB_SIGNING_KEY` | Nix secret key (`name:base64`). Without one, input-addressed narinfos are served unsigned. |
| `--base-url` | `FIAB_BASE_URL` | Fixed public URL instead of one derived from `Host` |
| `--trust-forwarded` | `FIAB_TRUST_FORWARDED=1` | Use `X-Forwarded-Host` and `X-Forwarded-Proto` (behind a reverse proxy) |
| `--allowed-hosts` | `FIAB_ALLOWED_HOSTS` | Reject other `Host` headers |
| `--cache` | | Extra cache directories. With no `--bundle`, serve only caches. |

Routes:

| Path | Content |
| --- | --- |
| `/` | Index: URLs, public key, what each mode needs |
| `/flake.tar.gz`, `/<mode>.tar.gz` | Per-host wrapper flake (`Vary: Host`, `Link: <…/immutable/<id>/<mode>.tar.gz>; rel="immutable"`) |
| `/immutable/<id>/<mode>.tar.gz` | The same wrapper at a stable URL. 404 if `<id>` was generated for another host. |
| `/dist/<mode>-<hash>.tar.gz` | Static main flake (`Cache-Control: immutable`) |
| `/cache/nix-cache-info`, `/cache/<hash>.narinfo`, `/cache/nar/…` | Binary cache. Input-addressed narinfos get `Sig:` on the fly. |
| `/public-key` | The public key, if signing |

`Host` is validated before it is templated into a wrapper. Signed narinfos
are cached in memory.

The wrapper carries a complete `flake.lock`. The main flake is locked by
URL and NAR hash, and in `filtered` mode the main flake's own lock nodes are
grafted underneath. Nix records the immutable URL, so a lock taken from host
A keeps pointing at host A. Switching mirrors means re-locking, because the
wrapper differs per host.

## How it works

### Content-addressed rewrite (`fiab rewrite`)

`ca-rewrite` caches are produced inside an ordinary derivation, with no IFD:
`exportReferencesGraph` puts the runtime closure in the sandbox, and `fiab`
reimplements `nix store make-content-addressed`. It serialises each path to a
NAR, rewrites references to already-rewritten dependencies, hashes the result
with self-references masked, computes the `fixed:r:sha256` store path, and
writes the final NAR and a narinfo with a `CA:` line. The tests check the
output byte for byte against Nix.

The CA store paths are only known after this build, and that's fine: the
dist flake is generated by a later derivation that reads the old→new path map.

### Content-addressed paths and Nix 2.35

Nix 2.17–2.34 lost the self-reference positions from the CA hash.
Nix 2.35 restored them ([NixOS/nix#15931](https://github.com/NixOS/nix/pull/15931)).
Any path that refers to itself (almost every package) therefore gets a
**different content-addressed store path** depending on the Nix version, and
a client accepts only its own variant.

`mkBundle` builds both variants by default (`caVariants`). The dist flake
picks one with `builtins.nixVersion`, and both are always available as
`legacyPackages.<system>.variants.{positions,legacy}.<name>`. The daemon
that verifies the paths can be a different version from the evaluating
client. If it is, select the variant explicitly. Lix and other forks that
report a higher version number but kept the old behaviour also need the
explicit `legacy` variant. Each variant is a full copy of the closure, so
drop the one your consumers don't use.

### Input-addressed cache

Input-addressed closures are cached with nixpkgs' `mkBinaryCache`, vendored
in `nix/binary-cache` with a two-line patch (`ca-lines.patch`, suitable for
upstreaming) so that narinfos of content-addressed paths carry a `CA:` line.
Without it, clients that trust no keys reject even content-addressed paths.

### Keeping the server small

A binary cache that is a derivation output would normally reference every
path it contains: Nix's reference scanner finds their hashes in the narinfos.
To prevent that, caches are packed: all narinfos go in one gzip-compressed
`narinfos.json.gz`, and NAR files are copied. Dist flakes store paths split
in two. The bundle's closure is therefore just the bundle and its caches,
which keeps the OCI image small.

### Filtered mode

`fiab detect-srcs` takes the derivation closure of every bundled package (via
`exportReferencesGraph` on the `drvPath`s) and finds the sources that come
from the flake tree:

- files copied from `./foo`
- directories from `./dir`, `lib.fileset.toSource`, `cleanSource` or `src = self`

These are matched byte for byte at the same relative paths. Files read only
during evaluation never reach a derivation, so they must be listed in
`extraFiles` (default: every `.nix` file). `bundle.verify` evaluates the
trimmed flake with `builtins.getFlake` and checks that every `drvPath`
matches the original. It uses import-from-derivation.

## Findings

- A flake's `nixConfig.extra-experimental-features = fetch-closure` does
  **not** make `builtins.fetchClosure` available, because builtins are fixed
  before the flake's config is read. Consumers must enable `fetch-closure`
  themselves. The dist flake fails with a message saying so.
- `nixConfig` substituters and keys are ignored for untrusted users on a
  multi-user install. `ca-rewrite` works for them anyway, and
  `tests/vm.nix` covers both sides.
- The Lockable HTTP Tarball protocol works as expected. With `Link: <url>;
  rel="immutable"` (exactly that form), Nix locks the wrapper under the
  immutable URL. It must be a tarball URL.
- Nix caches tarball downloads for `tarball-ttl` (one hour by default). After
  redeploying a bundle, consumers see the new wrapper only after that
  interval, or with `--refresh`.
- `builtins.fetchClosure` accepts store path strings as well as path values.

## Limitations

- Only `packages` outputs are bundled, with runtime closures only. Rebuilding
  from source still needs the original inputs. Use the original flake, or the
  `filtered` dist.
- `devShells`, `nixosConfigurations` and functions such as overlays or `lib`
  are not carried into the `ca-rewrite` and `ia-signed` dists. `filtered`
  keeps every output of the source flake.
- `filtered` does not handle `path:` flake inputs (such as relative sub-flakes) of the source flake.
- Paths of a different system need a builder for that system when the bundle
  is built.

## Development and tests

```bash
nix build .#fiab
```

```bash
tests/local.sh
```

```bash
nix build .#checks.x86_64-linux.vm
```

- `nix build .#fiab` also runs the Go unit tests, whose vectors come from real
  Nix: NAR hashes, `nix store sign` signatures, and `make-content-addressed`
  results from Nix 2.34 and 2.35.
- `tests/local.sh` reproduces every row of the brief's verified findings
  table, then serves the sample bundle under two hostnames and consumes all
  three modes from fresh chroot stores.
- `checks.<system>.vm` is a NixOS VM test with a multi-user daemon, an
  untrusted and a trusted user, and two hostnames. It needs KVM to run at a
  reasonable speed.
- `checks.<system>.sample-filtered-verify` is the filtered mode's drvPath
  check for `examples/sample`.

`examples/sample` is the sample flake. It has a package that reads a file at
evaluation time, one built from a `lib.fileset` source, and nixpkgs' `hello`,
for two systems.
