/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	ebpfpkg "aviator/internal/ebpf"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func main() {
	var (
		listenAddr string
		bpfObjPath string
		maxAge     time.Duration
	)

	flag.StringVar(&listenAddr, "listen-address", ":9100", "HTTP address for the latency API")
	flag.StringVar(&bpfObjPath, "bpf-object", "/opt/aviator/tcp_latency.o", "Path to compiled eBPF object")
	flag.DurationVar(&maxAge, "max-sample-age", 60*time.Second, "Max age of latency samples")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	log := zap.New(zap.UseFlagOptions(&opts))

	log.Info("starting aviator eBPF agent",
		"listenAddr", listenAddr,
		"bpfObject", bpfObjPath,
	)

	// Create collector and loader.
	collector := ebpfpkg.NewCollector(log, maxAge)
	loader := ebpfpkg.NewLoader(log, collector)

	// Load and attach eBPF programs.
	if err := loader.Load(bpfObjPath); err != nil {
		log.Error(err, "failed to load eBPF programs")
		os.Exit(1)
	}
	defer loader.Close()

	// Start event reader.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := loader.Run(ctx); err != nil {
			log.Error(err, "eBPF event reader failed")
		}
	}()

	// Start HTTP API server.
	mux := http.NewServeMux()
	mux.HandleFunc("/latencies", latenciesHandler(log, collector))
	mux.HandleFunc("/healthz", healthzHandler)
	mux.HandleFunc("/readyz", readyzHandler)

	server := &http.Server{
		Addr:         listenAddr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("HTTP API listening", "addr", listenAddr)
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			log.Error(err, "HTTP server error")
			os.Exit(1)
		}
	}()

	// Wait for shutdown signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Info("received signal, shutting down", "signal", sig)

	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	server.Shutdown(shutdownCtx)
}

type agentResponse struct {
	PodLatencies map[string]ebpfpkg.PodStats `json:"podLatencies"`
	NodeName     string                       `json:"nodeName"`
}

func latenciesHandler(log logr.Logger, collector *ebpfpkg.Collector) http.HandlerFunc {
	nodeName := os.Getenv("NODE_NAME")

	return func(w http.ResponseWriter, r *http.Request) {
		stats := collector.GetStats()

		resp := agentResponse{
			PodLatencies: stats,
			NodeName:     nodeName,
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			log.Error(err, "failed to encode response")
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	}
}

func healthzHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func readyzHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}
