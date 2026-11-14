package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// netManager owns the HTTPS listeners and can change them while the app runs, so access settings
// are configured in the web interface instead of through flags or environment variables.
//
//   - Local listener: always on (127.0.0.1 by default) with a self-signed certificate that is
//     generated once and stored, so your browser only asks to trust it the first time. Because it
//     never goes away, a wrong network setting can't lock you out.
//   - Extra listeners follow Settings: Let's Encrypt on :443 (plus :80 for the challenge), or a
//     certificate you supply on an address you choose.
type listenerStatus struct {
	Kind, Addr, Detail string
	OK                 bool
}

type netManager struct {
	mu        sync.Mutex
	a         *app
	handler   http.Handler
	localAddr string
	local     *http.Server
	extra     []*http.Server
	status    []listenerStatus
}

func newNetManager(a *app, h http.Handler, localAddr string) *netManager {
	return &netManager{a: a, handler: h, localAddr: localAddr}
}

func (m *netManager) Status() []listenerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]listenerStatus(nil), m.status...)
}

func (m *netManager) newServer(addr string, tc *tls.Config) *http.Server {
	return &http.Server{Addr: addr, Handler: m.handler, TLSConfig: tc, ReadTimeout: 30 * time.Second,
		WriteTimeout: 90 * time.Second, IdleTimeout: 2 * time.Minute}
}

// serve binds synchronously (so a busy port is reported), then serves in the background.
func (m *netManager) serve(kind string, srv *http.Server, tlsOn bool) {
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		m.status = append(m.status, listenerStatus{Kind: kind, Addr: srv.Addr, Detail: err.Error()})
		log.Println(kind, "listener:", err)
		return
	}
	m.status = append(m.status, listenerStatus{Kind: kind, Addr: srv.Addr, OK: true})
	go func() {
		var err error
		if tlsOn {
			err = srv.ServeTLS(ln, "", "")
		} else {
			err = srv.Serve(ln)
		}
		if err != nil && err != http.ErrServerClosed {
			log.Println(kind, "server:", err)
		}
	}()
}

// Start launches the local listener and whatever extra listeners Settings call for.
func (m *netManager) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	cert, err := m.a.localCert()
	if err != nil {
		log.Fatal(err)
	}
	m.local = m.newServer(m.localAddr, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	m.serve("This computer", m.local, true)
	if len(m.status) == 0 || !m.status[0].OK {
		log.Fatalf("cannot listen on %s (is BrokerBuddy already running? use -addr to pick another port)", m.localAddr)
	}
	m.startExtra()
}

// Reapply restarts the extra listeners after a settings change. The local listener keeps running.
func (m *netManager) Reapply() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.extra {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		s.Shutdown(ctx)
		cancel()
		s.Close()
	}
	m.extra = nil
	if len(m.status) > 1 { // keep only the local listener entry
		m.status = m.status[:1]
	}
	m.startExtra()
}

func (m *netManager) startExtra() {
	a := m.a
	switch a.getSetting("net_mode") {
	case "letsencrypt":
		domain := a.getSetting("net_domain")
		if !a.hasPassword() || domain == "" {
			m.status = append(m.status, listenerStatus{Kind: "Internet (Let's Encrypt)", Detail: "needs a domain and a password"})
			return
		}
		mgr := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(domain),
			Cache:      autocert.DirCache(filepath.Join(filepath.Dir(a.dbPath), "certs")),
			Email:      a.getSetting("net_email"),
		}
		if a.getSetting("net_staging") == "1" {
			mgr.Client = stagingClient() // test certificates; browsers won't trust them
		}
		redirect := m.newServer(":80", nil)
		redirect.Handler = mgr.HTTPHandler(nil) // answers ACME challenges, redirects the rest to https
		https := m.newServer(":443", mgr.TLSConfig())
		m.extra = append(m.extra, redirect, https)
		m.serve("Internet: http challenge", redirect, false)
		m.serve("Internet: https://"+domain, https, true)
	case "custom":
		if !a.hasPassword() {
			m.status = append(m.status, listenerStatus{Kind: "Custom certificate", Detail: "needs a password"})
			return
		}
		cert, err := parseKeyPair(a.getSecret("net_cert"), a.getSecret("net_key"))
		if err != nil {
			m.status = append(m.status, listenerStatus{Kind: "Custom certificate", Detail: err.Error()})
			return
		}
		addr := a.getSetting("net_addr")
		if addr == "" {
			addr = ":443"
		}
		srv := m.newServer(addr, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
		m.extra = append(m.extra, srv)
		m.serve("Custom certificate", srv, true)
	}
}

func parseKeyPair(certPEM, keyPEM string) (tls.Certificate, error) {
	if certPEM == "" || keyPEM == "" {
		return tls.Certificate{}, fmt.Errorf("paste both the certificate and the private key")
	}
	return tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
}

// localCert returns the self-signed certificate for the local listener, creating and storing it
// (private key encrypted) on first use so the browser's trust exception survives restarts.
func (a *app) localCert() (tls.Certificate, error) {
	if c, k := a.getSetting("selfsigned_cert"), a.getSecret("selfsigned_key"); c != "" && k != "" {
		if cert, err := tls.X509KeyPair([]byte(c), []byte(k)); err == nil {
			return cert, nil
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "BrokerBuddy local"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(5 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}))
	a.setSetting("selfsigned_cert", certPEM)
	a.setSecret("selfsigned_key", keyPEM)
	return tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
}
