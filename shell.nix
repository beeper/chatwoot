{ forCI ? false }: let
  pkgs = import <nixpkgs> {};
in
  with pkgs;
  mkShell {
    buildInputs = [
      go
      olm
    ] ++ lib.lists.optional (!forCI) [
      goimports
      gopls
      vgo2nix
    ];
  }
