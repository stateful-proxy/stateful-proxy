package main

import (
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

	// Create a new HTTP request based on the original one
	proxyReq, err := http.NewRequest(req.Method, url.String(), req.Body)
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

	// Copy the response body
	io.Copy(res, resp.Body)
}

func main() {
	http.HandleFunc("/", handleRequestAndRedirect)
	http.HandleFunc("/healthcheck", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	log.Println("Starting proxy server on :8000")
	if err := http.ListenAndServe(":8000", nil); err != nil {
		log.Fatal(err)
	}
}
