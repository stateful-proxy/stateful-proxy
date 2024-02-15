package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"time"

	"zombiezen.com/go/sqlite/sqlitemigration"
)

func handleRequestAndRedirect(res http.ResponseWriter, req *http.Request) {
	// Parse the destination URL
	url, err := url.Parse(req.URL.String())
	if err != nil {
		log.Fatal(err) // Handle error appropriately in production code
	}

	var reqBodyAcc bytes.Buffer
	reqBodyReader := io.TeeReader(req.Body, &reqBodyAcc)

	// Create a new HTTP request based on the original one, using the original body directly
	proxyReq, err := http.NewRequest(req.Method, url.String(), reqBodyReader)
	if err != nil {
		log.Fatal(err) // Handle error appropriately
	}

	// Copy the original headers
	proxyReq.Header = make(http.Header)
	for h, val := range req.Header {
		proxyReq.Header[h] = val
	}

	// Forward the request to the destination
	client := &http.Client{}
	resp, err := client.Do(proxyReq)
	if err != nil {
		http.Error(res, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy the response headers and status code
	for h, val := range resp.Header {
		res.Header()[h] = val
	}
	res.WriteHeader(resp.StatusCode)

	var respBodyAcc bytes.Buffer
	respBodyReader := io.TeeReader(resp.Body, &respBodyAcc)

	// Stream the response body directly to the client
	io.Copy(res, respBodyReader)

	// Log the request and response
	var reqHeaders bytes.Buffer
	req.Header.Write(&reqHeaders)
	log.Printf("Request: %s %s %s %s\n", &reqHeaders, req.Method, req.URL, reqBodyAcc.String())
	log.Printf("Response: %d %s %s\n", resp.StatusCode, resp.Status, respBodyAcc.String())
}

type App struct {
	db *DB
}

func NewApp(database_uri string) *App {
	db := NewDB(database_uri)
	return &App{db: db}
}

func (app *App) Close() error {
	return app.db.Close()
}

//////////////////////////

func handleProxy(logger *slog.Logger, client *http.Client) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		url, err := url.Parse(req.URL.String())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		proxyiedReq, err := http.NewRequest(req.Method, url.String(), req.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		proxyiedReq.Header = req.Header.Clone()
		// TODO set proper headers for the upstream request
		// proxyiedReq.Header.Set("X-Forwarded-For", r.RemoteAddr)

		resp, err := client.Do(proxyiedReq)
		if err != nil {
			// TODO what to do now?
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		for h, val := range resp.Header {
			w.Header()[h] = val
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})
}

func wrapRequestBody(body io.ReadCloser) (io.ReadCloser, bytes.Buffer) {
	var buf bytes.Buffer
	newBody := io.NopCloser(io.TeeReader(body, &buf))
	return newBody, buf
}

type WrappedResponseWriter struct {
	http.ResponseWriter
	Headers http.Header
	Status  int
	Body    bytes.Buffer
}

func (w *WrappedResponseWriter) WriteHeader(status int) {
	w.Status = status
	w.Headers = w.ResponseWriter.Header().Clone()
	w.ResponseWriter.WriteHeader(status)
}

func (w *WrappedResponseWriter) Write(data []byte) (int, error) {
	w.Body.Write(data)
	return w.ResponseWriter.Write(data)
}

func cachingMiddleware(next http.Handler, logger *slog.Logger, dbPool *sqlitemigration.Pool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var port *string
		if req.URL.Port() != "" {
			portStr := req.URL.Port()
			port = &portStr
		}
		request := &Req{
			Scheme:  req.URL.Scheme,
			Host:    req.URL.Hostname(),
			Path:    req.URL.Path,
			Method:  req.Method,
			Port:    port,
			Headers: req.Header,
		}
		conn, err := dbPool.Get(req.Context())
		if err != nil {
			http.Error(w, fmt.Errorf("Database problem: %s", err.Error()).Error(), http.StatusInternalServerError)
			return
		}
		defer dbPool.Put(conn)
		reqAccessor, err := NewReqAccessor(conn, request)
		if err != nil {
			http.Error(w, fmt.Errorf("Database problem: %s", err.Error()).Error(), http.StatusInternalServerError)
			return
		}
		if resp := reqAccessor.Resp(); resp != nil {
			logger.Info("cache hit")
			// may block if another request is currently fetching the response
			// but that's fine
			// TODO return the response
			for h, val := range resp.Headers {
				w.Header()[h] = val
			}
			w.WriteHeader(int(resp.Status))
			io.Copy(w, bytes.NewReader(resp.Body))
		} else {
			// forward request to the upstream server
			// while wrapping the response writer to capture the response
			// and cache it
			newBody, bodyAcc := wrapRequestBody(req.Body)
			req.Body = newBody

			wrapped := &WrappedResponseWriter{ResponseWriter: w}
			next.ServeHTTP(wrapped, req)

			reqAccessor.SetReqBody(conn, bodyAcc.Bytes())
			reqAccessor.SetResp(conn, &Resp{
				Status:  int32(wrapped.Status),
				Headers: wrapped.Headers,
				Body:    wrapped.Body.Bytes(),
			})
		}
	})
}

func NewServer(
	logger *slog.Logger, db *DB,
) http.Handler {
	client := &http.Client{}
	mux := http.NewServeMux()
	mux.Handle("/", cachingMiddleware(handleProxy(logger, client), logger, db.pool))
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

func run(ctx context.Context) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	config := Config{
		DatabaseUri: "db.sqlite",
		Host:        "127.0.0.1",
		Port:        "5000",
	}
	db := NewDB(config.DatabaseUri)
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

func main() {
	ctx := context.Background()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}

/*
func main() {
	app := NewApp("db.sqlite")
	defer app.Close()

	conn, err := app.db.pool.Get(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	defer app.db.pool.Put(conn)

	ra, err := NewReqAccessor(conn, &Req{
		Scheme: "http",
		Host:   "example.com",
		Port:   80,
		Path:   "/",
		Method: "GET",
		Headers: http.Header{
			"User-Agent": []string{"go-http-client/1.1"},
		},
		Body: nil,
	})

	log.Printf("%v, %d\n", ra.reqID, ra.State())
	log.Printf("Resp: %v\n", ra.resp)
	return

	id, err := app.db.GetReqId(context.Background(), "a", "a", "a", "a", "a")
	if err != nil {
		log.Println(err)
	}
	log.Printf("%v\n", id)

	id, err = app.db.GetReqId(context.Background(), "b", "a", "a", "a", "a")
	if err != nil {
		log.Println(err)
	}
	log.Printf("%v\n", id)

	http.HandleFunc("/", handleRequestAndRedirect)
	http.HandleFunc("/healthcheck", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	log.Println("Starting proxy server on :8000")
	if err := http.ListenAndServe(":8000", nil); err != nil {
		log.Fatal(err)
	}
}
*/
