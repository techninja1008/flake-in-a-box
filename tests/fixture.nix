# The fixture from the design brief: three content-addressed paths and one
# input-addressed path referring to all of them, cached by stock and by
# patched mkBinaryCache.
{ pkgs, mkBinaryCacheWithCA }:

let
  src = builtins.path {
    name = "ca-src";
    path = ./fixture-src;
  };
  txt = builtins.toFile "ca-text" "content-addressed text\n";
  fod = pkgs.runCommand "ca-fod" {
    outputHashMode = "flat";
    outputHashAlgo = "sha256";
    outputHash = builtins.hashString "sha256" "fixed output\n";
  } "echo 'fixed output' > $out";
  ia = pkgs.runCommand "ia-thing" { } ''
    mkdir $out
    ln -s ${src} $out/src
    ln -s ${txt} $out/txt
    ln -s ${fod} $out/fod
  '';
in
{
  inherit src txt fod ia;
  stock = pkgs.mkBinaryCache {
    rootPaths = [ ia ];
    compression = "none";
  };
  patched = mkBinaryCacheWithCA {
    rootPaths = [ ia ];
    compression = "none";
  };
}
