{
  description = "Chatwoot <-> Matrix Help Bot Integration";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    (flake-utils.lib.eachDefaultSystem (system:
      let pkgs = import nixpkgs { inherit system; };
      in rec {
        packages.chatwoot = pkgs.buildGoModule {
          pname = "chatwoot";
          version = "unstable-2024-03-26";
          src = self;
          propagatedBuildInputs = [ pkgs.olm ];
          vendorHash = "sha256-BIymQIs7vYO4JvRrj4cg8VlgsJ+pK/TRftwbYXKJD88=";
        };
        packages.default = packages.chatwoot;

        devShells.default = pkgs.mkShell {
          packages = with pkgs; [ go olm pre-commit gotools gopls ];
        };
      }));
}
