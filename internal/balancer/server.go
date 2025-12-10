package balancer

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"time"
)

type Server struct {
    URL          *url.URL
    ReverseProxy *httputil.ReverseProxy

    ActiveConnections atomic.Int32
    Weight            int

    RequestCount  atomic.Uint64
    IsHealthy     atomic.Bool
    FailureCount  atomic.Uint32
    LastCheckTime atomic.Int64
}

func (s *Server) Ratio() float64 {
    if !s.IsHealthy.Load() {
        return 1e18 // Infinite ratio for unhealthy servers
    }

    conn := float64(s.ActiveConnections.Load())
    w := float64(s.Weight)

    if w == 0 {
        return 1e18
    }
    return conn / w
}

func (s *Server) HealthCheck() error {
    client := &http.Client{
        Timeout: 3 * time.Second,
    }

    s.LastCheckTime.Store(time.Now().Unix())

    resp, err := client.Get(s.URL.String() + "/health")
    if err != nil {
        s.FailureCount.Add(1)
        return fmt.Errorf("health check failed: %w", err)
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != http.StatusOK {
        s.FailureCount.Add(1)
        return fmt.Errorf("health check returned status %d", resp.StatusCode)
    }
   
    s.FailureCount.Store(0)
    return nil
}

func NewServer(rawURL string, weight int) (*Server, error) {
    u, err := url.Parse(rawURL)
    if err != nil {
        return nil, err
    }

    proxy := httputil.NewSingleHostReverseProxy(u)

    // Enhanced error handling for proxy
    proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
        w.WriteHeader(http.StatusBadGateway)
    }

    originalDirector := proxy.Director
    proxy.Director = func(req *http.Request) {
        originalDirector(req)
        req.Host = u.Host
        // load balancer identification
        req.Header.Set("X-Forwarded-By", "go-loadbalancer")
    }

    server := &Server{
        URL:          u,
        ReverseProxy: proxy,
        Weight:       weight,
    }
    server.IsHealthy.Store(true)
    server.LastCheckTime.Store(time.Now().Unix())

    return server, nil
}