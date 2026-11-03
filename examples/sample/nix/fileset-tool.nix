{ lib, stdenvNoCC }:

stdenvNoCC.mkDerivation {
  pname = "fileset-tool";
  version = "0.1";
  src = lib.fileset.toSource {
    root = ../.;
    fileset = ../src;
  };
  installPhase = ''
    install -Dm755 src/fileset-tool.sh $out/bin/fileset-tool
    install -Dm644 src/data.txt $out/share/fileset-tool/data.txt
    substituteInPlace $out/bin/fileset-tool --replace-fail @data@ $out/share/fileset-tool/data.txt
  '';
  meta = {
    description = "Prints a data file shipped from a lib.fileset source";
    mainProgram = "fileset-tool";
    license = lib.licenses.mit;
  };
}
