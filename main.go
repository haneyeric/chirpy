package main

import (
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
)

type apiConfig struct {
	fileserverhits atomic.Int32
}

func (cfg *apiConfig) middlewareMetricsInc(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg.fileserverhits.Add(1)
		next.ServeHTTP(w, r)
	})
}
func main() {
	const port = "8080"
	const filerootpath = "."
	mux := http.NewServeMux()

	cfg := apiConfig{fileserverhits: atomic.Int32{}}

	mux.Handle("/app/", cfg.middlewareMetricsInc(http.StripPrefix("/app", http.FileServer(http.Dir(filerootpath)))))
	mux.HandleFunc("/healthz", healthz)
	mux.HandleFunc("/metrics", cfg.metrics)
	mux.HandleFunc("/reset", cfg.reset)

	server := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}
	log.Printf("Serving from %s on port: %s\n", filerootpath, port)
	log.Fatal(server.ListenAndServe())
}

func healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(http.StatusText(http.StatusOK)))
	return
}

func (cfg *apiConfig) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(fmt.Sprintf("Hits: %d", cfg.fileserverhits.Load())))
	return
}
func (cfg *apiConfig) reset(w http.ResponseWriter, r *http.Request) {
	cfg.fileserverhits.Store(0)
	w.Header().Add("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(fmt.Sprintf("Hits: %d", cfg.fileserverhits.Load())))
	return
}
