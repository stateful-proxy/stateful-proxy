package cache

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"zombiezen.com/go/sqlite/sqlitemigration"
)

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

func CachingMiddleware(next http.Handler, logger *slog.Logger, dbPool *sqlitemigration.Pool) http.Handler {
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
		resp, err := reqAccessor.Resp()
		if err != nil {
			http.Error(w, fmt.Errorf("Database problem: %s", err.Error()).Error(), http.StatusInternalServerError)
			return
		}
		if resp != nil {
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
