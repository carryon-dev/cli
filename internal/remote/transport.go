package remote

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"math/big"
	"net"
	"time"

	"github.com/quic-go/quic-go"
)

// Transport wraps a UDP socket with a QUIC listener and provides
// methods for accepting and dialing QUIC connections, enumerating
// local network candidates, and performing STUN discovery.
type Transport struct {
	udpConn   *net.UDPConn
	transport *quic.Transport
	listener  *quic.Listener
	port      int
}

// NewTransport creates a UDP socket bound to the given port (0 for ephemeral),
// initializes a QUIC transport and listener using a self-signed TLS certificate.
func NewTransport(port int) (*Transport, error) {
	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
	if err != nil {
		return nil, fmt.Errorf("listen UDP: %w", err)
	}

	tr := &quic.Transport{Conn: udpConn}

	tlsConf, err := generateSelfSigned()
	if err != nil {
		udpConn.Close()
		return nil, fmt.Errorf("generate TLS: %w", err)
	}

	ln, err := tr.Listen(tlsConf, &quic.Config{})
	if err != nil {
		udpConn.Close()
		return nil, fmt.Errorf("listen QUIC: %w", err)
	}

	actualPort := udpConn.LocalAddr().(*net.UDPAddr).Port

	return &Transport{
		udpConn:   udpConn,
		transport: tr,
		listener:  ln,
		port:      actualPort,
	}, nil
}

// Addr returns the listener's local address.
func (t *Transport) Addr() net.Addr {
	return t.listener.Addr()
}

// Port returns the bound UDP port.
func (t *Transport) Port() int {
	return t.port
}

// Accept waits for and returns the next incoming QUIC connection.
func (t *Transport) Accept(ctx context.Context) (*quic.Conn, error) {
	return t.listener.Accept(ctx)
}

// Dial establishes a QUIC connection to the given address string.
// TLS verification is skipped since we use self-signed certificates.
func (t *Transport) Dial(ctx context.Context, addr string) (*quic.Conn, error) {
	udpAddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return nil, fmt.Errorf("resolve addr: %w", err)
	}
	return t.transport.Dial(ctx, udpAddr, &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec - self-signed certs used for QUIC transport layer
		NextProtos:         []string{"carryon"},
	}, &quic.Config{})
}

// LocalCandidates enumerates network interfaces and returns LAN candidates
// for the bound UDP port. Loopback, link-local, and IPv6 addresses are excluded.
func (t *Transport) LocalCandidates() []Candidate {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	var candidates []Candidate
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}

			if ip == nil {
				continue
			}
			// Skip IPv6
			if ip.To4() == nil {
				continue
			}
			// Skip loopback (belt-and-suspenders)
			if ip.IsLoopback() {
				continue
			}
			// Skip link-local (169.254.x.x)
			if ip.IsLinkLocalUnicast() {
				continue
			}

			candidates = append(candidates, Candidate{
				Type: "lan",
				Addr: ip.String(),
				Port: t.port,
			})
		}
	}
	return candidates
}

// STUNDiscover sends a STUN Binding Request to stunServer using the underlying
// UDP socket and parses the XOR-MAPPED-ADDRESS from the response.
// stunServer should be in "host:port" format.
func (t *Transport) STUNDiscover(ctx context.Context, stunServer string) (Candidate, error) {
	serverAddr, err := net.ResolveUDPAddr("udp4", stunServer)
	if err != nil {
		return Candidate{}, fmt.Errorf("resolve STUN server: %w", err)
	}

	// Build a minimal STUN Binding Request (RFC 5389).
	// Header: 2-byte type, 2-byte length, 4-byte magic cookie, 12-byte tx ID.
	var req [20]byte
	// Type: 0x0001 (Binding Request)
	req[0] = 0x00
	req[1] = 0x01
	// Length: 0 (no attributes)
	req[2] = 0x00
	req[3] = 0x00
	// Magic cookie: 0x2112A442
	req[4] = 0x21
	req[5] = 0x12
	req[6] = 0xA4
	req[7] = 0x42
	// Transaction ID: 12 random bytes
	if _, err := rand.Read(req[8:]); err != nil {
		return Candidate{}, fmt.Errorf("generate tx ID: %w", err)
	}

	if _, err := t.udpConn.WriteTo(req[:], serverAddr); err != nil {
		return Candidate{}, fmt.Errorf("send STUN request: %w", err)
	}

	// Read the response via ReadNonQUICPacket so the QUIC transport demux sees it.
	buf := make([]byte, 1024)
	n, _, err := t.transport.ReadNonQUICPacket(ctx, buf)
	if err != nil {
		return Candidate{}, fmt.Errorf("read STUN response: %w", err)
	}
	resp := buf[:n]

	ip, port, err := parseSTUNResponse(resp, req[8:])
	if err != nil {
		return Candidate{}, err
	}

	return Candidate{
		Type: "stun",
		Addr: ip.String(),
		Port: port,
	}, nil
}

// parseSTUNResponse parses a raw STUN Binding Response and returns the
// XOR-MAPPED-ADDRESS IP and port. txID is the 12-byte transaction ID
// from the original request - the response's transaction ID is validated
// against it to prevent response spoofing. The magic cookie in the response
// is also validated against the fixed value 0x2112A442 (RFC 5389).
func parseSTUNResponse(data []byte, txID []byte) (net.IP, int, error) {
	if len(data) < 20 {
		return nil, 0, fmt.Errorf("STUN response too short: %d bytes", len(data))
	}

	// Verify it's a Binding Success Response (0x0101).
	msgType := binary.BigEndian.Uint16(data[0:2])
	if msgType != 0x0101 {
		return nil, 0, fmt.Errorf("unexpected STUN message type: 0x%04x", msgType)
	}

	// Verify magic cookie (RFC 5389: must be 0x2112A442).
	cookie := binary.BigEndian.Uint32(data[4:8])
	if cookie != 0x2112A442 {
		return nil, 0, fmt.Errorf("invalid STUN magic cookie: 0x%08x", cookie)
	}

	// Verify transaction ID matches the one we sent to prevent response spoofing.
	if len(txID) == 12 && !bytes.Equal(data[8:20], txID) {
		return nil, 0, fmt.Errorf("STUN transaction ID mismatch")
	}

	attrLen := int(binary.BigEndian.Uint16(data[2:4]))
	if len(data) < 20+attrLen {
		return nil, 0, fmt.Errorf("STUN response truncated")
	}

	attrs := data[20 : 20+attrLen]
	for len(attrs) >= 4 {
		attrType := binary.BigEndian.Uint16(attrs[0:2])
		attrValueLen := int(binary.BigEndian.Uint16(attrs[2:4]))
		if len(attrs) < 4+attrValueLen {
			break
		}
		value := attrs[4 : 4+attrValueLen]

		if attrType == 0x0020 { // XOR-MAPPED-ADDRESS
			// value: 1 byte reserved, 1 byte family, 2 byte XOR port, 4 byte XOR IP
			if len(value) < 8 {
				return nil, 0, fmt.Errorf("XOR-MAPPED-ADDRESS too short")
			}
			// family: 0x01 = IPv4
			if value[1] != 0x01 {
				return nil, 0, fmt.Errorf("unsupported address family: %d", value[1])
			}
			// XOR port with upper 2 bytes of magic cookie (0x2112)
			xorPort := binary.BigEndian.Uint16(value[2:4])
			mappedPort := int(xorPort ^ 0x2112)

			// XOR IP with magic cookie
			magicCookie := []byte{0x21, 0x12, 0xA4, 0x42}
			ip := make([]byte, 4)
			for i := range ip {
				ip[i] = value[4+i] ^ magicCookie[i]
			}

			return net.IP(ip), mappedPort, nil
		}

		// Advance to next attribute (4-byte aligned).
		advance := 4 + attrValueLen
		if attrValueLen%4 != 0 {
			advance += 4 - (attrValueLen % 4)
		}
		if advance >= len(attrs) {
			break
		}
		attrs = attrs[advance:]
	}

	return nil, 0, fmt.Errorf("XOR-MAPPED-ADDRESS not found in STUN response")
}

// Close shuts down the QUIC listener and transport.
func (t *Transport) Close() error {
	if err := t.listener.Close(); err != nil {
		return err
	}
	return t.transport.Close()
}

// generateSelfSigned creates a TLS config with a self-signed ECDSA P-256 certificate
// valid for 24 hours, configured for the "carryon" ALPN protocol.
func generateSelfSigned() (*tls.Config, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ECDSA key: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create certificate: %w", err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{certDER},
			PrivateKey:  key,
		}},
		NextProtos: []string{"carryon"},
	}, nil
}
