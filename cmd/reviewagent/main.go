package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bright/reviewagent/internal/app"
)

func main() {
	fixture := flag.String("fixture", "", "path to a unified diff to review locally")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	config, err := app.LoadConfig()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	service, err := app.NewService(config, logger)
	if err != nil {
		logger.Error("create service", "error", err)
		os.Exit(1)
	}
	defer func() { _ = service.Close() }()
	if *fixture != "" {
		if err := service.ReviewFixture(context.Background(), *fixture); err != nil {
			logger.Error("fixture review failed", "error", err)
			os.Exit(1)
		}
		return
	}

	server := &http.Server{
		Addr:              ":" + config.Port,
		Handler:           service.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	workerContext, stopWorker := context.WithCancel(context.Background())
	defer stopWorker()
	go service.Start(workerContext)
	go func() {
		logger.Info("reviewagent listening", "port", config.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server stopped", "error", err)
			os.Exit(1)
		}
	}()

	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-signalContext.Done()
	stopWorker()
	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		fmt.Fprintln(os.Stderr, "shutdown:", err)
	}
}
