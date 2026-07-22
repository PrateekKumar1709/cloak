# Homebrew formula stub for Cloak (not published as a tap yet).
# After tagging a release (e.g. v1.0.0) and filling sha256:
#   brew install --build-from-source Formula/cloak.rb
# or publish a tap from https://github.com/PrateekKumar1709/cloak
class Cloak < Formula
  desc "Local privacy firewall for cloud AI, powered by Lemonade"
  homepage "https://github.com/PrateekKumar1709/cloak"
  head "https://github.com/PrateekKumar1709/cloak.git", branch: "main"
  license "MIT"

  depends_on "go" => :build

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w -X main.version=HEAD"), "./cmd/cloak"
  end

  test do
    assert_match "cloak", shell_output("#{bin}/cloak version")
  end
end
