package balancer

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

type LoadBalancer interface {
    ServeHTTP(w http.ResponseWriter, r *http.Request)
    StartHealthChecks(ctx context.Context)
}

type WeightedLeastConnection struct {
    servers       []*Server
    mu            sync.RWMutex
    totalRequests uint64
    mu2           sync.Mutex // For totalRequests
}

func NewWeightedLeastConnection(servers []*Server) *WeightedLeastConnection {
    return &WeightedLeastConnection{
        servers: servers,
    }
}

func (wlc *WeightedLeastConnection) NextServer() *Server {
    wlc.mu.RLock()
    defer wlc.mu.RUnlock()

    if len(wlc.servers) == 0 {
        return nil
    }

    var bestServer *Server
    bestRatio := 1e18

    for _, server := range wlc.servers {
        ratio := server.Ratio()
        if ratio < bestRatio {
            bestRatio = ratio
            bestServer = server
        }
    }

    return bestServer
}

func (wlc *WeightedLeastConnection) StartHealthChecks(ctx context.Context) {
    ticker := time.NewTicker(10 * time.Second)
    defer ticker.Stop()

    wlc.performHealthChecks()

    for {
        select {
        case <-ctx.Done():
            log.Println("Stopping health checks")
            return
        case <-ticker.C:
            wlc.performHealthChecks()
        }
    }
}

func (wlc *WeightedLeastConnection) performHealthChecks() {
    wlc.mu.RLock()
    servers := make([]*Server, len(wlc.servers))
    copy(servers, wlc.servers)
    wlc.mu.RUnlock()

    for _, server := range servers {
        err := server.HealthCheck()
        wasHealthy := server.IsHealthy.Load()
        isHealthy := err == nil

        server.IsHealthy.Store(isHealthy)

        if wasHealthy != isHealthy {
            if isHealthy {
                log.Printf("[HEALTH] ✅ Server %s is now HEALTHY (failures: %d)", 
                    server.URL.Host, server.FailureCount.Load())
            } else {
                log.Printf("[HEALTH] ❌ Server %s is now UNHEALTHY: %v (failures: %d)", 
                    server.URL.Host, err, server.FailureCount.Load())
            }
        }
    }
}

func (wlc *WeightedLeastConnection) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    if r.URL.Path == "/health" || r.URL.Path == "/healthz" {
        wlc.handleHealthEndpoint(w, r)
        return
    }

    if r.URL.Path == "/metrics" {
        wlc.handleMetricsEndpoint(w, r)
        return
    }

    server := wlc.NextServer()

    if server == nil || !server.IsHealthy.Load() {
        log.Printf("[ERROR] No healthy backend available for request %s %s", r.Method, r.URL.Path)
        http.Error(w, "Service Unavailable: No healthy backend servers available.", http.StatusServiceUnavailable)
        return
    }

    server.ActiveConnections.Add(1)
    server.RequestCount.Add(1)
    
    wlc.mu2.Lock()
    wlc.totalRequests++
    wlc.mu2.Unlock()

    log.Printf("[WLC] Forwarding %s %s to %s (Active: %d, Total: %d, Ratio: %.2f)",
        r.Method,
        r.URL.Path,
        server.URL.Host,
        server.ActiveConnections.Load(),
        server.RequestCount.Load(),
        server.Ratio())

    defer server.ActiveConnections.Add(-1)

    server.ReverseProxy.ServeHTTP(w, r)
}

func (wlc *WeightedLeastConnection) handleHealthEndpoint(w http.ResponseWriter, r *http.Request) {
    wlc.mu.RLock()
    defer wlc.mu.RUnlock()

    healthyCount := 0
    for _, server := range wlc.servers {
        if server.IsHealthy.Load() {
            healthyCount++
        }
    }

    if healthyCount == 0 {
        w.WriteHeader(http.StatusServiceUnavailable)
        w.Write([]byte("UNHEALTHY: No healthy backends"))
        return
    }

    w.WriteHeader(http.StatusOK)
    w.Write([]byte("OK"))
}

func (wlc *WeightedLeastConnection) handleMetricsEndpoint(w http.ResponseWriter, r *http.Request) {
    wlc.mu.RLock()
    defer wlc.mu.RUnlock()

    w.Header().Set("Content-Type", "text/plain")
    w.WriteHeader(http.StatusOK)

    wlc.mu2.Lock()
    totalReqs := wlc.totalRequests
    wlc.mu2.Unlock()

    w.Write([]byte("# Load Balancer Metrics\n\n"))
    w.Write([]byte("## Overall\n"))
    fmt.Fprintf(w, "Total Requests: %d\n", totalReqs)
    fmt.Fprintf(w, "Backend Servers: %d\n\n", len(wlc.servers))

    w.Write([]byte("## Backend Servers\n"))
    for i, server := range wlc.servers {
        fmt.Fprintf(w, "[%d] %s\n", i+1, server.URL.Host)
        fmt.Fprintf(w, "  Status: %s\n", map[bool]string{true: "HEALTHY", false: "UNHEALTHY"}[server.IsHealthy.Load()])
        fmt.Fprintf(w, "  Weight: %d\n", server.Weight)
        fmt.Fprintf(w, "  Active Connections: %d\n", server.ActiveConnections.Load())
        fmt.Fprintf(w, "  Total Requests: %d\n", server.RequestCount.Load())
        fmt.Fprintf(w, "  Failure Count: %d\n", server.FailureCount.Load())
        fmt.Fprintf(w, "  Last Check: %s\n", time.Unix(server.LastCheckTime.Load(), 0).Format(time.RFC3339))
        fmt.Fprintf(w, "  Ratio: %.2f\n\n", server.Ratio())
    }
}