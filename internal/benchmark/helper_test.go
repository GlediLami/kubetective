package benchmark

import (
	"github.com/GlediLami/kubetective/internal/analyze"
	"github.com/GlediLami/kubetective/internal/analyze/configregression"
	"github.com/GlediLami/kubetective/internal/analyze/crashloop"
	"github.com/GlediLami/kubetective/internal/analyze/dns"
	"github.com/GlediLami/kubetective/internal/analyze/hpa"
	"github.com/GlediLami/kubetective/internal/analyze/imagepull"
	"github.com/GlediLami/kubetective/internal/analyze/nodepressure"
	"github.com/GlediLami/kubetective/internal/analyze/oom"
	"github.com/GlediLami/kubetective/internal/analyze/probe"
	"github.com/GlediLami/kubetective/internal/analyze/pvc"
	"github.com/GlediLami/kubetective/internal/analyze/scheduling"
	"github.com/GlediLami/kubetective/internal/analyze/service"
	"github.com/GlediLami/kubetective/internal/collect"
	"github.com/GlediLami/kubetective/internal/engine"
	"github.com/GlediLami/kubetective/pkg/api"
)

// scenariosPath is the suite as seen from this package's directory.
const scenariosPath = "../../scenarios"

// contractFactory wires the full analyzer set, mirroring cli.newEngine. Tests
// that assert on real engine output use it so they exercise the shipped
// analyzer registry rather than a convenient subset.
func contractFactory(cs ...collect.Collector) api.Investigator {
	creg := collect.NewRegistry()
	for _, c := range cs {
		creg.Register(c)
	}
	areg := analyze.NewRegistry()
	for _, a := range []analyze.Analyzer{
		oom.New(), crashloop.New(), imagepull.New(), scheduling.New(),
		probe.New(), service.New(), nodepressure.New(), configregression.New(),
		dns.New(), pvc.New(), hpa.New(),
	} {
		areg.Register(a)
	}
	return engine.New(creg, areg)
}
