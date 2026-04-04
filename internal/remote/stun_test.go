package remote

import (
	"encoding/binary"
	"net"
	"testing"
)

// buildSTUNResponse constructs a minimal STUN Binding Response (type 0x0101)
// with a single XOR-MAPPED-ADDRESS attribute for the given IP and port.
// txID must be exactly 12 bytes.
func buildSTUNResponse(txID []byte, ip net.IP, port int) []byte {
	// Attribute value: 1 reserved + 1 family + 2 XOR-port + 4 XOR-IP = 8 bytes.
	const attrValueLen = 8
	// Attribute header is 4 bytes (type + length).
	const attrTotalLen = 4 + attrValueLen // 12 bytes

	buf := make([]byte, 20+attrTotalLen)

	// STUN header.
	binary.BigEndian.PutUint16(buf[0:2], 0x0101) // Binding Response
	binary.BigEndian.PutUint16(buf[2:4], attrTotalLen)
	binary.BigEndian.PutUint32(buf[4:8], 0x2112A442) // magic cookie
	copy(buf[8:20], txID)

	// Attribute header.
	binary.BigEndian.PutUint16(buf[20:22], 0x0020) // XOR-MAPPED-ADDRESS
	binary.BigEndian.PutUint16(buf[22:24], attrValueLen)

	// Attribute value.
	buf[24] = 0x00        // reserved
	buf[25] = 0x01        // family IPv4
	xorPort := uint16(port) ^ 0x2112
	binary.BigEndian.PutUint16(buf[26:28], xorPort)
	magic := []byte{0x21, 0x12, 0xA4, 0x42}
	ip4 := ip.To4()
	for i := 0; i < 4; i++ {
		buf[28+i] = ip4[i] ^ magic[i]
	}

	return buf
}

func TestParseSTUNResponse_Valid(t *testing.T) {
	txID := make([]byte, 12)
	for i := range txID {
		txID[i] = byte(i + 1)
	}

	wantIP := net.IPv4(93, 184, 216, 34)
	wantPort := 8080

	data := buildSTUNResponse(txID, wantIP, wantPort)

	ip, port, err := parseSTUNResponse(data, txID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !ip.Equal(wantIP) {
		t.Errorf("IP: got %v, want %v", ip, wantIP)
	}
	if port != wantPort {
		t.Errorf("port: got %d, want %d", port, wantPort)
	}
}

func TestParseSTUNResponse_Errors(t *testing.T) {
	txID := make([]byte, 12)

	// A valid header used as a base for some cases.
	validHeader := func(attrLen uint16) []byte {
		buf := make([]byte, 20)
		binary.BigEndian.PutUint16(buf[0:2], 0x0101)
		binary.BigEndian.PutUint16(buf[2:4], attrLen)
		binary.BigEndian.PutUint32(buf[4:8], 0x2112A442)
		copy(buf[8:20], txID)
		return buf
	}

	cases := []struct {
		name string
		data []byte
	}{
		{
			name: "too short",
			data: []byte{0x01, 0x01},
		},
		{
			name: "wrong message type",
			data: func() []byte {
				buf := validHeader(0)
				binary.BigEndian.PutUint16(buf[0:2], 0x0001) // Binding Request
				return buf
			}(),
		},
		{
			name: "wrong magic cookie",
			data: func() []byte {
				buf := validHeader(0)
				binary.BigEndian.PutUint32(buf[4:8], 0xFFFFFFFF)
				return buf
			}(),
		},
		{
			name: "no attributes",
			data: validHeader(0), // valid header, zero-length body
		},
		{
			name: "truncated attributes",
			data: func() []byte {
				// Header says 16 bytes of attributes but we only append 4.
				buf := validHeader(16)
				buf = append(buf, 0x00, 0x20, 0x00, 0x08) // 4 bytes of attr data
				return buf
			}(),
		},
		{
			name: "no mapped address",
			data: func() []byte {
				// Valid response with an unknown attribute type 0x9999.
				const attrValueLen = 8
				const attrTotalLen = 4 + attrValueLen
				buf := validHeader(attrTotalLen)
				buf = append(buf, make([]byte, attrTotalLen)...)
				binary.BigEndian.PutUint16(buf[20:22], 0x9999) // unknown type
				binary.BigEndian.PutUint16(buf[22:24], attrValueLen)
				return buf
			}(),
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseSTUNResponse(tc.data, txID)
			if err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}
