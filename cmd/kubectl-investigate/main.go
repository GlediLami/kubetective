// kubectl-investigate is the kubectl plugin entry point. Being on PATH under
// this name is what makes `kubectl investigate <target>` work (kubectl plugin
// discovery). It shares the full CLI with kubetective.
package main

import (
	"github.com/GlediLami/kubetective/internal/cli"
)

func main() {
	cli.Execute()
}
