{
  lib,
  writeShellScriptBin,
  hello,
  greeting,
}:

(writeShellScriptBin "greet" ''
  echo ${lib.escapeShellArg greeting}
  exec ${lib.getExe hello} --greeting="$*"
'').overrideAttrs
  {
    pname = "greet";
    version = "1.0";
    meta = {
      description = "Greets you, via GNU hello";
      mainProgram = "greet";
      license = lib.licenses.mit;
    };
  }
