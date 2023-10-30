{
  description = "Chatwoot <-> Matrix Help Bot Integration";

  inputs = {
    nixpkgs-unstable.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs-unstable, flake-utils }:
    (flake-utils.lib.eachDefaultSystem
      (system:
        let
          pkgs = import nixpkgs-unstable { system = system; };
        in
        {
          packages.chatwoot = pkgs.buildGoModule rec {
            pname = "chatwoot";
            version = "unstable-2023-05-20";

            src = ./.;

            propagatedBuildInputs = [ pkgs.olm ];

            vendorSha256 = "sha256-whKiWndBv4TX/H19sINU75XK+PmdMomNkZpY84rDAbk=";
          };
          devShells.default = pkgs.mkShell {
            packages = with pkgs; [
              go_1_21
              olm
              pre-commit
              gotools
              gopls
            ];
          };
        }
      ));
}
