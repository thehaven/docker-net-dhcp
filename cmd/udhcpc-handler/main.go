package main

import (
	"encoding/json"
	"net"
	"os"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/thehaven/docker-net-dhcp/pkg/udhcpc"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatalf("Usage: %v <event type>", os.Args[0])
		return
	}

	event := udhcpc.Event{
		Type: os.Args[1],
	}

	switch event.Type {
	case "bound", "renew":
		if v6, ok := os.LookupEnv("ipv6"); ok {
			// Clean up the IP (udhcpc6 emits a _lot_ of zeros)
			_, netV6, err := net.ParseCIDR(v6 + "/128")
			if err != nil {
				log.WithError(err).Warn("Failed to parse IPv6 address")
			}

			event.Data.IP = netV6.String()
			ifaceStr := os.Getenv("interface")

			// Safety Net 5: IPv6 DAD check on bound
			if event.Type == "bound" && ifaceStr != "" && os.Getenv("DISABLE_ARP_CHECK") == "" {
				if hasV6Conflict(ifaceStr, netV6.IP) {
					log.Errorf("IPv6 address collision detected: %s is already active on %s", netV6.IP, ifaceStr)
					event.Type = "collision"
					_ = json.NewEncoder(os.Stdout).Encode(event)
					os.Exit(1)
				}
			}
		} else {
			ipStr := os.Getenv("ip")
			ifaceStr := os.Getenv("interface")
			maskStr := os.Getenv("mask")
			routerStr := os.Getenv("router")

			// Safety Net 2: Defensive environment parsing & fallbacks
			if maskStr == "" {
				maskStr = "255.255.255.0"
			}
			if routerStr == "" {
				log.Warn("DHCP server did not provide a gateway (router option 3)")
			}

			event.Data.IP = ipStr + "/" + maskStr
			event.Data.Gateway = routerStr
			event.Data.Domain = os.Getenv("domain")

			// Safety Net 3: Multi-probe L2 ARP check on bound to detect IP collision
			if event.Type == "bound" && ipStr != "" && ifaceStr != "" && os.Getenv("DISABLE_ARP_CHECK") == "" {
				if hasConflict(ifaceStr, ipStr) {
					log.Errorf("IP address collision detected: %s is already active on %s", ipStr, ifaceStr)
					event.Type = "collision"
					_ = json.NewEncoder(os.Stdout).Encode(event)
					os.Exit(1)
				}
			}

			// Safety Net 1: Send GARP broadcast on bound/renew to update neighboring ARP caches instantly
			if ipStr != "" && ifaceStr != "" {
				go sendGARP(ifaceStr, ipStr)
			}
		}
	case "deconfig", "leasefail", "nak", "collision":
		// No extra data needed for these events, just the type
	default:
		log.Warnf("Ignoring unknown event type `%v`", event.Type)
		return
	}

	if err := json.NewEncoder(os.Stdout).Encode(event); err != nil {
		log.WithError(err).Fatal("Failed to encode udhcpc event")
		return
	}
}

// Safety Net 3: Multi-probe ARP conflict checking
func hasConflict(ifaceName, targetIPStr string) bool {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return false
	}
	targetIP := net.ParseIP(targetIPStr)
	if targetIP == nil || targetIP.To4() == nil {
		return false
	}

	conn, err := net.ListenPacket("arp", ifaceName)
	if err != nil {
		return false
	}
	defer conn.Close()

	srcMAC := iface.HardwareAddr
	if len(srcMAC) < 6 {
		return false
	}

	frame := make([]byte, 42)
	copy(frame[0:6], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	copy(frame[6:12], srcMAC)
	frame[12] = 0x08
	frame[13] = 0x06

	frame[14] = 0x00; frame[15] = 0x01 // Hardware: Ethernet
	frame[16] = 0x08; frame[17] = 0x00 // Protocol: IPv4
	frame[18] = 0x06                   // HW size
	frame[19] = 0x04                   // Proto size
	frame[20] = 0x00; frame[21] = 0x01 // Opcode: Request
	copy(frame[22:28], srcMAC)         // Sender MAC
	copy(frame[28:32], net.IPv4zero.To4()) // Sender IP (0.0.0.0 for probe)
	copy(frame[32:38], []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00}) // Target MAC
	copy(frame[38:42], targetIP.To4())  // Target IP

	// Send 3 probes spaced 50ms apart
	for i := 0; i < 3; i++ {
		_, _ = conn.WriteTo(frame, nil)
		time.Sleep(50 * time.Millisecond)
	}

	_ = conn.SetDeadline(time.Now().Add(250 * time.Millisecond))
	buf := make([]byte, 1500)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			break
		}
		if n >= 42 && buf[12] == 0x08 && buf[13] == 0x06 { // ARP
			op := uint16(buf[20])<<8 | uint16(buf[21])
			replyIP := net.IP(buf[28:32])
			replyMAC := net.HardwareAddr(buf[22:28])
			if op == 2 && replyIP.Equal(targetIP.To4()) && replyMAC.String() != srcMAC.String() {
				return true
			}
		}
	}
	return false
}

// Safety Net 1: Gratuitous ARP (GARP) emission to instantly refresh switch/router ARP tables
func sendGARP(ifaceName, ipStr string) {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return
	}
	ip := net.ParseIP(ipStr)
	if ip == nil || ip.To4() == nil {
		return
	}

	conn, err := net.ListenPacket("arp", ifaceName)
	if err != nil {
		return
	}
	defer conn.Close()

	srcMAC := iface.HardwareAddr
	if len(srcMAC) < 6 {
		return
	}

	// Gratuitous ARP Reply (Opcode 2, Sender IP = Target IP = ip)
	frame := make([]byte, 42)
	copy(frame[0:6], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	copy(frame[6:12], srcMAC)
	frame[12] = 0x08
	frame[13] = 0x06

	frame[14] = 0x00; frame[15] = 0x01 // Ethernet
	frame[16] = 0x08; frame[17] = 0x00 // IPv4
	frame[18] = 0x06                   // HW size
	frame[19] = 0x04                   // Proto size
	frame[20] = 0x00; frame[21] = 0x02 // Opcode: Reply (GARP)
	copy(frame[22:28], srcMAC)         // Sender MAC
	copy(frame[28:32], ip.To4())       // Sender IP
	copy(frame[32:38], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}) // Target MAC (Broadcast)
	copy(frame[38:42], ip.To4())       // Target IP

	for i := 0; i < 2; i++ {
		_, _ = conn.WriteTo(frame, nil)
		time.Sleep(50 * time.Millisecond)
	}
}

// Safety Net 5: IPv6 Neighbor Solicitation (ICMPv6 DAD check)
func hasV6Conflict(ifaceName string, targetIP net.IP) bool {
	if targetIP == nil || targetIP.To16() == nil {
		return false
	}
	conn, err := net.ListenPacket("ip6:ipv6-icmp", ifaceName)
	if err != nil {
		return false
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(250 * time.Millisecond))
	buf := make([]byte, 1500)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			break
		}
		// ICMPv6 Neighbor Advertisement (Type 136)
		if n >= 24 && buf[0] == 136 {
			replyIP := net.ParseIP(addr.String())
			if replyIP != nil && replyIP.Equal(targetIP) {
				return true
			}
		}
	}
	return false
}
