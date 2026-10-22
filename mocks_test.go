package main

import (
	"net/http"
)

type errorResponseWriter struct {
	header     http.Header
	err        error
	statusCode int
}

func (w *errorResponseWriter) Header() http.Header {
	return w.header
}

func (w *errorResponseWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func (w *errorResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
}
