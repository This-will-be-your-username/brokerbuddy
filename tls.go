package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// serve picks one of three HTTPS modes:
//  1. DOMAIN set        -> Let's Encrypt via autocert (needs public DNS + ports 80/443); requires APP_PASSWORD.
//  2. TLS_CERT/TLS_KEY  -> your own certificate files.
//  3. neither           -> in-memory self-signed cert for localhost (browser will warn once).
func serve(h http.Handler) {
	user, pass := env("APP_USER", "admin"), os.Getenv("APP_PASSWORD")
	h = secureHeaders(h)

	if domain := os.Getenv("DOMAIN"); domain != "" {
		if pass == "" {
			log.Fatal("APP_PASSWORD is required when DOMAIN is set (the app is public and has no other login)")
		}
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(domain),
			Cache:      autocert.DirCache(env("CERT_DIR", "certs")),
			Email:      os.Getenv("ACME_EMAIL"),
		}
		if os.Getenv("ACME_STAGING") == "1" { // avoid rate limits while testing
			m.Client = stagingClient()
		}
		// :80 answers ACME HTTP-01 challenges and redirects everything else to https.
		go func() {
			log.Fatal(http.ListenAndServe(":80", m.HTTPHandler(nil)))
		}()
		srv := &http.Server{
			Addr: ":443", Handler: basicAuth(user, pass, h),
			TLSConfig:   m.TLSConfig(),
			ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 2 * time.Minute,
		}
		log.Println("serving https://" + domain)
		log.Fatal(srv.ListenAndServeTLS("", ""))
	}

	addr := env("ADDR", "127.0.0.1:8443")
	if !isLoopback(addr) {
		if pass == "" {
			log.Fatal("APP_PASSWORD is required when ADDR is not loopback")
		}
		h = basicAuth(user, pass, h)
	}
	srv := &http.Server{Addr: addr, Handler: h, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second}

	if c, k := os.Getenv("TLS_CERT"), os.Getenv("TLS_KEY"); c != "" && k != "" {
		log.Println("serving https://" + addr)
		log.Fatal(srv.ListenAndServeTLS(c, k))
	}
	cert, err := selfSigned()
	if err != nil {
		log.Fatal(err)
	}
	srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	log.Println("serving https://" + addr + " (self-signed; set DOMAIN for Let's Encrypt)")
	log.Fatal(srv.ListenAndServeTLS("", ""))
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func basicAuth(user, pass string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		uOK := subtle.ConstantTimeCompare([]byte(u), []byte(user))
		pOK := subtle.ConstantTimeCompare([]byte(p), []byte(pass))
		if !ok || uOK&pOK != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="BrokerBuddy", charset="UTF-8"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Strict-Transport-Security", "max-age=31536000")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func selfSigned() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "BrokerBuddy local"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}
