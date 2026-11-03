{ lib, buildGoModule }:

buildGoModule {
  pname = "fiab";
  version = "0.1.0";
  src = lib.fileset.toSource {
    root = ../fiab;
    fileset = lib.fileset.fileFilter (f: f.hasExt "go" || f.name == "go.mod") ../fiab;
  };
  vendorHash = null;
  env.CGO_ENABLED = 0;
  ldflags = [ "-s" "-w" ];
  meta = {
    description = "flake-in-a-box: CA closure rewriter, cache packer and bundle server";
    mainProgram = "fiab";
    license = lib.licenses.mit;
  };
}
