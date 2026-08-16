# KubeTective Homebrew formula.
#
# Install with the one-liner:
#   brew install gledilami/kubetective/kubetective
# Homebrew auto-taps github.com/GlediLami/homebrew-kubetective, which carries
# a copy of this file. Keep both in sync at release time (CONTRIBUTING.md).
#
# The url/sha256 point at the tagged release tarball (kept in sync by
# hack/update-formula.sh — see CONTRIBUTING.md).

class Kubetective < Formula
  desc "Kubernetes incident investigation engine with explainable, calibrated scoring"
  homepage "https://github.com/GlediLami/kubetective"
  url "https://github.com/GlediLami/kubetective/archive/refs/tags/v1.0.1.tar.gz"
  sha256 "5f8ee9ddc2a1c761f371f8f74b7b1319589315716ab36c0b5ae737966bce43a6"
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
