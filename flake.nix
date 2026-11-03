{
  description = "flake-in-a-box: serve a flake and a pre-built binary cache for it from one host";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    sample = {
      url = "path:./examples/sample";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    {
      self,
      nixpkgs,
      sample,
    }:
    let
      inherit (nixpkgs) lib;
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      linuxSystems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAllSystems = f: lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      overlays.default = final: _prev: {
        fiab = final.callPackage ./nix/fiab.nix { };
        mkBinaryCacheWithCA = final.callPackage ./nix/binary-cache { };
      };

      lib = {
        # mkBundle { pkgs, flake, name?, description?, systems?, includePackage?,
        #            modes?, caVariants?, compression?, src?, extraFiles? }
        mkBundle =
          { pkgs, ... }@args:
          import ./nix/mk-bundle.nix {
            inherit (pkgs) lib;
            inherit pkgs;
            fiab = self.packages.${pkgs.stdenv.hostPlatform.system}.fiab;
            mkBinaryCache = pkgs.callPackage ./nix/binary-cache { };
          } (builtins.removeAttrs args [ "pkgs" ]);

        # A `nix run`-able server with the bundle built in.
        mkServer =
          { pkgs, bundle }:
          pkgs.writeShellApplication {
            name = "fiab-serve-${bundle.name}";
            runtimeInputs = [ ];
            text = ''
              exec ${lib.getExe self.packages.${pkgs.stdenv.hostPlatform.system}.fiab} serve --bundle ${bundle} "$@"
            '';
            meta.description = "Serve the ${bundle.name} flake-in-a-box bundle";
          };

        # An OCI image with the bundle built in. The signing key is supplied at
        # runtime (FIAB_SIGNING_KEY or FIAB_SIGNING_KEY_FILE).
        mkImage =
          {
            pkgs,
            bundle,
            name ? "flake-in-a-box",
            tag ? null,
          }:
          pkgs.dockerTools.buildLayeredImage {
            inherit name tag;
            config = {
              Entrypoint = [
                (lib.getExe self.packages.${pkgs.stdenv.hostPlatform.system}.fiab)
                "serve"
                "--bundle"
                "${bundle}"
              ];
              Env = [ "FIAB_LISTEN=:8080" ];
              ExposedPorts."8080/tcp" = { };
            };
          };
      };

      packages = forAllSystems (
        pkgs:
        let
          fiab = pkgs.callPackage ./nix/fiab.nix { };
        in
        {
          inherit fiab;
          default = fiab;
          mkBinaryCacheWithCA = pkgs.callPackage ./nix/binary-cache { };
        }
        // lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux (
          let
            sampleBundle = self.lib.mkBundle {
              inherit pkgs;
              flake = sample;
              name = "sample";
              description = "flake-in-a-box sample";
              systems = [ pkgs.stdenv.hostPlatform.system ];
              modes = [
                "ca-rewrite"
                "ia-signed"
                "filtered"
              ];
              extraFiles = lib.fileset.unions [
                (lib.fileset.fileFilter (f: f.hasExt "nix") ./examples/sample)
                ./examples/sample/greeting.txt
              ];
            };
          in
          {
            sample-bundle = sampleBundle;
            sample-serve = self.lib.mkServer {
              inherit pkgs;
              bundle = sampleBundle;
            };
            sample-image = self.lib.mkImage {
              inherit pkgs;
              bundle = sampleBundle;
              name = "flake-in-a-box-sample";
            };
          }
        )
      );

      # Inputs for tests/local.sh.
      legacyPackages = lib.genAttrs linuxSystems (system: {
        fiabTests.fixture = import ./tests/fixture.nix {
          pkgs = nixpkgs.legacyPackages.${system};
          mkBinaryCacheWithCA = self.packages.${system}.mkBinaryCacheWithCA;
        };
      });

      apps = forAllSystems (pkgs: {
        default = {
          type = "app";
          program = lib.getExe self.packages.${pkgs.stdenv.hostPlatform.system}.fiab;
        };
      });

      checks = lib.genAttrs linuxSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          p = self.packages.${system};
        in
        {
          inherit (p) fiab sample-bundle;
          sample-filtered-verify = p.sample-bundle.verify;
          vm = import ./tests/vm.nix {
            inherit pkgs self;
            bundle = p.sample-bundle;
            nixpkgsSrc = nixpkgs.outPath;
          };
        }
      );

      formatter = forAllSystems (pkgs: pkgs.nixfmt);
    };
}
