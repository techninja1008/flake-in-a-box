# Generated-flake runtime shipped inside every "lib" dist flake
# (ca-rewrite, ia-signed). It needs no nixpkgs: packages are fetched with
# builtins.fetchClosure from the binary cache that served the flake and
# wrapped in a builtin:buildenv derivation carrying the original metadata,
# so `nix build`, `nix run` and `nix shell` work.
{ cacheUrl, data }:

let
  inherit (builtins)
    all
    compareVersions
    concatStringsSep
    elem
    filter
    head
    length
    listToAttrs
    mapAttrs
    nixVersion
    ;

  fetchClosure =
    builtins.fetchClosure or (throw ''
      flake-in-a-box: this flake needs the `fetch-closure` experimental feature.
      Enable it in nix.conf (extra-experimental-features = fetch-closure) or pass
      --extra-experimental-features fetch-closure. A flake's nixConfig cannot
      enable it.
    '');

  storePath = parts: builtins.storeDir + "/" + concatStringsSep "" parts;

  isCA = data.mode == "ca-rewrite";

  # Nix 2.35 changed how self-references are hashed in content-addressed
  # paths (NixOS/nix#15931), so CA bundles carry one closure per hashing
  # variant. The evaluating Nix picks its own; note that the daemon (which
  # verifies the paths) may be a different version.
  autoVariant =
    let
      preferred = if compareVersions nixVersion "2.35" >= 0 then "positions" else "legacy";
    in
    if elem preferred data.variants then preferred else head data.variants;

  fetch =
    variant: id:
    let
      p = data.paths.${id};
    in
    if isCA then
      fetchClosure {
        fromStore = cacheUrl;
        fromPath = storePath p.${variant};
      }
    else
      fetchClosure {
        fromStore = cacheUrl;
        fromPath = storePath p.ia;
        inputAddressed = true;
      };

  optionalAttr = name: value: if value == null then { } else { ${name} = value; };

  mkPackage =
    variant: pkg:
    let
      fetched = mapAttrs (_: fetch variant) pkg.paths;
      install = filter (o: fetched ? ${o}) pkg.outputsToInstall;
      installPaths = map (o: fetched.${o}) install;
      env = derivation {
        inherit (pkg) name;
        system = "builtin";
        builder = "builtin:buildenv";
        derivations = concatStringsSep " " (
          [
            "true"
            "5"
            (toString (length installPaths))
          ]
          ++ installPaths
        );
        manifest = builtins.toFile "manifest.nix" "[ ]";
        preferLocalBuild = true;
        allowSubstitutes = false;
      };
    in
    if installPaths == [ ] then
      throw "flake-in-a-box: package ${pkg.name} has no installable outputs"
    else if !(all (o: pkg.dirs.${o} or false) install) then
      # buildenv needs directories; plain files are exposed as store paths.
      head installPaths
    else
      env
      // {
        meta = pkg.meta;
        # The fetched store paths of every output, by output name.
        storePaths = fetched;
        flakeInABox = {
          inherit (data) mode;
          inherit variant;
        };
      }
      // optionalAttr "pname" (pkg.pname or null)
      // optionalAttr "version" (pkg.version or null);

  packagesFor = variant: mapAttrs (_: mapAttrs (_: mkPackage variant)) data.packages;

  variants = if isCA then data.variants else [ "ia" ];
  defaultVariant = if isCA then autoVariant else "ia";
in
{
  packages = packagesFor defaultVariant;

  # Explicit variant selection, e.g. legacyPackages.x86_64-linux.variants.legacy.hello
  legacyPackages = mapAttrs (system: _: {
    variants = listToAttrs (
      map (v: {
        name = v;
        value = (packagesFor v).${system};
      }) variants
    );
  }) data.packages;
}
