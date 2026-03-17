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
	Hostname  string
	V6        bool
	Once      bool
	Namespace string

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
		// Initial lease acquisition: fast retries, limited attempts
		// -t 5: retry 5 times before giving up
		// -T 3: wait 3 seconds between retries
		// -A 5: wait 5 seconds before retrying after failure
		c.cmd.Args = append(c.cmd.Args, "-t", "5", "-T", "3", "-A", "5")
	} else {
		// Persistent client: relaxed settings to avoid DHCP server flooding
		// No -t: retry forever (container needs to maintain lease)
		// -T 5: wait 5 seconds between retries
		// -A 30: wait 30 seconds after failure before retrying (prevents thundering herd)
		c.cmd.Args = append(c.cmd.Args, "-T", "5", "-A", "30")
	}

	stderrPipe, err := c.cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to set up udhcpc stderr pipe: %w", err)
	}
	// Pipe udhcpc stderr (logs) to logrus at debug level
	go io.Copy(log.StandardLogger().WriterLevel(log.DebugLevel), stderrPipe)

	if c.eventPipe, err = c.cmd.StdoutPipe(); err != nil {
		return nil, fmt.Errorf("failed to set up udhcpc stdout pipe: %w", err)
	}

	if opts.Once {
		// Exit after obtaining lease
		c.cmd.Args = append(c.cmd.Args, "-q")
	} else {
		// Release IP address on exit
		c.cmd.Args = append(c.cmd.Args, "-R")
	}

	if opts.Hostname != "" {
		hostnameOpt := "hostname:" + opts.Hostname
		if opts.V6 {
			// TODO: We encode the fqdn for DHCPv6 because udhcpc6 seems to be broken
			var data bytes.Buffer

			// flags: S bit set (see RFC4704)
			binary.Write(&data, binary.BigEndian, uint8(0b0001))
			binary.Write(&data, binary.BigEndian, uint8(len(opts.Hostname)))
			data.WriteString(opts.Hostname)

			hostnameOpt = "0x27:" + hex.EncodeToString(data.Bytes())
		}

		c.cmd.Args = append(c.cmd.Args, "-x", hostnameOpt)
	}

	// Vendor ID string option is not available for udhcpc6
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

	events := make(chan Event)
	go func() {
		scanner := bufio.NewScanner(c.eventPipe)
		for scanner.Scan() {
			log.WithField("line", string(scanner.Bytes())).Trace("udhcpc handler line")

			// Each line is a JSON-encoded event
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

// Finish sends SIGTERM to udhcpc(6) and waits for it to exit. SIGTERM will not
// be sent if `Opts.Once` is set.
func (c *DHCPClient) Finish(ctx context.Context) error {
	// If only running to get an IP once, udhcpc will terminate on its own
	if !c.Opts.Once {
		if err := c.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			return fmt.Errorf("failed to send SIGTERM to udhcpc: %w", err)
		}
	}

	errChan := make(chan error)
	go func() {
		errChan <- c.cmd.Wait()
	}()

	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		c.cmd.Process.Kill()
		return ctx.Err()
	}
}

// GetIP is a convenience function that runs udhcpc(6) once and returns the IP
// info obtained.
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

	var info *Info
	done := make(chan struct{})
	go func() {
		for {
			select {
			case event := <-events:
				switch event.Type {
				case "bound", "renew":
					info = &event.Data
				}
			case <-done:
				return
			}
		}
	}()
	defer close(done)

	if err := client.Finish(ctx); err != nil {
		return dummy, err
	}

	if info == nil {
		return dummy, util.ErrNoLease
	}

	return *info, nil
}
