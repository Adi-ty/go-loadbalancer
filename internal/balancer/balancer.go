package balancer

import (
	"context"
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
    servers []*Server
    mu      sync.RWMutex
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

    // Find server with lowest ratio without modifying the slice
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

    // Initial health check
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
                log.Printf("[HEALTH] ✅ Server %s is now HEALTHY", server.URL.Host)
            } else {
                log.Printf("[HEALTH] ❌ Server %s is now UNHEALTHY: %v", server.URL.Host, err)
            }
        }
    }
}

func (wlc *WeightedLeastConnection) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    server := wlc.NextServer()

    if server == nil || !server.IsHealthy.Load() {
        http.Error(w, "Service Unavailable: No healthy backend servers available.", http.StatusServiceUnavailable)
        return
    }

    server.ActiveConnections.Add(1)
    server.RequestCount.Add(1)
    
    log.Printf("[WLC] Forwarding to %s (Active: %d, Total: %d, Ratio: %.2f)",
        server.URL.Host, 
        server.ActiveConnections.Load(), 
        server.RequestCount.Load(),
        server.Ratio())

    defer server.ActiveConnections.Add(-1)

    server.ReverseProxy.ServeHTTP(w, r)
}