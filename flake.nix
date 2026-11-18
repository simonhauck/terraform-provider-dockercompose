{
  description = "terraform-provider-dockercompose development shell";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs =
    { self, nixpkgs }:
    let
      supportedSystems = [
        "x86_64-linux"
        "aarch64-linux"
        "aarch64-darwin"
      ];

      forEachSystem = f: nixpkgs.lib.genAttrs supportedSystems (system: f system);
    in
    {
      devShells = forEachSystem (
        system:
        let
          pkgs = import nixpkgs {
            inherit system;
            config.allowUnfree = true;
          };
        in
        {
          default = pkgs.mkShell {
            packages = with pkgs; [
              go
              gopls
              golangci-lint
              goreleaser
              gnumake
              terraform
              git
              gh
              jq
              actionlint
              nixfmt
            ];
            shellHook = ''
              export GOPATH="''${GOPATH:-$HOME/go}"
              export PATH="$GOPATH/bin:$PATH"
            '';
          };
        }
      );

      formatter = forEachSystem (system: (import nixpkgs { inherit system; }).nixfmt);
    };
}
