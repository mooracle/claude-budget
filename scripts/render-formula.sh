#!/bin/sh
# Render the Homebrew formula for claude-budget from built release binaries.
#
#   render-formula.sh <version> [dist-dir]
#
# <version>  release version, with or without a leading "v" (e.g. v0.1.0 or 0.1.0)
# dist-dir   directory holding the per-platform binaries, named
#            claude-budget-<os>-<arch> (defaults to ./dist)
#
# Prints the formula to stdout. The release workflow pipes it into the tap repo's
# Formula/claude-budget.rb on every v* tag; it can also be run by hand.
set -eu

ver="${1:?usage: render-formula.sh <version> [dist-dir]}"
ver="${ver#v}" # tolerate a leading "v"
dist="${2:-dist}"

# sha256 of a named release binary, portable across sha256sum (Linux) and
# shasum (macOS).
sha() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$dist/claude-budget-$1" | cut -d' ' -f1
  else
    shasum -a 256 "$dist/claude-budget-$1" | cut -d' ' -f1
  fi
}

base="https://github.com/mooracle/claude-budget/releases/download/v${ver}"

cat <<EOF
class ClaudeBudget < Formula
  desc "Per-commit Claude Code token-cost git trailers"
  homepage "https://github.com/mooracle/claude-budget"
  version "${ver}"
  license "MIT"

  on_macos do
    on_arm do
      url "${base}/claude-budget-darwin-arm64"
      sha256 "$(sha darwin-arm64)"
    end
    on_intel do
      url "${base}/claude-budget-darwin-amd64"
      sha256 "$(sha darwin-amd64)"
    end
  end

  on_linux do
    on_arm do
      url "${base}/claude-budget-linux-arm64"
      sha256 "$(sha linux-arm64)"
    end
    on_intel do
      url "${base}/claude-budget-linux-amd64"
      sha256 "$(sha linux-amd64)"
    end
  end

  def install
    # Each release asset is a bare binary named claude-budget-<os>-<arch>;
    # install whichever one was downloaded for this platform as \`claude-budget\`.
    bin.install Dir["claude-budget-*"].first => "claude-budget"
  end

  test do
    assert_match "${ver}", shell_output("#{bin}/claude-budget version")
  end
end
EOF
