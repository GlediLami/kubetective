# KubeDoctor Homebrew formula.
#
# Published via the main repo as a tap:
#   brew tap kubedoctor/kubedoctor https://github.com/kubedoctor/kubedoctor.git
#   brew install kubedoctor/kubedoctor/kubedoctor
#
# The url/sha256 below are placeholders until the first tagged release.
# After tagging, run: hack/update-formula.sh <tag>  (see CONTRIBUTING.md).

class Kubedoctor < Formula
  desc "Kubernetes incident investigation engine with explainable, calibrated scoring"
  homepage "https://github.com/kubedoctor/kubedoctor"
  url "https://github.com/kubedoctor/kubedoctor/archive/refs/tags/v0.7.0.tar.gz"
  sha256 "REPLACE_WITH_RELEASE_SHA256"
  license "Apache-2.0"

  depends_on "go" => :build

  def install
    system "go", "build", "-trimpath", "-ldflags", "-s -w", "-o", "kubedoctor", "./cmd/kubedoctor"
    system "go", "build", "-trimpath", "-ldflags", "-s -w", "-o", "kubectl-investigate", "./cmd/kubectl-investigate"
    bin.install "kubedoctor"
    bin.install "kubectl-investigate"
  end

  test do
    assert_match "kubedoctor", shell_output("#{bin}/kubedoctor --help")
    assert_match "investigate", shell_output("#{bin}/kubectl-investigate --help")
  end
end
