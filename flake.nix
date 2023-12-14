{
  description = "Chatwoot <-> Matrix Help Bot Integration";

  inputs = {
    nixpkgs-unstable.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs-unstable, flake-utils }:
    (flake-utils.lib.eachDefaultSystem (system:
      let pkgs = import nixpkgs-unstable { system = system; };
      in rec {
        packages.chatwoot = pkgs.buildGoModule {
          pname = "chatwoot";
          version = "unstable-2023-12-14";

          src = ./.;

          propagatedBuildInputs = [ pkgs.olm ];

          vendorHash = "sha256-MYmJf8UlR6IhF5SVEQ9nap1vWFH00TjFjKrmISInXMg=";
        };
        packages.default = packages.chatwoot;

        devShells.default = pkgs.mkShell {
          packages = with pkgs; [ go_1_21 olm pre-commit gotools gopls ];
        };
      }));
}
