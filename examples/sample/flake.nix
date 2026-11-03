{
  description = "flake-in-a-box sample flake";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAllSystems (pkgs: rec {
        # Reads ./greeting.txt at evaluation time: the file never appears in a
        # derivation, so the filtered mode needs it listed in extraFiles.
        greet = pkgs.callPackage ./nix/greet.nix {
          greeting = nixpkgs.lib.removeSuffix "\n" (builtins.readFile ./greeting.txt);
        };
        # Built from a lib.fileset source: only ./src is copied to the store.
        fileset-tool = pkgs.callPackage ./nix/fileset-tool.nix { };
        # A plain nixpkgs package re-exported (its closure is in cache.nixos.org).
        inherit (pkgs) hello;
        default = greet;
      });
    };
}
