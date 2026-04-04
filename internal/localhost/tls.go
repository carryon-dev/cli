package localhost

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// loadOrGenerateTLS loads an existing TLS certificate from baseDir/tls/ or
// generates a new self-signed one. Returns the TLS config and a hex-encoded
// SHA-256 fingerprint of the certificate.
func loadOrGenerateTLS(baseDir string) (*tls.Config, string, error) {
	tlsDir := filepath.Join(baseDir, "tls")
	certPath := filepath.Join(tlsDir, "cert.pem")
	keyPath := filepath.Join(tlsDir, "key.pem")

	// Try loading existing cert.
	if cert, fp, err := loadCert(certPath, keyPath); err == nil {
		return &tls.Config{Certificates: []tls.Certificate{cert}}, fp, nil
	}

	// Generate a new self-signed cert.
	if err := os.MkdirAll(tlsDir, 0700); err != nil {
		return nil, "", fmt.Errorf("create tls dir: %w", err)
	}

	cert, fp, err := generateSelfSigned(certPath, keyPath)
	if err != nil {
		return nil, "", err
	}

	return &tls.Config{Certificates: []tls.Certificate{cert}}, fp, nil
}

// loadCert loads a TLS certificate from disk and checks it hasn't expired.
func loadCert(certPath, keyPath string) (tls.Certificate, string, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, "", err
	}

	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return tls.Certificate{}, "", err
	}

	// Reject if expired or expiring within 7 days.
	if time.Now().After(leaf.NotAfter.Add(-7 * 24 * time.Hour)) {
		return tls.Certificate{}, "", fmt.Errorf("certificate expired or expiring soon")
	}

	fp := certFingerprint(cert.Certificate[0])
	return cert, fp, nil
}

// generateSelfSigned creates a new ECDSA P-256 self-signed certificate,
// writes it to disk, and returns the loaded tls.Certificate.
func generateSelfSigned(certPath, keyPath string) (tls.Certificate, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("generate key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("generate serial: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "carryOn localhost"},
		NotBefore:    now,
		NotAfter:     now.Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},

		DNSNames:    []string{"localhost"},
		IPAddresses: localIPAddresses(),
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("create certificate: %w", err)
	}

	// Write cert PEM.
	certFile, err := os.OpenFile(certPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("write cert: %w", err)
	}
	pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	certFile.Close()

	// Write key PEM.
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("marshal key: %w", err)
	}
	keyFile, err := os.OpenFile(keyPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("write key: %w", err)
	}
	pem.Encode(keyFile, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	keyFile.Close()

	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("reload generated cert: %w", err)
	}

	return cert, certFingerprint(certDER), nil
}

// localIPAddresses returns loopback and non-loopback IP addresses for all
// network interfaces, suitable for use as certificate SANs.
func localIPAddresses() []net.IP {
	ips := []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}

	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil && !ip.IsLoopback() {
				ips = append(ips, ip)
			}
		}
	}

	return ips
}

// certFingerprint returns the SHA-256 fingerprint of a DER-encoded certificate
// in colon-separated hex format (e.g. "AB:CD:EF:...").
func certFingerprint(certDER []byte) string {
	sum := sha256.Sum256(certDER)
	hex := hex.EncodeToString(sum[:])
	parts := make([]string, 0, 32)
	for i := 0; i < len(hex); i += 2 {
		parts = append(parts, strings.ToUpper(hex[i:i+2]))
	}
	return strings.Join(parts, ":")
}

// autoTLSListener wraps a TCP listener to handle both TLS and plain HTTP
// connections on the same port. TLS connections (first byte 0x16) are passed
// through to the TLS layer. Plain HTTP connections receive a 301 redirect
// to the HTTPS equivalent and are closed.
type autoTLSListener struct {
	net.Listener
	tlsConfig *tls.Config
	port      int
}

func (l *autoTLSListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}

		// Peek the first byte to distinguish TLS from plain HTTP.
		peeked := make([]byte, 1)
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := conn.Read(peeked)
		conn.SetReadDeadline(time.Time{})

		if err != nil || n == 0 {
			conn.Close()
			continue
		}

		// Prepend the peeked byte back so the reader sees the full stream.
		wrapped := &prefixConn{
			Conn:   conn,
			reader: io.MultiReader(strings.NewReader(string(peeked[:n])), conn),
		}

		if peeked[0] == 0x16 {
			// TLS ClientHello - wrap in TLS and return to the HTTP server.
			return tls.Server(wrapped, l.tlsConfig), nil
		}

		// Plain HTTP - redirect to HTTPS in background, then accept next conn.
		go l.redirectToHTTPS(wrapped)
	}
}

// redirectToHTTPS reads an HTTP request from a plain connection and responds
// with a 301 redirect to the HTTPS equivalent.
func (l *autoTLSListener) redirectToHTTPS(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	req, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		return
	}

	host := req.Host
	if host == "" {
		host = fmt.Sprintf("localhost:%d", l.port)
	}
	// Ensure host includes the port (browsers strip :443 but not custom ports).
	if !strings.Contains(host, ":") {
		host = fmt.Sprintf("%s:%d", host, l.port)
	}

	target := fmt.Sprintf("https://%s%s", host, req.URL.RequestURI())
	resp := fmt.Sprintf(
		"HTTP/1.1 301 Moved Permanently\r\nLocation: %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n",
		target,
	)
	conn.Write([]byte(resp))
}

// prefixConn wraps a net.Conn with a reader that first yields peeked bytes
// before reading from the underlying connection.
type prefixConn struct {
	net.Conn
	reader io.Reader
}

func (c *prefixConn) Read(b []byte) (int, error) {
	return c.reader.Read(b)
}
