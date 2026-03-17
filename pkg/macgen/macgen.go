// Package macgen provides deterministic, locally administered MAC generation.
package macgen

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
)

// Format defines output formatting style.
type Format string

const (
	FormatColon  Format = "colon"  // aa:bb:cc:dd:ee:ff
	FormatHyphen Format = "hyphen" // aa-bb-cc-dd-ee-ff
	FormatDot    Format = "dot"    // aabb.ccdd.eeff
)

// Options describes how to derive and format a MAC address.
type Options struct {
	Seed   string
	Format Format
}

// Generate returns a deterministic, locally administered MAC address string.
func Generate(opts Options) (string, error) {
	if opts.Seed == "" {
		return "", fmt.Errorf("seed cannot be empty")
	}
	if opts.Format == "" {
		opts.Format = FormatColon
	}

	sum := md5.Sum([]byte(opts.Seed))
	hexStr := hex.EncodeToString(sum[:])

	var macBytes [6]byte
	macBytes[0] = 0x02 // LAA, unicast
	for i := 1; i < 6; i++ {
		off := 2 * (i - 1)
		val, err := strconv.ParseUint(hexStr[off:off+2], 16, 8)
		if err != nil {
			return "", fmt.Errorf("failed to parse hex byte: %w", err)
		}
		macBytes[i] = byte(val)
	}

	switch opts.Format {
	case FormatColon:
		return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", macBytes[0], macBytes[1], macBytes[2], macBytes[3], macBytes[4], macBytes[5]), nil
	case FormatHyphen:
		return fmt.Sprintf("%02x-%02x-%02x-%02x-%02x-%02x", macBytes[0], macBytes[1], macBytes[2], macBytes[3], macBytes[4], macBytes[5]), nil
	case FormatDot:
		return fmt.Sprintf("%02x%02x.%02x%02x.%02x%02x", macBytes[0], macBytes[1], macBytes[2], macBytes[3], macBytes[4], macBytes[5]), nil
	default:
		return "", fmt.Errorf("unsupported format: %s", opts.Format)
	}
}

// GenerateDUID generates a deterministic DUID-LL (Link-Layer) identifier based on a generated MAC.
func GenerateDUID(macStr string) (string, error) {
	hwAddr, err := net.ParseMAC(macStr)
	if err != nil {
		return "", fmt.Errorf("invalid MAC address: %w", err)
	}

	// DUID-LL:
	// Type: 3 (2 bytes) -> 00:03
	// Hardware type: 1 (Ethernet, 2 bytes) -> 00:01
	// Link-layer address (MAC, 6 bytes)
	
	// RFC 3315 / RFC 8415 DUID-LL definition
	// Formatted as hex string with colons
	
	duid := fmt.Sprintf("00:03:00:01:%02x:%02x:%02x:%02x:%02x:%02x",
		hwAddr[0], hwAddr[1], hwAddr[2], hwAddr[3], hwAddr[4], hwAddr[5])
		
	return duid, nil
}
