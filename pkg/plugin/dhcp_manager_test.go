package plugin

import (
	"testing"
	"time"

	"github.com/thehaven/docker-net-dhcp/pkg/udhcpc"
)

func TestDHCPManagerRenew(t *testing.T) {
	m := &dhcpManager{}

	// Test IPv4 renew
	info := udhcpc.Info{
		IP:      "192.168.1.100/24",
		Gateway: "192.168.1.1",
	}

	err := m.renew(false, info)
	if err != nil {
		t.Fatalf("renew failed: %v", err)
	}

	if m.LastIP == nil || m.LastIP.IP.String() != "192.168.1.100" {
		t.Errorf("LastIP not updated correctly: %v", m.LastIP)
	}

	// Test IPv6 renew
	infoV6 := udhcpc.Info{
		IP: "2001:db8::1/64",
	}

	err = m.renew(true, infoV6)
	if err != nil {
		t.Fatalf("renew V6 failed: %v", err)
	}

	if m.LastIPv6 == nil || m.LastIPv6.IP.String() != "2001:db8::1" {
		t.Errorf("LastIPv6 not updated correctly: %v", m.LastIPv6)
	}
}

// TestDHCPManagerProcessEvents verifies that both "bound" and "renew" events
// update LastIP. The channel is closed after queuing both events so that
// processEvents returns cleanly — avoiding the data race that `time.Sleep`
// caused in the previous version of this test.
func TestDHCPManagerProcessEvents(t *testing.T) {
	m := &dhcpManager{
		joinReq:  JoinRequest{EndpointID: "12345678901234567890"},
		stopChan: make(chan struct{}),
	}

	events := make(chan udhcpc.Event, 2)
	events <- udhcpc.Event{
		Type: "bound",
		Data: udhcpc.Info{IP: "192.168.1.101/24"},
	}
	events <- udhcpc.Event{
		Type: "renew",
		Data: udhcpc.Info{IP: "192.168.1.102/24"},
	}
	close(events) // triggers the fixed EOF path

	done := make(chan struct{})
	go func() {
		m.processEvents(false, events)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("processEvents did not return after events channel closed")
	}

	// Safe to read: goroutine has returned.
	if m.LastIP == nil || m.LastIP.IP.String() != "192.168.1.102" {
		t.Errorf("expected LastIP 192.168.1.102 after bound+renew sequence, got %v", m.LastIP)
	}
}

// TestDHCPManagerProcessEventsFailure verifies that failure events (leasefail,
// nak, deconfig) are handled without crashing and that processEvents exits on
// channel close. Previously the test used time.Sleep and had a data race.
func TestDHCPManagerProcessEventsFailure(t *testing.T) {
	m := &dhcpManager{
		joinReq:  JoinRequest{EndpointID: "12345678901234567890"},
		stopChan: make(chan struct{}),
	}

	events := make(chan udhcpc.Event, 3)
	events <- udhcpc.Event{Type: "leasefail"}
	events <- udhcpc.Event{Type: "nak"}
	events <- udhcpc.Event{Type: "deconfig"}
	close(events)

	done := make(chan struct{})
	go func() {
		m.processEvents(false, events)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("processEvents timed out on failure events")
	}
}

// TestProcessEventsReturnsOnClosedChannel is the regression test for the
// "events channel deadlock" bug: when udhcpc exits unexpectedly its stdout
// pipe closes, which must cause processEvents to return rather than spin
// forever or block.
//
// With the UNFIXED code the closed channel causes an infinite spin-loop
// (the zero-value receive fires on every iteration and no case matches)
// so this test fails with a 200 ms timeout.
func TestProcessEventsReturnsOnClosedChannel(t *testing.T) {
	m := &dhcpManager{
		joinReq:  JoinRequest{EndpointID: "12345678901234567890"},
		stopChan: make(chan struct{}),
	}

	events := make(chan udhcpc.Event)

	done := make(chan struct{})
	go func() {
		m.processEvents(false, events)
		close(done)
	}()

	// Simulate udhcpc process exiting: its pipe closes.
	close(events)

	select {
	case <-done:
		// processEvents returned promptly — correct behaviour.
	case <-time.After(200 * time.Millisecond):
		close(m.stopChan) // unblock goroutine so the test can exit cleanly
		t.Fatal("processEvents did not return after events channel was closed (spin-loop or deadlock)")
	}
}

// TestProcessEventsStopsOnStopChan verifies the normal stop path still works.
func TestProcessEventsStopsOnStopChan(t *testing.T) {
	m := &dhcpManager{
		joinReq:  JoinRequest{EndpointID: "12345678901234567890"},
		stopChan: make(chan struct{}),
	}

	events := make(chan udhcpc.Event) // never closed, never written

	done := make(chan struct{})
	go func() {
		m.processEvents(false, events)
		close(done)
	}()

	close(m.stopChan) // normal stop

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("processEvents did not return after stopChan closed")
	}
}
