package main

import (
	"context"
	"log"

	"github.com/egemengol/goproxy/internal/server"
)

func main() {
	ctx := context.Background()
	if err := server.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
