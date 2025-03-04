package main

import (
	"crypto/tls"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/heptiolabs/healthcheck"
	"github.com/neicnordic/sensitive-data-archive/internal/config"
)

// HealthCheck registers and endpoint for healthchecking the service
type HealthCheck struct {
	port       int
	DB         *sql.DB
	s3URL      string
	brokerURL  string
	tlsConfig  *tls.Config
	serverCert string
	serverKey  string
}

// NewHealthCheck creates a new healthchecker. It needs to know where to find
// the backend S3 storage and the Message Broker so it can report readiness.
func NewHealthCheck(port int, db *sql.DB, conf *config.Config, tlsConfig *tls.Config) *HealthCheck {
	s3URL := conf.Inbox.S3.URL
	if conf.Inbox.S3.Port != 0 {
		s3URL = fmt.Sprintf("%s:%d", s3URL, conf.Inbox.S3.Port)
	}
	if conf.Inbox.S3.Readypath != "" {
		s3URL += conf.Inbox.S3.Readypath
	}

	brokerURL := fmt.Sprintf("%s:%d", conf.Broker.Host, conf.Broker.Port)

	serverCert := conf.Server.Cert
	serverKey := conf.Server.Key

	return &HealthCheck{port, db, s3URL, brokerURL, tlsConfig, serverCert, serverKey}
}

// RunHealthChecks should be run as a go routine in the main app. It registers
// the healthcheck handler on the port specified in when creating a new
// healthcheck.
func (h *HealthCheck) RunHealthChecks() {
	health := healthcheck.NewHandler()

	health.AddLivenessCheck("goroutine-threshold", healthcheck.GoroutineCountCheck(100))

	health.AddReadinessCheck("S3-backend-http", h.httpsGetCheck(h.s3URL, 5000*time.Millisecond))

	health.AddReadinessCheck("broker-tcp", healthcheck.TCPDialCheck(h.brokerURL, 5000*time.Millisecond))

	health.AddReadinessCheck("database", healthcheck.DatabasePingCheck(h.DB, 1*time.Second))

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			// readyEndpoint does not accept method head
			r.Method = http.MethodGet
			health.ReadyEndpoint(w, r)
		}
	})
	mux.HandleFunc("/health", health.ReadyEndpoint)

	addr := ":" + strconv.Itoa(h.port)
	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
		ReadHeaderTimeout: 3 * time.Second,
	}
	if h.serverCert != "" && h.serverKey != "" {
		if err := server.ListenAndServeTLS(h.serverCert, h.serverKey); err != nil {
			panic(err)
		}
	} else {
		if err := server.ListenAndServe(); err != nil {
			panic(err)
		}
	}
}

func (h *HealthCheck) httpsGetCheck(url string, timeout time.Duration) healthcheck.Check {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	cfg.RootCAs = h.tlsConfig.RootCAs
	tr := &http.Transport{TLSClientConfig: cfg}
	client := http.Client{
		Transport: tr,
		Timeout:   timeout,
		// never follow redirects
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return func() error {
		resp, e := client.Get(url)
		if e != nil {
			return e
		}
		_ = resp.Body.Close() // ignoring error
		if resp.StatusCode != 200 {
			return fmt.Errorf("returned status %d", resp.StatusCode)
		}

		return nil
	}
}
