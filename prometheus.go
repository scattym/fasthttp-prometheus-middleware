package fasthttpprom

import (
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/fasthttp/router"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpadaptor"
)

var defaultMetricPath = "/metrics"

const (
	metricsReadTimeout  = 10 * time.Second
	metricsWriteTimeout = 15 * time.Second
	metricsIdleTimeout  = 60 * time.Second
	// Bounds scrape concurrency and duration so /metrics cannot be used to exhaust the service.
	metricsMaxInFlight = 10
	metricsTimeout     = 10 * time.Second
)

// ListenerHandler url label
type ListenerHandler func(c *fasthttp.RequestCtx) string

// Prometheus contains the metrics gathered by the instance and its path
type Prometheus struct {
	reqDur        *prometheus.HistogramVec
	reqInProgress prometheus.Gauge
	// app is the application router that is wrapped by the middleware.
	app *router.Router
	// metricsRouter serves /metrics on listenAddress; it is never the app router.
	metricsRouter *router.Router
	listenAddress string
	MetricsPath   string
	Handler       fasthttp.RequestHandler
	groupPath     bool
	registerer    prometheus.Registerer
	gatherer      prometheus.Gatherer
	server        *fasthttp.Server
}

// NewPrometheus generates a new set of metrics with a certain subsystem name using the default registry.
func NewPrometheus(subsystem string) *Prometheus {
	return newPrometheus(subsystem, prometheus.DefaultRegisterer, prometheus.DefaultGatherer)
}

// NewPrometheusWithRegistry is like NewPrometheus but registers and serves metrics from the given registry.
func NewPrometheusWithRegistry(subsystem string, reg *prometheus.Registry) *Prometheus {
	return newPrometheus(subsystem, reg, reg)
}

func newPrometheus(subsystem string, reg prometheus.Registerer, g prometheus.Gatherer) *Prometheus {
	p := &Prometheus{
		MetricsPath: defaultMetricPath,
		// Grouping is on by default: raw paths are client controlled and unbounded.
		groupPath:  true,
		registerer: reg,
		gatherer:   g,
	}
	p.registerMetrics(subsystem)

	return p
}

// SetPathGrouping labels metrics with the matched route pattern instead of the raw request path.
// It is enabled by default; disabling it on an internet-facing service allows unbounded label cardinality.
func (p *Prometheus) SetPathGrouping(enabled bool) {
	p.groupPath = enabled
}

// SetListenAddress for exposing metrics on address. If not set, it will be exposed at the
// same address of api that is being used
func (p *Prometheus) SetListenAddress(address string) {
	p.listenAddress = address
	if p.listenAddress != "" {
		p.metricsRouter = router.New()
	}
}

// SetListenAddressWithRouter for using a separate router to expose metrics. (this keeps things like GET /metrics out of
// your content's access log).
func (p *Prometheus) SetListenAddressWithRouter(listenAddress string, r *router.Router) {
	p.listenAddress = listenAddress
	if len(p.listenAddress) > 0 {
		p.metricsRouter = r
	}
}

// SetMetricsPath registers the metrics handler on r. If a listen address is configured it also starts
// a server for r on that address.
func (p *Prometheus) SetMetricsPath(r *router.Router) {
	r.GET(p.MetricsPath, p.prometheusHandler())
	if p.listenAddress != "" {
		p.runServer(r)
	}
}

func (p *Prometheus) runServer(r *router.Router) {
	srv := &fasthttp.Server{
		Handler:      r.Handler,
		ReadTimeout:  metricsReadTimeout,
		WriteTimeout: metricsWriteTimeout,
		IdleTimeout:  metricsIdleTimeout,
	}
	p.server = srv
	addr := p.listenAddress
	go func() {
		if err := srv.ListenAndServe(addr); err != nil {
			log.Printf("fasthttpprom: metrics server on %s stopped: %v", addr, err)
		}
	}()
}

// Shutdown stops the separate metrics server, if one was started.
func (p *Prometheus) Shutdown() error {
	if p.server == nil {
		return nil
	}
	return p.server.Shutdown()
}

// registerCollector registers c, reusing an identical collector that is already registered.
func (p *Prometheus) registerCollector(c prometheus.Collector) prometheus.Collector {
	if err := p.registerer.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			return are.ExistingCollector
		}
		panic(fmt.Sprintf("fasthttpprom: registering metrics: %v", err))
	}
	return c
}

func (p *Prometheus) registerMetrics(subsystem string) {
	reqDur := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: subsystem,
			Name:      "request_duration_seconds",
			Help:      "request latencies",
			Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.2, 0.3, 0.5, 0.75, 1, 1.5, 2, 3, 5, 10},
		},
		[]string{"code", "path", "method"},
	)

	reqInProgress := prometheus.NewGauge(prometheus.GaugeOpts{
		Subsystem: subsystem,
		Name:      "requests_in_progress",
		Help:      "Total HTTP requests currently in progress",
	})

	p.reqDur = p.registerCollector(reqDur).(*prometheus.HistogramVec)
	p.reqInProgress = p.registerCollector(reqInProgress).(prometheus.Gauge)
}

// Custom adds the middleware to a fasthttp. If a listen address is set, /metrics is served only
// from that address and never from the application router.
func (p *Prometheus) Custom(r *router.Router) {
	p.app = r
	if p.listenAddress != "" {
		if p.metricsRouter == nil {
			p.metricsRouter = router.New()
		}
		p.SetMetricsPath(p.metricsRouter)
	} else {
		p.SetMetricsPath(r)
	}
	p.Handler = p.HandlerFunc()
}

// Use adds the middleware to a fasthttp
func (p *Prometheus) Use(r *router.Router) {
	p.app = r
	r.GET(p.MetricsPath, p.prometheusHandler())
	p.Handler = p.HandlerFunc()
}

// HandlerFunc is onion or wraper to handler for fasthttp listenandserve
func (p *Prometheus) HandlerFunc() fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		if string(ctx.Request.URI().Path()) == p.MetricsPath {
			p.app.Handler(ctx)
			return
		}
		p.reqInProgress.Inc()
		start := time.Now()
		done := false
		defer func() {
			if done {
				return
			}
			// The handler panicked: keep the gauge balanced and record the request as a 500.
			p.observe(ctx, "500", start)
		}()
		p.app.Handler(ctx)
		done = true
		p.observe(ctx, strconv.Itoa(ctx.Response.StatusCode()), start)
	}
}

func (p *Prometheus) observe(ctx *fasthttp.RequestCtx, status string, start time.Time) {
	elapsed := time.Since(start).Seconds()
	uri := string(ctx.Request.URI().Path())
	if p.groupPath {
		// Unmatched requests (404/405, crawlers) share one label to keep cardinality bounded.
		uri = unmatchedPath
		if matched, ok := ctx.UserValue(router.MatchedRoutePathParam).(string); ok && matched != "" {
			uri = matched
		}
	}
	p.reqDur.WithLabelValues(status, uri, normaliseMethod(ctx.Method())).Observe(elapsed)
	p.reqInProgress.Dec()
}

const unmatchedPath = "unmatched"

// normaliseMethod bounds the method label; clients can send arbitrary method tokens.
func normaliseMethod(m []byte) string {
	switch string(m) {
	case fasthttp.MethodGet, fasthttp.MethodHead, fasthttp.MethodPost, fasthttp.MethodPut,
		fasthttp.MethodPatch, fasthttp.MethodDelete, fasthttp.MethodOptions:
		return string(m)
	}
	return "other"
}

// since prometheus/client_golang use net/http we need this net/http adapter for fasthttp
func (p *Prometheus) prometheusHandler() fasthttp.RequestHandler {
	return fasthttpadaptor.NewFastHTTPHandler(promhttp.InstrumentMetricHandler(
		p.registerer,
		promhttp.HandlerFor(p.gatherer, promhttp.HandlerOpts{
			ErrorHandling:       promhttp.ContinueOnError,
			MaxRequestsInFlight: metricsMaxInFlight,
			Timeout:             metricsTimeout,
		}),
	))
}
