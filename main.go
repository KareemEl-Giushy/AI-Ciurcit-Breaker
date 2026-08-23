package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"circuit-breaker-proxy/inspector"
	"circuit-breaker-proxy/proxy"
	"circuit-breaker-proxy/utils"
)

func main() {
	// Parse, overlay (Defaults -> YAML -> Environment -> CLI Flags), and validate configuration
	cfg, err := utils.ParseAndValidateConfig(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s❌ Configuration error:%s %v\n", utils.ColorRed, utils.ColorReset, err)
		os.Exit(1)
	}

	// Configure structured logger
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}))
	slog.SetDefault(logger)

	// Initialize Sliding Window Inspector
	windowInspector := inspector.NewSlidingWindow(inspector.SlidingWindowConfig{
		WindowDuration: cfg.WindowDuration,
		MaxRequests:    cfg.MaxRequests,
		MaxTokens:      cfg.MaxTokens,
		EnforceLimits:  cfg.EnforceLimits,
	})

	// Initialize Circuit Breaker Engine with SimHash and Jaccard
	cbEngine := inspector.NewCircuitBreakerEngine(inspector.CircuitBreakerConfig{
		WindowDuration:     cfg.WindowDuration,
		MaxToolRepeats:     cfg.CBMaxToolRepeats,
		MaxToolErrors:      cfg.CBMaxToolErrors,
		MaxHammingDistance: cfg.CBMaxHammingDist,
		JaccardThreshold:   cfg.CBJaccardThreshold,
		Enabled:            cfg.CBEnabled,
	})

	// Initialize Velocity Detection Engine
	velocityDetector := inspector.NewVelocityDetector(inspector.VelocityConfig{
		MaxRPS:             cfg.VelocityMaxRPS,
		MaxEndpointRepeats: cfg.VelocityMaxEndpointRepeats,
		RepeatWindow:       cfg.VelocityRepeatWindow,
		Enabled:            cfg.VelocityEnabled,
	})

	// Create reverse proxy handler with inspector, circuit breaker, velocity detector, and conversation recorder
	proxyHandler := proxy.NewServerHandler(proxy.Config{
		TargetURL:         cfg.TargetURL,
		Logger:            logger,
		Inspector:         windowInspector,
		CircuitBreaker:    cbEngine,
		Velocity:          velocityDetector,
		ShowSlidingWindow: cfg.ShowSlidingWindow,
		SaveConversations: cfg.SaveConversations,
		SaveDir:           cfg.SaveDir,
	})

	serverAddr := fmt.Sprintf(":%s", cfg.Port)
	srv := &http.Server{
		Addr:              serverAddr,
		Handler:           proxyHandler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	if cfg.LoadedConfigFile != "" {
		fmt.Printf("  %s📄 Loaded configuration from :%s %s%s%s\n", utils.ColorBold, utils.ColorReset, utils.ColorCyan, cfg.LoadedConfigFile, utils.ColorReset)
	}

	utils.PrintStartupBanner(
		cfg.Port, cfg.TargetURLString,
		cfg.WindowDuration, cfg.MaxRequests, cfg.MaxTokens, cfg.EnforceLimits,
		cfg.CBMaxToolRepeats, cfg.CBMaxToolErrors, cfg.CBMaxHammingDist, cfg.CBJaccardThreshold,
		cfg.VelocityMaxRPS, cfg.VelocityMaxEndpointRepeats, cfg.VelocityRepeatWindow,
		cfg.SaveConversations, cfg.SaveDir,
	)

	// Graceful shutdown handling
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server encountered error", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	fmt.Printf("\n%s🛑 Shutting down reverse proxy gracefully...%s\n", utils.ColorYellow, utils.ColorReset)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("server forced to shutdown", slog.String("error", err.Error()))
	} else {
		fmt.Printf("%s✨ Server stopped cleanly.%s\n\n", utils.ColorGreen, utils.ColorReset)
	}
}
