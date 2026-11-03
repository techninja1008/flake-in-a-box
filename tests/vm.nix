# NixOS VM test: a server node runs `fiab serve` with the sample bundle, and
# a client node with a multi-user Nix daemon consumes it under two
# hostnames, as an untrusted user and as a trusted one.
{
  pkgs,
  self,
  bundle,
  nixpkgsSrc,
}:

let
  inherit (pkgs) lib;
  system = pkgs.stdenv.hostPlatform.system;
  fiab = self.packages.${system}.fiab;

  # A throwaway key for the test only (it ends up in the store).
  keys = pkgs.runCommand "fiab-vm-test-keys" { nativeBuildInputs = [ pkgs.nix ]; } ''
    mkdir $out
    export NIX_STATE_DIR=$TMPDIR/state NIX_CONF_DIR=$TMPDIR
    nix --extra-experimental-features nix-command key generate-secret --key-name fiab-vm-test-1 > $out/secret
    nix --extra-experimental-features nix-command key convert-secret-to-public < $out/secret > $out/public
  '';

  greeting = "Hello from the flake-in-a-box sample!";
in
pkgs.testers.runNixOSTest {
  name = "flake-in-a-box";

  nodes = {
    server = {
      networking.firewall.allowedTCPPorts = [ 80 ];
      systemd.services.fiab = {
        wantedBy = [ "multi-user.target" ];
        after = [ "network.target" ];
        serviceConfig = {
          ExecStart = "${lib.getExe fiab} serve --bundle ${bundle} --listen :80";
          Environment = [ "FIAB_SIGNING_KEY_FILE=${keys}/secret" ];
          DynamicUser = true;
          AmbientCapabilities = [ "CAP_NET_BIND_SERVICE" ];
        };
      };
    };

    client =
      { nodes, ... }:
      {
        virtualisation = {
          memorySize = 2048;
          diskSize = 4096;
          # The filtered dist evaluates nixpkgs from its lock file; there is no
          # network in the test, so the source is provided up front.
          additionalPaths = [ nixpkgsSrc ];
          # Hashing that source (~50k files) over the shared virtiofs store
          # exhausts virtiofsd's file descriptors; use a store image instead.
          useNixStoreImage = true;
          writableStore = true;
        };
        networking.extraHosts = ''
          ${nodes.server.networking.primaryIPAddress} cache-a.test cache-b.test
        '';
        nix.settings = {
          experimental-features = [
            "nix-command"
            "flakes"
            "fetch-closure"
          ];
          substituters = lib.mkForce [ ];
          trusted-public-keys = lib.mkForce [ ];
          trusted-users = lib.mkForce [ "root" ];
          narinfo-cache-negative-ttl = 0;
          tarball-ttl = 0;
        };
        users.users.alice = {
          isNormalUser = true;
        };
      };
  };

  testScript = ''
    start_all()
    server.wait_for_unit("fiab.service")
    server.wait_for_open_port(80)
    client.wait_for_unit("multi-user.target")

    public_key = server.succeed("cat ${keys}/public").strip()

    def alice(cmd):
        return f"su - alice -c '{cmd}'"

    with subtest("wrapper flakes are templated per host"):
        for host in ["cache-a.test", "cache-b.test"]:
            out = client.succeed(f"curl -fsS http://{host}/flake.tar.gz | tar xzO flake/flake.nix")
            assert f'"http://{host}/cache"' in out, out
            assert public_key in out, out

    with subtest("untrusted user: ca-rewrite needs no trusted key"):
        out = client.succeed(alice(
            "nix run --accept-flake-config http://cache-a.test/ca-rewrite.tar.gz#greet -- untrusted"
        ))
        assert "${greeting}" in out, out
        client.succeed(alice(
            "nix shell --accept-flake-config http://cache-b.test/flake.tar.gz#fileset-tool -c fileset-tool"
            " | grep -q \"data from the fileset source\""
        ))

    with subtest("untrusted user: the daemon ignores the flake's trusted key"):
        client.fail(alice(
            "nix build --no-link --accept-flake-config http://cache-a.test/ia-signed.tar.gz#hello"
        ))

    with subtest("trusted user: ia-signed and filtered substitute everything"):
        for host in ["cache-a.test", "cache-b.test"]:
            out = client.succeed(
                f"nix run --accept-flake-config http://{host}/ia-signed.tar.gz#greet -- trusted"
            )
            assert "${greeting}" in out, out
        dry = client.succeed(
            "nix build --dry-run --accept-flake-config http://cache-b.test/filtered.tar.gz#greet 2>&1"
        )
        assert "will be built" not in dry, dry
        out = client.succeed(
            "nix run --accept-flake-config http://cache-b.test/filtered.tar.gz#greet -- filtered"
        )
        assert "${greeting}" in out, out

    with subtest("index page describes the modes"):
        out = client.succeed("curl -fsS http://cache-a.test/")
        assert "needs the public key above trusted" in out, out
  '';
}
