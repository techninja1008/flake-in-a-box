# flake-in-a-box: build a servable bundle from an existing flake.
#
# The result is a directory with
#   bundle.json            manifest read by `fiab serve`
#   dist/<mode>-<h>.tar.gz one static main flake per mode
#   cache/<name>/          packed binary caches (narinfos.json.gz + nar/)
#
# Everything here is pure: no IFD except in the optional `verify` check of
# the filtered mode.
{
  lib,
  pkgs,
  fiab,
  mkBinaryCache,
}:

{
  # The source flake, e.g. `self` or `inputs.foo`.
  flake,
  name ? "bundle",
  description ? "",
  # Systems whose packages are bundled. Building closures for a system needs
  # a builder for it.
  systems ? builtins.attrNames (flake.packages or { }),
  # Which packages to bundle: a predicate `system: name: package: bool`.
  includePackage ? _system: _name: _pkg: true,
  # Dist flavours; the first one is also served as /flake.tar.gz.
  #   ca-rewrite  closures rewritten to content-addressed paths and fetched
  #               with builtins.fetchClosure; consumers need no trusted key.
  #   ia-signed   the original input-addressed paths via fetchClosure
  #               { inputAddressed = true; }; signed by the server.
  #   filtered    the source flake itself, trimmed to the files it uses.
  modes ? [ "ca-rewrite" ],
  # Self-reference hashing variants for ca-rewrite: "positions" (Nix >= 2.35)
  # and/or "legacy" (Nix 2.17 - 2.34). Each variant is a full closure copy.
  caVariants ? [
    "positions"
    "legacy"
  ],
  compression ? "zstd",
  # filtered mode: the source tree and extra files to keep on top of the
  # automatically detected ones (files read at evaluation time, such as
  # imported .nix files). Defaults to every .nix file plus flake.lock.
  src ? flake.outPath,
  extraFiles ? null,
}:

let
  inherit (lib)
    concatMap
    concatMapStringsSep
    filterAttrs
    genAttrs
    mapAttrs
    optional
    unique
    ;

  knownModes = [
    "ca-rewrite"
    "ia-signed"
    "filtered"
  ];

  checked =
    assert lib.assertMsg (modes != [ ]) "flake-in-a-box: modes must not be empty";
    assert lib.assertMsg (lib.all (m: lib.elem m knownModes) modes)
      "flake-in-a-box: unknown mode in ${toString modes}; known: ${toString knownModes}";
    assert lib.assertMsg (lib.unique modes == modes) "flake-in-a-box: duplicate modes";
    assert lib.assertMsg (
      !(lib.elem "ca-rewrite" modes) || caVariants != [ ]
    ) "flake-in-a-box: ca-rewrite needs at least one caVariant";
    assert lib.assertMsg (lib.all (v: lib.elem v [
      "positions"
      "legacy"
    ]) caVariants) "flake-in-a-box: caVariants must be positions and/or legacy";
    assert lib.assertMsg (lib.elem compression [
      "none"
      "zstd"
      "xz"
    ]) "flake-in-a-box: compression must be none, zstd or xz";
    true;

  has = m: lib.elem m modes;

  # ---------------------------------------------------------------------
  # Package selection and metadata (pure evaluation).

  selected = genAttrs systems (
    system: filterAttrs (n: p: lib.isDerivation p && includePackage system n p) (flake.packages.${system} or { })
  );

  # JSON-safe copy of a value: drops functions, derivations, paths and
  # strings carrying store context, and limits depth.
  clean =
    depth: v:
    let
      t = builtins.typeOf v;
    in
    if t == "string" then
      (if builtins.hasContext v then null else v)
    else if
      lib.elem t [
        "int"
        "float"
        "bool"
        "null"
      ]
    then
      v
    else if t == "list" then
      (if depth == 0 then null else builtins.filter (x: x != null) (map (clean (depth - 1)) v))
    else if t == "set" then
      (
        if depth == 0 || lib.isDerivation v then
          null
        else
          filterAttrs (_: x: x != null) (mapAttrs (_: clean (depth - 1)) v)
      )
    else
      null;

  metaKeys = [
    "description"
    "longDescription"
    "homepage"
    "downloadPage"
    "changelog"
    "license"
    "mainProgram"
    "platforms"
    "badPlatforms"
    "broken"
    "unfree"
    "insecure"
    "knownVulnerabilities"
    "priority"
    "outputsToInstall"
    "maintainers"
    "teams"
  ];

  safeMeta =
    meta:
    lib.listToAttrs (
      concatMap (
        k:
        let
          r = builtins.tryEval (builtins.deepSeq (clean 4 meta.${k}) (clean 4 meta.${k}));
        in
        optional (meta ? ${k} && r.success && r.value != null) {
          name = k;
          value = r.value;
        }
      ) metaKeys
    );

  pkgInfo =
    n: p:
    let
      outputs = p.outputs or [ "out" ];
      meta = p.meta or { };
    in
    {
      name = p.name or n;
      pname = p.pname or null;
      version = p.version or null;
      inherit outputs;
      outputsToInstall = meta.outputsToInstall or [ (builtins.head outputs) ];
      meta = safeMeta meta;
      paths = genAttrs outputs (o: p.${o}.outPath);
    };

  infos = mapAttrs (_: mapAttrs pkgInfo) selected;

  # Output paths with their string context: the closure roots.
  rootPaths = unique (
    concatMap (
      system: concatMap (i: builtins.attrValues i.paths) (builtins.attrValues infos.${system})
    ) systems
  );

  # Metadata JSON. Its string context keeps the output paths as inputs, so
  # the dist builders can inspect them (e.g. whether an output is a directory).
  preJson = pkgs.writeText "${name}-packages-pre.json" (builtins.toJSON { packages = infos; });

  # ---------------------------------------------------------------------
  # Binary caches.

  caCache =
    variant:
    pkgs.stdenvNoCC.mkDerivation {
      name = "${name}-ca-cache-${variant}";
      __structuredAttrs = true;
      exportReferencesGraph.closure = rootPaths;
      outputs = [
        "out"
        "map"
      ];
      nativeBuildInputs = [
        fiab
      ]
      ++ optional (compression == "zstd") pkgs.zstd
      ++ optional (compression == "xz") pkgs.xz;
      buildCommand = ''
        fiab rewrite --variant ${variant} --compression ${compression} \
          --jobs "$NIX_BUILD_CORES" --out "$out" --map map.json
        mkdir -p "$map"
        cp map.json "$map/map.json"
      '';
    };

  caCaches = genAttrs caVariants caCache;

  # Input-addressed runtime closures via (patched) mkBinaryCache, then
  # packed so the cache does not reference the paths it contains.
  iaCacheFlat = mkBinaryCache {
    name = "${name}-ia-cache-flat";
    inherit compression rootPaths;
  };

  iaCache = pkgs.runCommand "${name}-ia-cache" { nativeBuildInputs = [ fiab ]; } ''
    fiab pack-cache --out "$out" ${iaCacheFlat}
  '';

  needIA = has "ia-signed" || has "filtered";

  caches =
    (lib.optionalAttrs (has "ca-rewrite") (lib.mapAttrs' (v: c: lib.nameValuePair "ca-${v}" c) caCaches))
    // lib.optionalAttrs needIA { ia = iaCache; };

  # ---------------------------------------------------------------------
  # Dist flakes. Each dist derivation produces flake.tar.gz plus info.json.

  packDist = mode: ''
    tar --sort=name --mtime=@1 --owner=0 --group=0 --numeric-owner \
      -cf - flake | gzip -9n > "$out/flake.tar.gz"
    narHash=$(fiab nar-hash flake)
    python3 -c 'import json,sys; json.dump({"narHash": sys.argv[1]}, open(sys.argv[2], "w"))' \
      "$narHash" "$out/info.json"
  '';

  libDist =
    mode:
    pkgs.runCommand "${name}-dist-${mode}"
      {
        nativeBuildInputs = [
          fiab
          pkgs.python3
        ];
      }
      ''
        mkdir -p "$out"
        python3 ${./dist/gen-dist.py} --mode ${mode} --pre ${preJson} \
          --outputs-nix ${./dist/outputs.nix} \
          --description ${lib.escapeShellArg description} \
          ${
            lib.optionalString (mode == "ca-rewrite") (
              concatMapStringsSep " " (v: "--map ${v}=${caCaches.${v}.map}/map.json") caVariants
            )
          } \
          --out flake
        ${packDist mode}
      '';

  # filtered mode -------------------------------------------------------

  # lib.fileset needs a path value; flake sources are store path strings.
  srcPath = if builtins.isPath src then src else /. + builtins.unsafeDiscardStringContext (toString src);

  mandatoryFiles = lib.fileset.unions (
    [ (srcPath + "/flake.nix") ]
    ++ optional (builtins.pathExists (srcPath + "/flake.lock")) (srcPath + "/flake.lock")
  );

  extraTree = lib.fileset.toSource {
    root = srcPath;
    fileset = lib.fileset.unions [
      mandatoryFiles
      (if extraFiles == null then lib.fileset.fileFilter (f: f.hasExt "nix") srcPath else extraFiles)
    ];
  };

  drvPaths = unique (
    concatMap (
      system: map (p: p.drvPath) (builtins.attrValues selected.${system})
    ) systems
  );

  # Output names re-exported by the wrapper.
  flakeOutputNames = builtins.filter (
    n:
    !(lib.elem n [
      "outPath"
      "inputs"
      "outputs"
      "sourceInfo"
      "narHash"
      "lastModified"
      "lastModifiedDate"
      "rev"
      "shortRev"
      "dirtyRev"
      "dirtyShortRev"
      "revCount"
      "submodules"
      "_type"
    ])
  ) (builtins.attrNames flake);

  filteredTree = pkgs.stdenvNoCC.mkDerivation {
    name = "${name}-filtered-src";
    __structuredAttrs = true;
    exportReferencesGraph.drvs = drvPaths;
    nativeBuildInputs = [ fiab ];
    inherit src;
    extra = extraTree;
    buildCommand = ''
      fiab detect-srcs --tree "$src" --attr drvs > detected.txt
      echo "detected $(wc -l < detected.txt) source file(s) used by derivations"
      mkdir -p "$out"
      # The extra (evaluation-time) files...
      cp -r --preserve=mode "$extra/." "$out/"
      chmod -R u+w "$out"
      # ...plus the files used by derivations, from the original tree.
      while IFS= read -r f; do
        mkdir -p "$out/$(dirname "$f")"
        cp -P --preserve=mode "$src/$f" "$out/$f"
        chmod u+w "$out/$f" 2>/dev/null || true
      done < detected.txt
      chmod -R u+w "$out"
    '';
  };

  filteredNarHash = pkgs.runCommand "${name}-filtered-narhash" { nativeBuildInputs = [ fiab ]; } ''
    fiab nar-hash ${filteredTree} > $out
  '';

  filteredDist =
    pkgs.runCommand "${name}-dist-filtered"
      {
        nativeBuildInputs = [
          fiab
          pkgs.python3
        ];
      }
      ''
        mkdir -p "$out"
        cp -a ${filteredTree} flake
        chmod -R u+w flake
        ${packDist "filtered"}
      '';

  # IFD check: the trimmed flake evaluates to the same derivations.
  verifyFiltered =
    let
      narHash = lib.removeSuffix "\n" (builtins.readFile filteredNarHash);
      # readFile realises filteredTree (via filteredNarHash), so the store
      # path can be passed to getFlake without its context.
      escaped = builtins.replaceStrings [ "+" "/" "=" ] [ "%2B" "%2F" "%3D" ] narHash;
      trimmed = builtins.getFlake (
        builtins.unsafeDiscardStringContext "path:${filteredTree}?narHash=${escaped}"
      );
      pairs = concatMap (
        system:
        map (n: {
          inherit system n;
          ok =
            builtins.unsafeDiscardStringContext trimmed.packages.${system}.${n}.drvPath
            == builtins.unsafeDiscardStringContext selected.${system}.${n}.drvPath;
        }) (builtins.attrNames selected.${system})
      ) systems;
      bad = builtins.filter (p: !p.ok) pairs;
    in
    if bad == [ ] then
      pkgs.runCommand "${name}-filtered-verified" { } ''
        echo "${toString (builtins.length pairs)} package(s) match" > $out
      ''
    else
      throw "flake-in-a-box: trimmed flake evaluates differently for: ${
        concatMapStringsSep ", " (p: "${p.system}.${p.n}") bad
      }. Add the files read at evaluation time to extraFiles.";

  dists = lib.listToAttrs (
    map (m: {
      name = m;
      value = if m == "filtered" then filteredDist else libDist m;
    }) modes
  );

  # ---------------------------------------------------------------------
  # Manifest and bundle.

  modeEntry =
    m:
    {
      name = m;
      kind = if m == "filtered" then "passthrough" else "lib";
      lastModified = 1;
      signed = m != "ca-rewrite";
      # Flake nixConfig cannot enable experimental features for evaluation
      # (builtins.fetchClosure is missing either way), so none are set.
      experimentalFeatures = [ ];
      needsFetchClosure = m != "filtered";
      outputs = if m == "filtered" then flakeOutputNames else [ ];
    };

  manifest = pkgs.writeText "${name}-manifest.json" (
    builtins.toJSON {
      version = 1;
      inherit name description;
      defaultMode = builtins.head modes;
      modes = map modeEntry modes;
      caches = map (c: "cache/${c}") (builtins.attrNames caches);
    }
  );

  bundle =
    pkgs.runCommand "${name}-bundle"
      {
        nativeBuildInputs = [ pkgs.python3 ];
        passthru = {
          inherit
            caches
            dists
            caCaches
            iaCacheFlat
            preJson
            ;
          filteredTree = if has "filtered" then filteredTree else null;
          verify = if has "filtered" then verifyFiltered else null;
        };
      }
      ''
        mkdir -p "$out/dist" "$out/cache"
        ${concatMapStringsSep "\n" (c: ''ln -s ${caches.${c}} "$out/cache/${c}"'') (builtins.attrNames caches)}
        python3 - "$out" ${manifest} ${
          concatMapStringsSep " " (m: "${m}=${dists.${m}}") modes
        } ${lib.optionalString (has "filtered") "lock=${src}"} <<'EOF'
        import hashlib, json, os, shutil, sys
        out, manifest = sys.argv[1], json.load(open(sys.argv[2]))
        dists = dict(a.split("=", 1) for a in sys.argv[3:] if not a.startswith("lock="))
        lock_src = [a[5:] for a in sys.argv[3:] if a.startswith("lock=")]
        for m in manifest["modes"]:
            d = dists[m["name"]]
            info = json.load(open(os.path.join(d, "info.json")))
            data = open(os.path.join(d, "flake.tar.gz"), "rb").read()
            h = hashlib.sha256(data).hexdigest()[:20]
            rel = f"dist/{m['name']}-{h}.tar.gz"
            shutil.copyfile(os.path.join(d, "flake.tar.gz"), os.path.join(out, rel))
            m["dist"] = rel
            m["narHash"] = info["narHash"]
            if m["name"] == "filtered" and lock_src:
                lf = os.path.join(lock_src[0], "flake.lock")
                if os.path.exists(lf):
                    m["lock"] = json.load(open(lf))
        json.dump(manifest, open(os.path.join(out, "bundle.json"), "w"), indent=2)
        EOF
      '';
in
assert checked;
bundle
