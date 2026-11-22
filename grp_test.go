package fasthttpprom

import (
	"testing"

	"github.com/fasthttp/router"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/valyala/fasthttp"
)

func TestPathGroupingBoundsCardinality(t *testing.T) {
	r := router.New()
	r.SaveMatchedRoutePath = true
	r.GET("/items/{id}", func(c *fasthttp.RequestCtx) {})
	p := NewPrometheus("grptest")
	p.Use(r)
	p.SetPathGrouping(true)
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
