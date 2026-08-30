{
  description = "davkit — WebDAV, CalDAV and CardDAV for Go";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  inputs.flake-utils.url = "github:numtide/flake-utils";

  outputs = {
    nixpkgs,
    flake-utils,
    ...
  }: let
    # The Go toolchain for the devshell and the checks. Bump this one string to
    # move them together; it names a `go_*` attribute in nixpkgs.
    goVersion = "1_27";
  in
    flake-utils.lib.eachDefaultSystem (
      system: let
        pkgs = import nixpkgs {
          inherit system;
          overlays = [
            # nixpkgs still defaults `go` to an older release than this module's
            # go directive. golangci-lint and govulncheck need no override: they
            # already build against the newest Go, which they must, since a Go
            # analyser cannot load a module whose go directive is newer than the
            # toolchain it was compiled with.
            (_final: prev: {go = prev."go_${goVersion}";})
          ];
        };
      in {
        devShells.default = pkgs.mkShell {
          name = "davkit";

          packages = with pkgs; [
            go
            gopls
            delve

            golangci-lint
            golangci-lint-langserver
            govulncheck

            # The RFC 4918 conformance suite. conformance/litmus_test.go skips
            # when this is absent, so it is the difference between that test
            # running and silently doing nothing.
            litmus
          ];

          GOPROXY = "direct";

          # -race requires cgo, and both the suite and .build.yml run with it.
          CGO_ENABLED = 1;

          # GCC here is not built with -oO, so fortify hardening breaks the Go
          # debugger whenever cgo is on.
          # See https://github.com/NixOS/nixpkgs/pull/12895/files
          hardeningDisable = ["fortify"];
        };

        checks = import ./nix/checks.nix {inherit pkgs;};
      }
    );
}
