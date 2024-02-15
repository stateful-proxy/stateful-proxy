package proxy

import (
	"io"
	"log/slog"
	"net/http"
	"net/url"
)

func HandleProxy(logger *slog.Logger, client *http.Client) http.Handler {
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
