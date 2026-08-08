package udhcpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"syscall"

	log "github.com/sirupsen/logrus"

	"github.com/thehaven/docker-net-dhcp/pkg/util"
)

const (
	DefaultHandler = "/usr/lib/net-dhcp/udhcpc-handler"
	VendorID       = "docker-net-dhcp"
)

type DHCPClientOptions struct {
	Hostname   string
	MacAddress string
	V6         bool
	Once       bool
	Namespace  string

	HandlerScript string
}

// DHCPClient represents a udhcpc(6) client
type DHCPClient struct {
	Opts *DHCPClientOptions

	cmd       *exec.Cmd
	eventPipe io.ReadCloser
}

// NewDHCPClient creates a new udhcpc(6) client
func NewDHCPClient(iface string, opts *DHCPClientOptions) (*DHCPClient, error) {
	if opts.HandlerScript == "" {
		opts.HandlerScript = DefaultHandler
	}

	path := "udhcpc"
	if opts.V6 {
		path = "udhcpc6"
	}

	var cmd *exec.Cmd
	if opts.Namespace != "" {
		// Use nsenter to securely execute udhcpc inside the container's namespace
		cmd = exec.Command("nsenter", "--net="+opts.Namespace, path, "-f", "-i", iface, "-s", opts.HandlerScript)
	} else {
		cmd = exec.Command(path, "-f", "-i", iface, "-s", opts.HandlerScript)
	}

	c := &DHCPClient{
		Opts: opts,
		cmd:  cmd,
	}



	if opts.Once {
		c.cmd.Args = append(c.cmd.Args, "-t", "5", "-T", "3", "-A", "5")
	} else {
		c.cmd.Args = append(c.cmd.Args, "-T", "5", "-A", "30")
	}

	stderrPipe, err := c.cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to set up udhcpc stderr pipe: %w", err)
	}
	go io.Copy(log.StandardLogger().WriterLevel(log.DebugLevel), stderrPipe)

	if c.eventPipe, err = c.cmd.StdoutPipe(); err != nil {
		return nil, fmt.Errorf("failed to set up udhcpc stdout pipe: %w", err)
	}

	if opts.Once {
		c.cmd.Args = append(c.cmd.Args, "-q")
	}

	if opts.Hostname != "" {
		// DUAL SIGNALING: Use both Option 12 (hostname) and Option 81 (FQDN) for maximum compatibility
		hostnameOpt := "hostname:" + opts.Hostname
		if opts.V6 {
			var data bytes.Buffer
			binary.Write(&data, binary.BigEndian, uint8(0b0001))
			binary.Write(&data, binary.BigEndian, uint8(len(opts.Hostname)))
			data.WriteString(opts.Hostname)
			hostnameOpt = "0x27:" + hex.EncodeToString(data.Bytes())
		} else {
			// v4 Client FQDN (Option 81)
			c.cmd.Args = append(c.cmd.Args, "-F", opts.Hostname)
		}
		c.cmd.Args = append(c.cmd.Args, "-x", hostnameOpt)
	}

	if !opts.V6 {
		c.cmd.Args = append(c.cmd.Args, "-V", VendorID)
	}

	log.WithField("cmd", c.cmd).Trace("new udhcpc client")

	return c, nil
}

// Start starts udhcpc(6)
func (c *DHCPClient) Start() (chan Event, error) {
	if err := c.cmd.Start(); err != nil {
		return nil, err
	}

	// Buffer of 1 prevents a race in GetIP(): the scanner goroutine may
	// send a "bound" event before the caller's reader goroutine is ready.
	// Without the buffer, the unbuffered send blocks until a receiver is
	// available — but if Reap() → close(done) runs first, the reader
	// exits and the event is lost. Buffering one event decouples the
	// producer from the consumer safely for all callers.
	events := make(chan Event, 1)
	go func() {
		// Closing the channel signals to processEvents that the udhcpc process
		// has exited (pipe EOF). Without this close, processEvents would spin
		// forever on the zero-value receive from an unclosed closed channel,
		// silently preventing any further DHCP renewal.
		defer close(events)
		scanner := bufio.NewScanner(c.eventPipe)
		for scanner.Scan() {
			log.WithField("line", string(scanner.Bytes())).Trace("udhcpc handler line")

			var event Event
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				log.WithError(err).Warn("Failed to decode udhcpc event")
				continue
			}

			events <- event
		}
	}()

	return events, nil
}

// Reap waits for the udhcpc(6) process to exit naturally without sending any
// signal. Used by GetIP() for transient Once-mode DHCP probes where the process
// is expected to exit on its own — the persistent DHCP manager handles the
// actual lease lifecycle.
//
// IMPORTANT: Reap must NEVER send SIGTERM. Do NOT add signal-sending logic here.
// The GetIP() pipeline that calls this method relies on the process running to
// completion to return lease info. Premature termination silently breaks DHCP
// lease acquisition for new containers.
func (c *DHCPClient) Reap(ctx context.Context) error {
	errChan := make(chan error)
	go func() {
		errChan <- c.cmd.Wait()
	}()

	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		if c.cmd.Process != nil {
			c.cmd.Process.Kill()
		}
		return ctx.Err()
	}
}

// Release sends SIGTERM to the udhcpc(6) process to trigger a DHCPRELEASE,
// cleanly returning the lease to the server, then waits for the process to
// exit. Used by the persistent DHCP manager on container shutdown.
func (c *DHCPClient) Release(ctx context.Context) error {
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Signal(syscall.SIGTERM)
	}

	errChan := make(chan error)
	go func() {
		errChan <- c.cmd.Wait()
	}()

	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		if c.cmd.Process != nil {
			c.cmd.Process.Kill()
		}
		return ctx.Err()
	}
}

// GetIP is a convenience function that runs udhcpc(6) once and returns the IP info.
func GetIP(ctx context.Context, iface string, opts *DHCPClientOptions) (Info, error) {
	dummy := Info{}

	opts.Once = true
	client, err := NewDHCPClient(iface, opts)
	if err != nil {
		return dummy, fmt.Errorf("failed to create DHCP client: %w", err)
	}

	events, err := client.Start()
	if err != nil {
		return dummy, fmt.Errorf("failed to start DHCP client: %w", err)
	}

	// Read events in a goroutine. Use range-events (blocks until channel
	// closes on EOF) rather than select-with-done to avoid a scheduling
	// race: if Reap() returns before the reader goroutine processes the
	// "bound" event, the old done-channel-based loop could exit before
	// info is set.
	var info *Info
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for event := range events {
			switch event.Type {
			case "bound", "renew":
				info = &event.Data
			}
		}
	}()

	if err := client.Reap(ctx); err != nil {
		<-readerDone
		return dummy, err
	}

	// Reap() returned, so the scanner goroutine has seen EOF on the
	// process stdout and closed the events channel. range-events has
	// exited by now. Wait for the reader goroutine to confirm before
	// checking info to eliminate the race.
	<-readerDone

	if info == nil {
		return dummy, util.ErrNoLease
	}

	return *info, nil
}
