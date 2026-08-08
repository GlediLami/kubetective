# KubeTective Homebrew formula.
#
# Published via the main repo as a tap:
#   brew tap gledilami/kubetective https://github.com/GlediLami/kubetective.git
#   brew install gledilami/kubetective/kubetective
#
# The url/sha256 below are placeholders until the first tagged release.
# After tagging, run: hack/update-formula.sh <tag>  (see CONTRIBUTING.md).

class Kubetective < Formula
  desc "Kubernetes incident investigation engine with explainable, calibrated scoring"
  homepage "https://github.com/GlediLami/kubetective"
  url "https://github.com/GlediLami/kubetective/archive/refs/tags/v0.7.0.tar.gz"
  sha256 "6fcfc9734781f25f86f1cf204829876066fa5311b2e8ffe1a306bfbc7769d90c"
  license "Apache-2.0"

  depends_on "go" => :build

  def install
    system "go", "build", "-trimpath", "-ldflags", "-s -w", "-o", "kubetective", "./cmd/kubetective"
    system "go", "build", "-trimpath", "-ldflags", "-s -w", "-o", "kubectl-investigate", "./cmd/kubectl-investigate"
    bin.install "kubetective"
    bin.install "kubectl-investigate"
  end

  test do
    assert_match "kubetective", shell_output("#{bin}/kubetective --help")
    assert_match "investigate", shell_output("#{bin}/kubectl-investigate --help")
  end
end
