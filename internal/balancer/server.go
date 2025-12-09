package balancer

import (
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

    RequestCount atomic.Uint64
    IsHealthy    atomic.Bool
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

    resp, err := client.Get(s.URL.String() + "/health")
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return nil
    }
    return nil
}

func NewServer(rawURL string, weight int) (*Server, error) {
    u, err := url.Parse(rawURL)
    if err != nil {
        return nil, err
    }

    proxy := httputil.NewSingleHostReverseProxy(u)

    originalDirector := proxy.Director
    proxy.Director = func(req *http.Request) {
        originalDirector(req)
        req.Host = u.Host
    }

    server := &Server{
        URL:          u,
        ReverseProxy: proxy,
        Weight:       weight,
    }
    server.IsHealthy.Store(true)

    return server, nil
}