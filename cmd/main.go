package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Adi-ty/go-loadbalancer/internal/balancer"
)

const listenPort = "8080"

func parseServerInput(input string) ([]*balancer.Server, error) {
    input = strings.TrimSpace(input)
    parts := strings.Split(input, ",")
    var servers []*balancer.Server

    for _, part := range parts {
        part = strings.TrimSpace(part)
        if part == "" {
            continue
        }

        addrWeight := strings.Split(part, "/")
        if len(addrWeight) != 2 {
            return nil, fmt.Errorf("invalid format for server %s. Expected: host:port/weight", part)
        }

        rawURL := addrWeight[0]
        weightStr := addrWeight[1]

        if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
            rawURL = "http://" + rawURL
        }

        weight, err := strconv.Atoi(weightStr)
        if err != nil || weight < 1 {
            return nil, fmt.Errorf("invalid weight for server %s. Must be an integer >= 1", rawURL)
        }

        server, err := balancer.NewServer(rawURL, weight)
        if err != nil {
            return nil, err
        }
        servers = append(servers, server)
        log.Printf("Added backend: %s (Weight: %d)", server.URL.String(), weight)
    }

    if len(servers) == 0 {
        return nil, fmt.Errorf("no valid backend servers configured")
    }
    return servers, nil
}

func main() {
    reader := bufio.NewReader(os.Stdin)
    fmt.Println("--- Weighted Least Connection Load Balancer ---")
    fmt.Println("Enter backend servers with weights separated by commas.")
    fmt.Println("Format: host:port/weight, host:port/weight")
    fmt.Println("Example: localhost:8081/5, localhost:8082/1")
    fmt.Print("> ")

    input, err := reader.ReadString('\n')
    if err != nil {
        log.Fatalf("Error reading backend servers: %v", err)
    }

    servers, err := parseServerInput(input)
    if err != nil {
        log.Fatalf("Configuration error: %v", err)
    }

    loadBalancer := balancer.NewWeightedLeastConnection(servers)

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    go loadBalancer.StartHealthChecks(ctx)

    srv := &http.Server{
        Addr:         ":" + listenPort,
        Handler:      loadBalancer,
        ReadTimeout:  15 * time.Second,
        WriteTimeout: 15 * time.Second,
        IdleTimeout:  60 * time.Second,
    }

    go func() {
        fmt.Printf("\n🚀 Starting Load Balancer on http://localhost:%s\n", listenPort)
        if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            log.Fatalf("Server failed: %v", err)
        }
    }()

    // Graceful shutdown
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
    <-sigChan

    log.Println("\n🛑 Shutting down gracefully...")
    cancel() // Stop health checks

    shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer shutdownCancel()

    if err := srv.Shutdown(shutdownCtx); err != nil {
        log.Printf("Server shutdown error: %v", err)
    }

    log.Println("✅ Shutdown complete")
}