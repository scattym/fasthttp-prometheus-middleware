package fasthttpprom

import (
	"net"
	"testing"

	"github.com/fasthttp/router"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
)

func TestPathGroupingBoundsCardinality(t *testing.T) {
	r := router.New()
	r.SaveMatchedRoutePath = true
	r.GET("/items/{id}", func(c *fasthttp.RequestCtx) {})
	p := NewPrometheusWithRegistry("grptest", prometheus.NewRegistry())
	p.Use(r)
	for _, path := range []string{"/items/1", "/items/2", "/a", "/b", "/c"} {
		for _, m := range []string{"GET", "FOO", "BAR"} {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI(path)
			ctx.Request.Header.SetMethod(m)
			p.Handler(ctx)
		}
	}
	// items GET(200) + items other(405) + unmatched other(404) + unmatched GET(404)
	if n := testutil.CollectAndCount(p.reqDur); n != 4 {
		t.Fatalf("expected 4 series, got %d", n)
	}
}

func TestPanicKeepsGaugeBalanced(t *testing.T) {
	r := router.New()
	r.GET("/boom", func(c *fasthttp.RequestCtx) { panic("boom") })
	p := NewPrometheusWithRegistry("panictest", prometheus.NewRegistry())
	p.Use(r)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/boom")
	func() {
		defer func() { _ = recover() }()
		p.Handler(ctx)
	}()
	if v := testutil.ToFloat64(p.reqInProgress); v != 0 {
		t.Fatalf("gauge = %v, want 0", v)
	}
	if n := testutil.CollectAndCount(p.reqDur); n != 1 {
		t.Fatalf("expected 1 series, got %d", n)
	}
}

func TestMetricsRequestDoesNotTouchGauge(t *testing.T) {
	r := router.New()
	p := NewPrometheusWithRegistry("mtest", prometheus.NewRegistry())
	p.Use(r)

	ln := fasthttputil.NewInmemoryListener()
	srv := &fasthttp.Server{Handler: p.Handler}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Shutdown() }()
	c := &fasthttp.Client{Dial: func(string) (net.Conn, error) { return ln.Dial() }}

	code, _, err := c.Get(nil, "http://x/metrics")
	if err != nil || code != 200 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if v := testutil.ToFloat64(p.reqInProgress); v != 0 {
		t.Fatalf("gauge = %v, want 0", v)
	}
}

func TestCustomServesMetricsOnlyOnMetricsRouter(t *testing.T) {
	app := router.New()
	app.GET("/secret", func(c *fasthttp.RequestCtx) { c.SetBodyString("secret") })
	p := NewPrometheusWithRegistry("ctest", prometheus.NewRegistry())
	p.SetListenAddress("127.0.0.1:0")
	p.Custom(app)
	defer func() { _ = p.Shutdown() }()

	// Metrics must not be on the app router, and the app must not be on the metrics router.
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/metrics")
	app.Handler(ctx)
	if ctx.Response.StatusCode() != 404 {
		t.Fatalf("app router served /metrics: %d", ctx.Response.StatusCode())
	}
	ctx = &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/secret")
	p.metricsRouter.Handler(ctx)
	if ctx.Response.StatusCode() != 404 {
		t.Fatalf("metrics router served app route: %d", ctx.Response.StatusCode())
	}
}

func TestDuplicateRegistrationReusesCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	a := NewPrometheusWithRegistry("dup", reg)
	b := NewPrometheusWithRegistry("dup", reg)
	if a.reqDur != b.reqDur {
		t.Fatal("expected shared collector")
	}
}
