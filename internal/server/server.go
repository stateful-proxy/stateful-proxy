package server

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/egemengol/goproxy/internal/cache"
	"github.com/egemengol/goproxy/internal/proxy"
)

func NewServer(
	logger *slog.Logger, db *cache.DB,
) http.Handler {
	client := &http.Client{}
	mux := http.NewServeMux()
	mux.Handle("/", cache.CachingMiddleware(proxy.HandleProxy(logger, client), logger, db.Pool))
	mux.HandleFunc("GET /healthcheck", func(res http.ResponseWriter, req *http.Request) {
		res.WriteHeader(http.StatusOK)
	})
	return mux
}

type Config struct {
	DatabaseUri string
	Host        string
	Port        string
}

func Run(ctx context.Context) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	config := Config{
		DatabaseUri: "db.sqlite",
		Host:        "127.0.0.1",
		Port:        "5000",
	}
	db := cache.NewDB(config.DatabaseUri)
	defer db.Close()
	srv := NewServer(
		logger, db,
	)

	httpServer := &http.Server{
		Addr:    net.JoinHostPort(config.Host, config.Port),
		Handler: srv,
	}
	go func() {
		log.Printf("listening on %s\n", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "error listening and serving: %s\n", err)
		}
	}()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		// make a new context for the Shutdown (thanks Alessandro Rosetti)
		shutdownCtx := context.Background()
		shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "error shutting down http server: %s\n", err)
		}
	}()
	wg.Wait()
	return nil
}
