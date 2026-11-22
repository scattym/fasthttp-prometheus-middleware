package main

import (
	"fmt"
	"log"

	"github.com/fasthttp/router"
	fasthttpprom "github.com/scattym/fasthttp-prometheus-middleware"
	"github.com/valyala/fasthttp"
)

func handleValue(ctx *fasthttp.RequestCtx) {
	id := fmt.Sprintf("%v", ctx.UserValue("id"))
	ctx.SetStatusCode(200)
	m := map[string]interface{}{
		"id": id,
	}
	byteKey := []byte(fmt.Sprintf("%v", m))
	ctx.SetBody(byteKey)
	log.Println(string(ctx.Request.URI().Path()))
}

func main() {

	r := router.New()
	p := fasthttpprom.NewPrometheus("")
	p.Use(r)

	r.GET("/health", func(ctx *fasthttp.RequestCtx) {
		ctx.SetStatusCode(200)
		ctx.SetBody([]byte(`{"status": "pass"}`))
		log.Println(string(ctx.Request.URI().Path()))
	})

	r.GET("/values/{id}", handleValue)

	log.Println("main is listening on ", "8080")
	log.Fatal(fasthttp.ListenAndServe(":"+"8080", p.Handler))

}
