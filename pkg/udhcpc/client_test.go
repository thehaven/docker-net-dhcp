package udhcpc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"
)

func TestNewDHCPClientArgs(t *testing.T) {
	tests := []struct {
		name     string
		iface    string
		opts     DHCPClientOptions
		wantArgs []string
		wantPath string
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

// TestReleaseSendsSIGTERM verifies that Release() sends SIGTERM to the
// udhcpc process to trigger DHCPRELEASE before reaping.
func TestReleaseSendsSIGTERM(t *testing.T) {
	c := exec.Command("sleep", "30")

	client := &DHCPClient{
		cmd: c,
		Opts: &DHCPClientOptions{
			Once:          false,
			HandlerScript: "/usr/share/udhcpc/default.script",
		},
	}

	if err := client.cmd.Start(); err != nil {
		t.Fatalf("failed to start dummy process: %v", err)
	}
	if client.cmd.Process == nil {
		t.Fatal("process should be running after Start()")
	}

	err := client.Release(context.Background())

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError from SIGTERM-killed process, got: %v", err)
	}
	status := exitErr.Sys().(syscall.WaitStatus)
	if !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Errorf("expected SIGTERM signal, got signal=%v signalled=%v", status.Signal(), status.Signaled())
	}
}

// TestReapDoesNotSignal verifies that Reap() does NOT send any signal to the
// process — it only waits for natural exit. This is the contract GetIP()
// depends on: the Once-mode udhcpc must run to completion to return lease info.
// Premature termination (as introduced by a regression on 2026-07-21 where
// SIGTERM was added to Finish's Once path) silently breaks DHCP acquisition.
func TestReapDoesNotSignal(t *testing.T) {
	c := exec.Command("sleep", "5")

	client := &DHCPClient{
		cmd: c,
		Opts: &DHCPClientOptions{
			Once:          true,
			HandlerScript: "/usr/share/udhcpc/default.script",
		},
	}

	if err := client.cmd.Start(); err != nil {
		t.Fatalf("failed to start dummy process: %v", err)
	}

	// Reap() must NOT kill the process — it should wait for natural exit.
	// Since sleep 5 takes 5s, a short context timeout should trigger.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := client.Reap(ctx)

	// Should time out (context deadline exceeded) because sleep 5
	// is still running and we didn't signal it.
	if err == nil {
		// Cleanup: sleep 5 already exited
		return
	}
	if err != context.DeadlineExceeded {
		exitErr, ok := err.(*exec.ExitError)
		if ok && exitErr.Sys().(syscall.WaitStatus).Signaled() {
			t.Error("Reap() must NOT signal the process, but the process was killed")
		}
	}
}

// TestGetIPPipeline validates the full GetIP() pipeline: creates a Once-mode
// client, starts a fake udhcpc that emits a bound event, reads the lease via
// the event channel, and reaps the process via Reap(). This is the critical
// integration point that broke on 2026-07-21 when Finish() was changed to
// always send SIGTERM — the premature kill prevented the event from being
// delivered, causing silent DHCP acquisition failures.
func TestGetIPPipeline(t *testing.T) {
	// Copy the fake udhcpc script to a temp dir so we can prepend it to PATH
	// without affecting the real testdata location.
	fakeDir := t.TempDir()
	fakeUdhcpc := filepath.Join(fakeDir, "udhcpc")
	src, err := os.ReadFile("testdata/fake-udhcpc")
	if err != nil {
		t.Fatalf("failed to read fake udhcpc script: %v", err)
	}
	if err := os.WriteFile(fakeUdhcpc, src, 0755); err != nil {
		t.Fatalf("failed to write fake udhcpc: %v", err)
	}

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", fakeDir+string(filepath.ListSeparator)+oldPath)
	defer os.Setenv("PATH", oldPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	info, err := GetIP(ctx, "eth0", &DHCPClientOptions{
		Hostname: "test-container",
	})
	if err != nil {
		t.Fatalf("GetIP() returned error: %v", err)
	}

	if info.IP != "10.0.0.100" {
		t.Errorf("GetIP() IP = %q, want %q", info.IP, "10.0.0.100")
	}
	if info.Gateway != "10.0.0.1" {
		t.Errorf("GetIP() gateway = %q, want %q", info.Gateway, "10.0.0.1")
	}
}
