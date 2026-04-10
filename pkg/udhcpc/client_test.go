package udhcpc

import (
	"reflect"
	"testing"
)

func TestNewDHCPClientArgs(t *testing.T) {
	tests := []struct {
		name      string
		iface     string
		opts      DHCPClientOptions
		wantArgs  []string
		wantPath  string
	}{
		{
			name:  "Basic IPv4 without hostname",
			iface: "eth0",
			opts: DHCPClientOptions{
				V6: false,
			},
			wantArgs: []string{"udhcpc", "-f", "-i", "eth0", "-s", DefaultHandler, "-T", "5", "-A", "30", "-V", VendorID},
			wantPath: "udhcpc",
		},
		{
			name:  "IPv4 with hostname",
			iface: "eth1",
			opts: DHCPClientOptions{
				V6:       false,
				Hostname: "test-host",
			},
			wantArgs: []string{"udhcpc", "-f", "-i", "eth1", "-s", DefaultHandler, "-T", "5", "-A", "30", "-F", "test-host", "-x", "hostname:test-host", "-V", VendorID},
			wantPath: "udhcpc",
		},
		{
			name:  "IPv6 with hostname",
			iface: "eth0",
			opts: DHCPClientOptions{
				V6:       true,
				Hostname: "test-v6",
			},
			// For IPv6, the hostname option is converted to a custom hex string for Option 39 (0x27)
			// length of "test-v6" is 7. 0b0001 (1) + length (7) + "test-v6" (746573742d7636)
			// 01 07 74 65 73 74 2d 76 36 -> 0107746573742d7636
			wantArgs: []string{"udhcpc6", "-f", "-i", "eth0", "-s", DefaultHandler, "-T", "5", "-A", "30", "-x", "0x27:0107746573742d7636"},
			wantPath: "udhcpc6",
		},
		{
			name:  "With namespace",
			iface: "eth0",
			opts: DHCPClientOptions{
				V6:        false,
				Namespace: "/var/run/netns/testns",
			},
			wantArgs: []string{"nsenter", "--net=/var/run/netns/testns", "udhcpc", "-f", "-i", "eth0", "-s", DefaultHandler, "-T", "5", "-A", "30", "-V", VendorID},
			wantPath: "nsenter",
		},
		{
			name:  "Once mode",
			iface: "eth0",
			opts: DHCPClientOptions{
				V6:   false,
				Once: true,
			},
			wantArgs: []string{"udhcpc", "-f", "-i", "eth0", "-s", DefaultHandler, "-t", "5", "-T", "3", "-A", "5", "-q", "-V", VendorID},
			wantPath: "udhcpc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewDHCPClient(tt.iface, &tt.opts)
			if err != nil {
				t.Fatalf("NewDHCPClient() error = %v", err)
			}

			if client.cmd.Path != tt.wantPath && client.cmd.Args[0] != tt.wantPath {
				t.Errorf("NewDHCPClient() path = %v, want %v", client.cmd.Args[0], tt.wantPath)
			}

			if !reflect.DeepEqual(client.cmd.Args, tt.wantArgs) {
				t.Errorf("NewDHCPClient() args = %v, want %v", client.cmd.Args, tt.wantArgs)
			}
		})
	}
}
