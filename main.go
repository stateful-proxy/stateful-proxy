package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/url"
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
