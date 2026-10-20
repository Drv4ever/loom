package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ayush/loom/internal/loom"
)

// Runner dispatches CLI commands.
type Runner struct {
	Client *Client
	Out    io.Writer
	Err    io.Writer
}

// NewRunner creates a new Runner with standard streams.
func NewRunner(apiURL string) *Runner {
	return &Runner{
		Client: NewClient(apiURL),
		Out:    os.Stdout,
		Err:    os.Stderr,
	}
}

// Execute parses and runs the requested command arguments.
func (r *Runner) Execute(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return r.RunHelp(nil)
	}

	cmd := args[0]
	cmdArgs := args[1:]

	switch cmd {
	case "status":
		return r.RunStatus(ctx, cmdArgs)
	case "devices":
		return r.RunDevices(ctx, cmdArgs)
	case "services":
		return r.RunServices(ctx, cmdArgs)
	case "forwards":
		return r.RunForwards(ctx, cmdArgs)
	case "forward":
		return r.RunForward(ctx, cmdArgs)
	case "publish":
		return r.RunServicesPublish(ctx, cmdArgs)
	case "unpublish":
		return r.RunServicesUnpublish(ctx, cmdArgs)
	case "sync":
		return r.RunSync(ctx, cmdArgs)
	case "network":
		return r.RunNetwork(ctx, cmdArgs)
	case "relays", "relay":
		return r.RunRelays(ctx, cmdArgs)
	case "pair":
		return r.RunPair(ctx, cmdArgs)
	case "acl":
		return r.RunACL(ctx, cmdArgs)
	case "doctor":
		return r.RunDoctor(ctx, cmdArgs)
	case "help", "-h", "--help":
		return r.RunHelp(cmdArgs)
	default:
		return fmt.Errorf("unknown command %q. Run 'loom help' for available commands", cmd)
	}
}

// RunHelp displays CLI help.
func (r *Runner) RunHelp(args []string) error {
	fmt.Fprintln(r.Out, `Loom - Private encrypted mesh for services and folder sync

USAGE:
  loom [flags]                      Start the Loom daemon (default)
  loom <command> [options]          Control a running Loom daemon

COMMANDS:
  status                            Show network, device, service, and sync status
  devices                           List mesh devices
  devices connect <multiaddress>    Connect to a remote peer via multiaddress
  devices block <peer-id>           Block a device and drop all active connections
  devices unblock <peer-id>         Unblock a previously blocked device
  devices blocked                   List blocked devices
  pair                              Generate a 6-digit PIN to pair a new device
  pair join <pin> [--relay <addr>]  Join a network using an ephemeral pairing PIN
  pair status                       Check active pairing session
  acl                               Show granular service grants and folder permissions
  services                          List published services across the mesh
  publish <target> [--name <name>] [--trusted] [--allow <peers>]  Publish a local loopback HTTP service
  unpublish <id>                    Stop publishing a local service
  forwards                          List active TCP port forwards
  forward <peer>:<target> [--port P] Forward a local TCP port to a peer's service or port
  forward stop <id>                 Stop an active TCP port forward
  sync                              List sending and received sync folders
  sync add <path> --to <device> [--allow <peers>] [--readonly]  Sync a local folder to a target device
  sync now <folder-id>              Trigger an immediate sync
  sync pause <folder-id>            Pause syncing for a folder
  sync resume <folder-id>           Resume syncing for a folder
  sync remove <folder-id>           Stop tracking a folder
  sync conflicts <folder-id>        List active file conflicts for a folder
  sync resolve <id> <cid> --keep    Resolve a conflict ('local', 'remote', 'both')
  sync trash <folder-id>            Inspect soft-deleted files in safety archive
  relays                            List configured Circuit Relay v2 servers
  relays add <multiaddress>         Add a relay server to the network
  relays remove <id|address>        Remove a relay server
  network pause                     Pause all mesh peer networking
  network resume                    Resume mesh networking
  doctor                            Diagnose mesh daemon, ports, and health
  daemon [flags]                    Run the background Loom daemon
  help [command]                    Show this help message

GLOBAL OPTIONS:
  --api <url>                       Loom API URL (default: http://127.0.0.1:8787 or $LOOM_API_URL)
  --json                            Output machine-readable JSON where applicable

EXAMPLES:
  loom status
  loom publish localhost:3000 --name "Dev preview" --trusted
  loom forward linux:5432 --port 5432
  loom sync add ./my-assets --to "Studio Workstation"
  loom devices connect /ip4/192.168.1.50/tcp/4001/p2p/12D3KooW...
  loom doctor`)
	return nil
}

// RunStatus outputs overall mesh status.
func (r *Runner) RunStatus(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(r.Err)
	jsonOutput := fs.Bool("json", false, "Output in JSON format")
	if err := fs.Parse(args); err != nil {
		return err
	}

	state, err := r.Client.GetState(ctx)
	if err != nil {
		return err
	}

	if *jsonOutput {
		enc := json.NewEncoder(r.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(state)
	}

	// Network Overview
	netStatus := "Connected"
	if !state.Network.Connected {
		netStatus = "Paused"
	}
	PrintHeader(r.Out, "Network Overview")
	fmt.Fprintf(r.Out, "  Name:         %s\n", state.Network.Name)
	fmt.Fprintf(r.Out, "  Network ID:   %s\n", state.Network.ID)
	fmt.Fprintf(r.Out, "  Status:       %s\n", StatusDot(state.Network.Connected, netStatus))
	fmt.Fprintf(r.Out, "  Peer ID:      %s\n", state.Network.PeerID)
	fmt.Fprintf(r.Out, "  Version:      %s\n", state.Network.Version)
	if state.Network.Demo {
		fmt.Fprintf(r.Out, "  Mode:         %s\n", Colorize(colorYellow, "Local Demo"))
	}
	if len(state.Network.Addresses) > 0 {
		fmt.Fprintf(r.Out, "  Addresses:    %s\n", state.Network.Addresses[0])
		for _, addr := range state.Network.Addresses[1:] {
			fmt.Fprintf(r.Out, "                %s\n", addr)
		}
	}
	fmt.Fprintln(r.Out)

	// Summary stats
	fmt.Fprintf(r.Out, "  Summary: %s online device(s), %s service(s), %s synced file(s) (%s transferred)\n\n",
		Colorize(colorBold, fmt.Sprintf("%d", state.Stats.OnlineDevices)),
		Colorize(colorBold, fmt.Sprintf("%d", state.Stats.Services)),
		Colorize(colorBold, fmt.Sprintf("%d", state.Stats.SyncedFiles)),
		FormatBytes(state.Stats.TransferredBytes),
	)

	// Devices Table
	PrintHeader(r.Out, fmt.Sprintf("Devices (%d)", len(state.Devices)))
	w := NewTableWriter(r.Out)
	fmt.Fprintln(w, "NAME\tSTATUS\tCONNECTION\tPLATFORM\tID\tADDRESS\tLAST SEEN")
	for _, dev := range state.Devices {
		name := dev.Name
		if dev.Self {
			name += " (self)"
		}
		status := StatusDot(dev.Online, "offline")
		if dev.Online {
			status = StatusDot(true, "online")
		}
		conn := dev.Connection
		if conn == "direct" {
			conn = Colorize(colorCyan, "direct")
		} else if conn == "relay" {
			conn = Colorize(colorYellow, "relayed")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			name,
			status,
			conn,
			dev.Platform,
			truncate(dev.ID, 16),
			truncate(dev.Address, 36),
			FormatTimeRelative(dev.LastSeen),
		)
	}
	w.Flush()
	fmt.Fprintln(r.Out)

	// Services Table
	if len(state.Services) > 0 {
		PrintHeader(r.Out, fmt.Sprintf("Published Services (%d)", len(state.Services)))
		w = NewTableWriter(r.Out)
		fmt.Fprintln(w, "NAME\tDEVICE\tTARGET\tPROXY URL")
		for _, svc := range state.Services {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
				svc.Name,
				svc.DeviceName,
				svc.Target,
				svc.URL,
			)
		}
		w.Flush()
		fmt.Fprintln(r.Out)
	}

	// TCP Port Forwards Table
	if len(state.Forwards) > 0 {
		PrintHeader(r.Out, fmt.Sprintf("TCP Port Forwards (%d)", len(state.Forwards)))
		w = NewTableWriter(r.Out)
		fmt.Fprintln(w, "ID\tLOCAL ADDRESS\tDESTINATION PEER\tTARGET\tSTATUS")
		for _, f := range state.Forwards {
			fmt.Fprintf(w, "%s\t127.0.0.1:%d\t%s\t%s\t%s\n",
				f.ID,
				f.LocalPort,
				f.PeerName,
				f.Target,
				f.Status,
			)
		}
		w.Flush()
		fmt.Fprintln(r.Out)
	}

	// Sending Folders Table
	if len(state.Folders) > 0 {
		PrintHeader(r.Out, fmt.Sprintf("Sending Folders (%d)", len(state.Folders)))
		w = NewTableWriter(r.Out)
		fmt.Fprintln(w, "NAME\tDESTINATION\tSTATUS\tFILES\tSIZE\tPATH")
		for _, f := range state.Folders {
			status := f.Status
			if f.Conflicts > 0 {
				status = Colorize(colorYellow, fmt.Sprintf("conflict (%d)", f.Conflicts))
			} else if f.Paused {
				status = Colorize(colorYellow, "paused")
			} else if f.Status == "synced" {
				status = Colorize(colorGreen, "synced")
			} else if f.Status == "syncing" {
				status = Colorize(colorBlue, fmt.Sprintf("syncing (%d%%)", f.Progress))
			} else if f.Status == "error" {
				status = Colorize(colorRed, "error: "+f.Error)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n",
				f.Name,
				f.PeerName,
				status,
				f.Files,
				FormatBytes(f.Bytes),
				f.Path,
			)
		}
		w.Flush()
		fmt.Fprintln(r.Out)
	}

	// Received Folders Table
	if len(state.ReceivedFolders) > 0 {
		PrintHeader(r.Out, fmt.Sprintf("Received Folders (%d)", len(state.ReceivedFolders)))
		w = NewTableWriter(r.Out)
		fmt.Fprintln(w, "NAME\tSENDER\tFILES\tSIZE\tLOCAL PATH\tLAST RECEIVED")
		for _, rf := range state.ReceivedFolders {
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\n",
				rf.Name,
				rf.PeerName,
				len(rf.Files),
				FormatBytes(rf.Bytes),
				rf.Path,
				FormatTimeRelative(rf.LastReceived),
			)
		}
		w.Flush()
		fmt.Fprintln(r.Out)
	}

	return nil
}

// RunDevices handles 'devices' subcommands.
func (r *Runner) RunDevices(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "list" {
		fs := flag.NewFlagSet("devices", flag.ContinueOnError)
		fs.SetOutput(r.Err)
		jsonOutput := fs.Bool("json", false, "Output in JSON format")
		if len(args) > 0 && args[0] == "list" {
			if err := fs.Parse(args[1:]); err != nil {
				return err
			}
		} else {
			if err := fs.Parse(args); err != nil {
				return err
			}
		}

		state, err := r.Client.GetState(ctx)
		if err != nil {
			return err
		}

		if *jsonOutput {
			enc := json.NewEncoder(r.Out)
			enc.SetIndent("", "  ")
			return enc.Encode(state.Devices)
		}

		w := NewTableWriter(r.Out)
		fmt.Fprintln(w, "NAME\tSTATUS\tCONNECTION\tPLATFORM\tID\tADDRESS\tLAST SEEN")
		for _, dev := range state.Devices {
			name := dev.Name
			if dev.Self {
				name += " (self)"
			}
			status := StatusDot(dev.Online, "offline")
			if dev.Online {
				status = StatusDot(true, "online")
			}
			conn := dev.Connection
			if conn == "direct" {
				conn = Colorize(colorCyan, "direct")
			} else if conn == "relay" {
				conn = Colorize(colorYellow, "relayed")
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				name,
				status,
				conn,
				dev.Platform,
				dev.ID,
				dev.Address,
				FormatTimeRelative(dev.LastSeen),
			)
		}
		w.Flush()
		return nil
	}

	sub := args[0]
	switch sub {
	case "connect":
		if len(args) < 2 {
			return fmt.Errorf("usage: loom devices connect <multiaddress>")
		}
		multiaddr := args[1]
		fmt.Fprintf(r.Out, "Connecting to %s...\n", multiaddr)
		dev, err := r.Client.ConnectPeer(ctx, multiaddr)
		if err != nil {
			return fmt.Errorf("failed to connect: %w", err)
		}
		fmt.Fprintf(r.Out, "%s Connected to device %q (%s)\n",
			Colorize(colorGreen, "✓"), dev.Name, dev.ID)
		return nil
	case "block":
		if len(args) < 2 {
			return fmt.Errorf("usage: loom devices block <peer-id>")
		}
		peerID := args[1]
		if err := r.Client.BlockDevice(ctx, peerID); err != nil {
			return fmt.Errorf("failed to block device: %w", err)
		}
		fmt.Fprintf(r.Out, "%s Blocked device %s. All active connections dropped.\n",
			Colorize(colorGreen, "✓"), peerID)
		return nil
	case "unblock":
		if len(args) < 2 {
			return fmt.Errorf("usage: loom devices unblock <peer-id>")
		}
		peerID := args[1]
		if err := r.Client.UnblockDevice(ctx, peerID); err != nil {
			return fmt.Errorf("failed to unblock device: %w", err)
		}
		fmt.Fprintf(r.Out, "%s Unblocked device %s.\n",
			Colorize(colorGreen, "✓"), peerID)
		return nil
	case "blocked":
		blocked, err := r.Client.ListBlockedDevices(ctx)
		if err != nil {
			return fmt.Errorf("failed to list blocked devices: %w", err)
		}
		if len(blocked) == 0 {
			fmt.Fprintln(r.Out, "No blocked devices.")
			return nil
		}
		PrintHeader(r.Out, fmt.Sprintf("Blocked Devices (%d)", len(blocked)))
		for _, b := range blocked {
			fmt.Fprintf(r.Out, "  %s  %s\n", Colorize(colorRed, "✕"), b)
		}
		return nil
	default:
		return fmt.Errorf("unknown devices subcommand %q. Available: list, connect <multiaddr>, block <peer-id>, unblock <peer-id>, blocked", sub)
	}
}

// RunServices handles 'services' subcommands.
func (r *Runner) RunServices(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "list" {
		fs := flag.NewFlagSet("services", flag.ContinueOnError)
		fs.SetOutput(r.Err)
		jsonOutput := fs.Bool("json", false, "Output in JSON format")
		if len(args) > 0 && args[0] == "list" {
			if err := fs.Parse(args[1:]); err != nil {
				return err
			}
		} else {
			if err := fs.Parse(args); err != nil {
				return err
			}
		}

		state, err := r.Client.GetState(ctx)
		if err != nil {
			return err
		}

		if *jsonOutput {
			enc := json.NewEncoder(r.Out)
			enc.SetIndent("", "  ")
			return enc.Encode(state.Services)
		}

		if len(state.Services) == 0 {
			fmt.Fprintln(r.Out, "No services published. Publish one with: loom publish <target> --name <name>")
			return nil
		}

		w := NewTableWriter(r.Out)
		fmt.Fprintln(w, "ID\tNAME\tDEVICE\tTARGET\tPROXY URL")
		for _, svc := range state.Services {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				svc.ID,
				svc.Name,
				svc.DeviceName,
				svc.Target,
				svc.URL,
			)
		}
		w.Flush()
		return nil
	}

	sub := args[0]
	switch sub {
	case "publish":
		return r.RunServicesPublish(ctx, args[1:])
	case "unpublish":
		return r.RunServicesUnpublish(ctx, args[1:])
	default:
		return fmt.Errorf("unknown services subcommand %q. Available: list, publish, unpublish", sub)
	}
}

// RunServicesPublish handles publishing a local service.
func (r *Runner) RunServicesPublish(ctx context.Context, args []string) error {
	var target, name string
	var trusted bool
	var allowedPeers []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if (arg == "--name" || arg == "-name") && i+1 < len(args) {
			name = args[i+1]
			i++
		} else if strings.HasPrefix(arg, "--name=") || strings.HasPrefix(arg, "-name=") {
			name = strings.SplitN(arg, "=", 2)[1]
		} else if arg == "--trusted" || arg == "-trusted" {
			trusted = true
		} else if strings.HasPrefix(arg, "--trusted=") || strings.HasPrefix(arg, "-trusted=") {
			trusted = strings.SplitN(arg, "=", 2)[1] == "true"
		} else if (arg == "--allow" || arg == "-allow" || arg == "--allowed-peers") && i+1 < len(args) {
			for _, p := range strings.Split(args[i+1], ",") {
				if trimmed := strings.TrimSpace(p); trimmed != "" {
					allowedPeers = append(allowedPeers, trimmed)
				}
			}
			i++
		} else if strings.HasPrefix(arg, "--allow=") || strings.HasPrefix(arg, "--allowed-peers=") {
			val := strings.SplitN(arg, "=", 2)[1]
			for _, p := range strings.Split(val, ",") {
				if trimmed := strings.TrimSpace(p); trimmed != "" {
					allowedPeers = append(allowedPeers, trimmed)
				}
			}
		} else if !strings.HasPrefix(arg, "-") && target == "" {
			target = arg
		}
	}

	if target == "" {
		return fmt.Errorf("usage: loom publish <target-url> [--name <name>] [--trusted] [--allow <peer-ids>]\nExample: loom publish http://127.0.0.1:3000 --name preview --trusted --allow peerA,peerB")
	}

	// Normalization: if user typed "localhost:3000" or "127.0.0.1:3000", prepend http://
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		target = "http://" + target
	}
	u, err := url.Parse(target)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("invalid target URL %q: must be a loopback HTTP URL like http://127.0.0.1:3000", target)
	}

	svcName := name
	if svcName == "" {
		svcName = "Service on port " + u.Port()
		if u.Port() == "" {
			svcName = "Service " + u.Host
		}
	}

	svc, err := r.Client.PublishServiceWithOptions(ctx, svcName, target, trusted, allowedPeers)
	if err != nil {
		return fmt.Errorf("failed to publish service: %w", err)
	}

	fmt.Fprintf(r.Out, "%s Published service %q\n", Colorize(colorGreen, "✓"), svc.Name)
	fmt.Fprintf(r.Out, "  ID:        %s\n", svc.ID)
	fmt.Fprintf(r.Out, "  Target:    %s\n", svc.Target)
	if svc.Trusted {
		fmt.Fprintf(r.Out, "  Mode:      %s\n", Colorize(colorYellow, "Trusted (Origin/Auth/Cookie headers preserved)"))
	} else {
		fmt.Fprintf(r.Out, "  Mode:      %s\n", Colorize(colorCyan, "Sandboxed (headers stripped, CSP sandbox enforced)"))
	}
	if len(svc.AllowedPeers) > 0 {
		fmt.Fprintf(r.Out, "  Allowed:   %s\n", strings.Join(svc.AllowedPeers, ", "))
	} else {
		fmt.Fprintf(r.Out, "  Allowed:   All network peers\n")
	}
	fmt.Fprintf(r.Out, "  Proxy URL: %s%s\n", r.Client.BaseURL, svc.URL)
	return nil
}

// RunServicesUnpublish stops publishing a local service.
func (r *Runner) RunServicesUnpublish(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: loom unpublish <service-id>")
	}
	id := args[0]
	if err := r.Client.UnpublishService(ctx, id); err != nil {
		return fmt.Errorf("failed to unpublish service: %w", err)
	}
	fmt.Fprintf(r.Out, "%s Unpublished service %q\n", Colorize(colorGreen, "✓"), id)
	return nil
}

// RunForwards displays active TCP port forwards.
func (r *Runner) RunForwards(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("forwards", flag.ContinueOnError)
	fs.SetOutput(r.Err)
	jsonOutput := fs.Bool("json", false, "Output in JSON format")
	if err := fs.Parse(args); err != nil {
		return err
	}

	forwards, err := r.Client.ListForwards(ctx)
	if err != nil {
		return err
	}

	if *jsonOutput {
		enc := json.NewEncoder(r.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(forwards)
	}

	if len(forwards) == 0 {
		fmt.Fprintln(r.Out, "No active TCP port forwards.")
		fmt.Fprintln(r.Out, "\nTo forward a port to a remote service or TCP target:")
		fmt.Fprintln(r.Out, "  loom forward <peer>:<port-or-service> [--port <local-port>]")
		return nil
	}

	PrintHeader(r.Out, fmt.Sprintf("Active TCP Port Forwards (%d)", len(forwards)))
	w := NewTableWriter(r.Out)
	fmt.Fprintln(w, "ID\tLOCAL ADDRESS\tDESTINATION PEER\tTARGET\tSTATUS")
	for _, f := range forwards {
		fmt.Fprintf(w, "%s\t127.0.0.1:%d\t%s\t%s\t%s\n",
			f.ID,
			f.LocalPort,
			f.PeerName,
			f.Target,
			f.Status,
		)
	}
	w.Flush()
	return nil
}

// RunForward handles creating or stopping TCP port forwards.
func (r *Runner) RunForward(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: loom forward <peer>:<port-or-service> [--port <local-port>]\n       loom forward stop <id>\n       loom forwards")
	}

	if args[0] == "list" {
		return r.RunForwards(ctx, args[1:])
	}

	if args[0] == "stop" {
		if len(args) < 2 {
			return fmt.Errorf("usage: loom forward stop <id>")
		}
		id := args[1]
		if err := r.Client.StopForward(ctx, id); err != nil {
			return fmt.Errorf("failed to stop port forward: %w", err)
		}
		fmt.Fprintf(r.Out, "%s Stopped port forward %q\n", Colorize(colorGreen, "✓"), id)
		return nil
	}

	var destination string
	var localPort int
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if (arg == "--port" || arg == "-port" || arg == "-p") && i+1 < len(args) {
			p, err := strconv.Atoi(args[i+1])
			if err != nil || p < 1 || p > 65535 {
				return fmt.Errorf("invalid port %q: must be between 1 and 65535", args[i+1])
			}
			localPort = p
			i++
		} else if strings.HasPrefix(arg, "--port=") || strings.HasPrefix(arg, "-port=") || strings.HasPrefix(arg, "-p=") {
			val := strings.SplitN(arg, "=", 2)[1]
			p, err := strconv.Atoi(val)
			if err != nil || p < 1 || p > 65535 {
				return fmt.Errorf("invalid port %q: must be between 1 and 65535", val)
			}
			localPort = p
		} else if !strings.HasPrefix(arg, "-") && destination == "" {
			destination = arg
		}
	}

	if destination == "" {
		return fmt.Errorf("usage: loom forward <peer>:<port-or-service> [--port <local-port>]")
	}

	parts := strings.SplitN(destination, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("invalid forward destination %q: expected <peer>:<port-or-service>, e.g. 'linux:5432' or 'macbook:svc-abc'", destination)
	}
	peerSpec, targetSpec := parts[0], parts[1]

	state, err := r.Client.GetState(ctx)
	if err != nil {
		return err
	}

	// Resolve peer
	var targetPeerID, targetPeerName string
	for _, dev := range state.Devices {
		if dev.Self {
			continue
		}
		if dev.ID == peerSpec || strings.EqualFold(dev.Name, peerSpec) {
			targetPeerID = dev.ID
			targetPeerName = dev.Name
			break
		}
	}
	if targetPeerID == "" {
		for _, dev := range state.Devices {
			if dev.Self {
				continue
			}
			if strings.HasPrefix(strings.ToLower(dev.Name), strings.ToLower(peerSpec)) ||
				strings.HasPrefix(dev.ID, peerSpec) {
				targetPeerID = dev.ID
				targetPeerName = dev.Name
				break
			}
		}
	}
	if targetPeerID == "" {
		return fmt.Errorf("destination peer %q not found or offline", peerSpec)
	}

	var serviceID, target string
	// Check if targetSpec is an integer port number
	if portNum, err := strconv.Atoi(targetSpec); err == nil && portNum > 0 && portNum <= 65535 {
		target = fmt.Sprintf("127.0.0.1:%d", portNum)
		if localPort == 0 {
			localPort = portNum
		}
	} else {
		// Look for service matching targetSpec on that peer
		for _, svc := range state.Services {
			if (svc.DeviceID == targetPeerID || strings.EqualFold(svc.DeviceName, targetPeerName)) &&
				(svc.ID == targetSpec || strings.EqualFold(svc.Name, targetSpec) || strings.HasPrefix(svc.ID, targetSpec)) {
				serviceID = svc.ID
				target = svc.Target
				break
			}
		}
		if serviceID == "" {
			if strings.Contains(targetSpec, ":") {
				target = targetSpec
			} else {
				return fmt.Errorf("could not resolve %q to a port or published service on peer %q", targetSpec, targetPeerName)
			}
		}
	}

	fwd, err := r.Client.StartForward(ctx, targetPeerID, serviceID, target, localPort)
	if err != nil {
		return fmt.Errorf("failed to start port forward: %w", err)
	}

	fmt.Fprintf(r.Out, "%s Port forward active\n", Colorize(colorGreen, "✓"))
	fmt.Fprintf(r.Out, "  ID:          %s\n", fwd.ID)
	fmt.Fprintf(r.Out, "  Local:       127.0.0.1:%d\n", fwd.LocalPort)
	fmt.Fprintf(r.Out, "  Destination: %s (%s)\n", fwd.PeerName, fwd.Target)
	if fwd.ServiceID != "" {
		fmt.Fprintf(r.Out, "  Service ID:  %s\n", fwd.ServiceID)
	}
	return nil
}

// RunSync handles 'sync' subcommands.
func (r *Runner) RunSync(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "list" {
		fs := flag.NewFlagSet("sync", flag.ContinueOnError)
		fs.SetOutput(r.Err)
		jsonOutput := fs.Bool("json", false, "Output in JSON format")
		if len(args) > 0 && args[0] == "list" {
			if err := fs.Parse(args[1:]); err != nil {
				return err
			}
		} else {
			if err := fs.Parse(args); err != nil {
				return err
			}
		}

		state, err := r.Client.GetState(ctx)
		if err != nil {
			return err
		}

		if *jsonOutput {
			enc := json.NewEncoder(r.Out)
			enc.SetIndent("", "  ")
			return enc.Encode(map[string]any{
				"sending":  state.Folders,
				"received": state.ReceivedFolders,
			})
		}

		PrintHeader(r.Out, fmt.Sprintf("Sending Folders (%d)", len(state.Folders)))
		if len(state.Folders) == 0 {
			fmt.Fprintln(r.Out, "  No sending folders registered. Add one with: loom sync add <path> --to <device>")
		} else {
			w := NewTableWriter(r.Out)
			fmt.Fprintln(w, "ID\tNAME\tDESTINATION\tSTATUS\tFILES\tSIZE\tLOCAL PATH")
			for _, f := range state.Folders {
				status := f.Status
				if f.Conflicts > 0 {
					status = Colorize(colorYellow, fmt.Sprintf("conflict (%d)", f.Conflicts))
				} else if f.Paused {
					status = Colorize(colorYellow, "paused")
				} else if f.Status == "synced" {
					status = Colorize(colorGreen, "synced")
				} else if f.Status == "syncing" {
					status = Colorize(colorBlue, fmt.Sprintf("syncing (%d%%)", f.Progress))
				} else if f.Status == "error" {
					status = Colorize(colorRed, "error: "+f.Error)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
					f.ID,
					f.Name,
					f.PeerName,
					status,
					f.Files,
					FormatBytes(f.Bytes),
					f.Path,
				)
			}
			w.Flush()
		}
		fmt.Fprintln(r.Out)

		PrintHeader(r.Out, fmt.Sprintf("Received Folders (%d)", len(state.ReceivedFolders)))
		if len(state.ReceivedFolders) == 0 {
			fmt.Fprintln(r.Out, "  No folders received from peers yet.")
		} else {
			w := NewTableWriter(r.Out)
			fmt.Fprintln(w, "ID\tNAME\tSENDER\tFILES\tSIZE\tMANAGED PATH\tLAST RECEIVED")
			for _, rf := range state.ReceivedFolders {
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
					rf.ID,
					rf.Name,
					rf.PeerName,
					len(rf.Files),
					FormatBytes(rf.Bytes),
					rf.Path,
					FormatTimeRelative(rf.LastReceived),
				)
			}
			w.Flush()
		}
		return nil
	}

	sub := args[0]
	switch sub {
	case "add":
		return r.runSyncAdd(ctx, args[1:])
	case "now":
		if len(args) < 2 {
			return fmt.Errorf("usage: loom sync now <folder-id>")
		}
		id := args[1]
		folder, err := r.Client.SyncFolder(ctx, id)
		if err != nil {
			return fmt.Errorf("sync failed: %w", err)
		}
		fmt.Fprintf(r.Out, "%s Sync triggered for %q (%s)\n", Colorize(colorGreen, "✓"), folder.Name, folder.Status)
		return nil
	case "pause":
		if len(args) < 2 {
			return fmt.Errorf("usage: loom sync pause <folder-id>")
		}
		id := args[1]
		folder, err := r.Client.SetFolderPaused(ctx, id, true)
		if err != nil {
			return fmt.Errorf("pause failed: %w", err)
		}
		fmt.Fprintf(r.Out, "%s Paused sync for %q\n", Colorize(colorYellow, "⏸"), folder.Name)
		return nil
	case "resume":
		if len(args) < 2 {
			return fmt.Errorf("usage: loom sync resume <folder-id>")
		}
		id := args[1]
		folder, err := r.Client.SetFolderPaused(ctx, id, false)
		if err != nil {
			return fmt.Errorf("resume failed: %w", err)
		}
		fmt.Fprintf(r.Out, "%s Resumed sync for %q\n", Colorize(colorGreen, "✓"), folder.Name)
		return nil
	case "remove":
		if len(args) < 2 {
			return fmt.Errorf("usage: loom sync remove <folder-id>")
		}
		id := args[1]
		if err := r.Client.RemoveFolder(ctx, id); err != nil {
			return fmt.Errorf("remove failed: %w", err)
		}
		fmt.Fprintf(r.Out, "%s Stopped tracking folder %q (files kept on disk)\n", Colorize(colorGreen, "✓"), id)
		return nil
	case "conflicts":
		return r.runSyncConflicts(ctx, args[1:])
	case "resolve":
		return r.runSyncResolve(ctx, args[1:])
	case "trash":
		return r.runSyncTrash(ctx, args[1:])
	default:
		return fmt.Errorf("unknown sync subcommand %q. Available: list, add, now, pause, resume, remove, conflicts, resolve, trash", sub)
	}
}

func (r *Runner) runSyncAdd(ctx context.Context, args []string) error {
	var path, toDevice, name string
	var allowedPeers []string
	var readOnly bool
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if (arg == "--to" || arg == "-to") && i+1 < len(args) {
			toDevice = args[i+1]
			i++
		} else if strings.HasPrefix(arg, "--to=") || strings.HasPrefix(arg, "-to=") {
			toDevice = strings.SplitN(arg, "=", 2)[1]
		} else if (arg == "--name" || arg == "-name") && i+1 < len(args) {
			name = args[i+1]
			i++
		} else if strings.HasPrefix(arg, "--name=") || strings.HasPrefix(arg, "-name=") {
			name = strings.SplitN(arg, "=", 2)[1]
		} else if (arg == "--allow" || arg == "-allow" || arg == "--allowed-peers") && i+1 < len(args) {
			for _, p := range strings.Split(args[i+1], ",") {
				if trimmed := strings.TrimSpace(p); trimmed != "" {
					allowedPeers = append(allowedPeers, trimmed)
				}
			}
			i++
		} else if strings.HasPrefix(arg, "--allow=") || strings.HasPrefix(arg, "--allowed-peers=") {
			val := strings.SplitN(arg, "=", 2)[1]
			for _, p := range strings.Split(val, ",") {
				if trimmed := strings.TrimSpace(p); trimmed != "" {
					allowedPeers = append(allowedPeers, trimmed)
				}
			}
		} else if arg == "--readonly" || arg == "-readonly" || arg == "--read-only" {
			readOnly = true
		} else if !strings.HasPrefix(arg, "-") && path == "" {
			path = arg
		}
	}

	if path == "" || toDevice == "" {
		return fmt.Errorf("usage: loom sync add <path> --to <device-name-or-id> [--name <name>] [--allow <peers>] [--readonly]")
	}

	folderPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

	// Verify target device
	state, err := r.Client.GetState(ctx)
	if err != nil {
		return err
	}

	targetPeerID := ""
	targetPeerName := ""
	for _, dev := range state.Devices {
		if dev.Self {
			continue
		}
		if dev.ID == toDevice || strings.EqualFold(dev.Name, toDevice) {
			targetPeerID = dev.ID
			targetPeerName = dev.Name
			break
		}
	}
	if targetPeerID == "" {
		// Try prefix match on peer ID or name
		for _, dev := range state.Devices {
			if dev.Self {
				continue
			}
			if strings.HasPrefix(strings.ToLower(dev.Name), strings.ToLower(toDevice)) ||
				strings.HasPrefix(dev.ID, toDevice) {
				targetPeerID = dev.ID
				targetPeerName = dev.Name
				break
			}
		}
	}

	if targetPeerID == "" {
		return fmt.Errorf("device %q not found or offline. Available devices: %s", toDevice, availableDeviceNames(state.Devices))
	}

	folderName := name
	if folderName == "" {
		folderName = filepath.Base(folderPath)
	}

	folder, err := r.Client.AddFolderWithOptions(ctx, folderPath, targetPeerID, folderName, allowedPeers, readOnly)
	if err != nil {
		return fmt.Errorf("failed to add folder: %w", err)
	}

	fmt.Fprintf(r.Out, "%s Added sync folder %q\n", Colorize(colorGreen, "✓"), folder.Name)
	fmt.Fprintf(r.Out, "  ID:          %s\n", folder.ID)
	fmt.Fprintf(r.Out, "  Destination: %s (%s)\n", targetPeerName, folder.PeerID)
	fmt.Fprintf(r.Out, "  Local Path:  %s\n", folder.Path)
	if folder.ReadOnly {
		fmt.Fprintf(r.Out, "  Read Only:   %s\n", Colorize(colorYellow, "Yes (incoming remote writes blocked)"))
	}
	if len(folder.AllowedPeers) > 0 {
		fmt.Fprintf(r.Out, "  Allowed:     %s\n", strings.Join(folder.AllowedPeers, ", "))
	}
	return nil
}

func (r *Runner) runSyncConflicts(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: loom sync conflicts <folder-id> [--json]")
	}
	folderID := args[0]
	fs := flag.NewFlagSet("conflicts", flag.ContinueOnError)
	fs.SetOutput(r.Err)
	jsonOutput := fs.Bool("json", false, "Output in JSON format")
	if len(args) > 1 {
		_ = fs.Parse(args[1:])
	}

	conflicts, err := r.Client.GetConflicts(ctx, folderID)
	if err != nil {
		return err
	}

	if *jsonOutput {
		enc := json.NewEncoder(r.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(conflicts)
	}

	PrintHeader(r.Out, fmt.Sprintf("Active Conflicts for %s (%d)", folderID, len(conflicts)))
	if len(conflicts) == 0 {
		fmt.Fprintln(r.Out, "  No active conflicts for this folder.")
		return nil
	}

	w := NewTableWriter(r.Out)
	fmt.Fprintln(w, "ID\tFILE\tCONFLICT COPY\tREMOTE DEVICE\tDETECTED")
	for _, c := range conflicts {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			c.ID,
			c.Path,
			c.ConflictPath,
			c.DeviceName,
			FormatTimeRelative(c.CreatedAt),
		)
	}
	w.Flush()
	fmt.Fprintf(r.Out, "\nResolve with: loom sync resolve %s <conflict-id> --keep <local|remote|both>\n", folderID)
	return nil
}

func (r *Runner) runSyncResolve(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: loom sync resolve <folder-id> <conflict-id> --keep <local|remote|both>")
	}
	folderID := args[0]
	conflictID := args[1]
	var keep string
	for i := 2; i < len(args); i++ {
		arg := args[i]
		if (arg == "--keep" || arg == "-keep") && i+1 < len(args) {
			keep = args[i+1]
			i++
		} else if strings.HasPrefix(arg, "--keep=") || strings.HasPrefix(arg, "-keep=") {
			keep = strings.SplitN(arg, "=", 2)[1]
		}
	}
	if keep == "" {
		return fmt.Errorf("missing --keep flag. Choose 'local', 'remote', or 'both'")
	}
	res := loom.ConflictResolution(strings.ToLower(keep))
	if res != loom.ResolutionKeepLocal && res != loom.ResolutionKeepRemote && res != loom.ResolutionKeepBoth {
		return fmt.Errorf("invalid resolution %q: must be 'local', 'remote', or 'both'", keep)
	}

	if err := r.Client.ResolveConflict(ctx, folderID, conflictID, res); err != nil {
		return fmt.Errorf("resolve failed: %w", err)
	}

	fmt.Fprintf(r.Out, "%s Resolved conflict %q keeping %s\n", Colorize(colorGreen, "✓"), conflictID, keep)
	return nil
}

func (r *Runner) runSyncTrash(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: loom sync trash <folder-id> [--json]")
	}
	folderID := args[0]
	fs := flag.NewFlagSet("trash", flag.ContinueOnError)
	fs.SetOutput(r.Err)
	jsonOutput := fs.Bool("json", false, "Output in JSON format")
	if len(args) > 1 {
		_ = fs.Parse(args[1:])
	}

	entries, err := r.Client.GetTrash(ctx, folderID)
	if err != nil {
		return err
	}

	if *jsonOutput {
		enc := json.NewEncoder(r.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	}

	PrintHeader(r.Out, fmt.Sprintf("Safety Archive (Trash) for %s (%d)", folderID, len(entries)))
	if len(entries) == 0 {
		fmt.Fprintln(r.Out, "  Trash archive is empty.")
		return nil
	}

	w := NewTableWriter(r.Out)
	fmt.Fprintln(w, "ORIGINAL PATH\tSIZE\tDELETED")
	for _, e := range entries {
		fmt.Fprintf(w, "%s\t%s\t%s\n",
			e.OriginalPath,
			FormatBytes(e.Size),
			FormatTimeRelative(e.DeletedAt),
		)
	}
	w.Flush()
	return nil
}

func availableDeviceNames(devices []loom.Device) string {
	names := make([]string, 0, len(devices))
	for _, d := range devices {
		if !d.Self {
			names = append(names, fmt.Sprintf("%q (%s)", d.Name, truncate(d.ID, 10)))
		}
	}
	if len(names) == 0 {
		return "none (no remote peers connected)"
	}
	return strings.Join(names, ", ")
}

// RunNetwork pauses or resumes mesh peer networking.
func (r *Runner) RunNetwork(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "status" {
		state, err := r.Client.GetState(ctx)
		if err != nil {
			return err
		}
		status := "Active"
		if !state.Network.Connected {
			status = "Paused"
		}
		fmt.Fprintf(r.Out, "Mesh Networking: %s\n", StatusDot(state.Network.Connected, status))
		return nil
	}

	switch args[0] {
	case "pause":
		netState, err := r.Client.SetNetworkConnected(ctx, false)
		if err != nil {
			return fmt.Errorf("failed to pause network: %w", err)
		}
		fmt.Fprintf(r.Out, "%s Mesh networking paused (peer connections closed)\n", Colorize(colorYellow, "⏸"))
		_ = netState
		return nil
	case "resume":
		netState, err := r.Client.SetNetworkConnected(ctx, true)
		if err != nil {
			return fmt.Errorf("failed to resume network: %w", err)
		}
		fmt.Fprintf(r.Out, "%s Mesh networking resumed (reconnecting to peers)\n", Colorize(colorGreen, "✓"))
		_ = netState
		return nil
	default:
		return fmt.Errorf("unknown network command %q. Available: status, pause, resume", args[0])
	}
}

// RunDoctor performs system, network, and daemon diagnostics.
func (r *Runner) RunDoctor(ctx context.Context, args []string) error {
	PrintHeader(r.Out, "Loom Health & Diagnostics")

	// 1. Daemon HTTP API Check
	healthy, err := r.Client.GetHealth(ctx)
	if err != nil || !healthy {
		fmt.Fprintf(r.Out, "  [!] Daemon Status:    %s\n", Colorize(colorRed, "Unreachable"))
		fmt.Fprintf(r.Out, "      Error: %v\n", err)
		fmt.Fprintf(r.Out, "      Hint: Start the daemon with 'loom daemon' or check if another process is using port 8787.\n")
		return nil
	}
	fmt.Fprintf(r.Out, "  [✓] Daemon Status:    %s (listening at %s)\n", Colorize(colorGreen, "Running & Healthy"), r.Client.BaseURL)

	// 2. Fetch Diagnostics & State
	diag, err := r.Client.GetDiagnostics(ctx)
	if err != nil {
		fmt.Fprintf(r.Out, "  [!] Diagnostics API:  %s (%v)\n", Colorize(colorYellow, "Warning"), err)
	} else {
		uptime := int64(0)
		if u, ok := diag["uptimeSeconds"].(float64); ok {
			uptime = int64(u)
		}
		fmt.Fprintf(r.Out, "  [✓] Uptime:           %s\n", time.Duration(uptime)*time.Second)
		if limits, ok := diag["limits"].(map[string]any); ok {
			fmt.Fprintf(r.Out, "  [✓] Limits:           Max file size %s, Max proxy body %s\n",
				FormatBytes(int64(limits["fileBytes"].(float64))),
				FormatBytes(int64(limits["proxyRequestBytes"].(float64))),
			)
		}
	}

	state, err := r.Client.GetState(ctx)
	if err == nil {
		fmt.Fprintf(r.Out, "  [✓] Mesh Network ID:  %s (%s)\n", state.Network.ID, state.Network.Name)
		fmt.Fprintf(r.Out, "  [✓] Local Peer ID:    %s\n", state.Network.PeerID)
		fmt.Fprintf(r.Out, "  [✓] Connected Peers:  %d online of %d known\n", state.Stats.OnlineDevices, len(state.Devices))
		fmt.Fprintf(r.Out, "  [✓] Active Services:  %d published\n", len(state.Services))
		fmt.Fprintf(r.Out, "  [✓] Folders Synced:   %d files (%s transferred)\n", state.Stats.SyncedFiles, FormatBytes(state.Stats.TransferredBytes))
		if len(state.Relays) > 0 {
			fmt.Fprintf(r.Out, "  [✓] Configured Relays: %d relay server(s)\n", len(state.Relays))
		}
	}

	// 3. Port Checks
	ports := []int{8787, 3000, 4001}
	fmt.Fprintln(r.Out)
	PrintHeader(r.Out, "Port Availability Check")
	for _, port := range ports {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			desc := "Open / In use"
			if port == 8787 {
				desc = "In use by Loom daemon (expected)"
			} else if port == 3000 {
				desc = "In use (Web dashboard / Vite)"
			}
			fmt.Fprintf(r.Out, "  - Port %-5d: %s (%s)\n", port, Colorize(colorGreen, "Active"), desc)
		} else {
			fmt.Fprintf(r.Out, "  - Port %-5d: %s\n", port, Colorize(colorMuted, "Available / Closed"))
		}
	}

	fmt.Fprintln(r.Out)
	fmt.Fprintf(r.Out, "%s All core diagnostics completed successfully.\n", Colorize(colorGreen, "✓"))
	return nil
}

// RunRelays lists or manages relays.
func (r *Runner) RunRelays(ctx context.Context, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "add":
			return r.RunRelaysAdd(ctx, args[1:])
		case "remove", "rm", "delete":
			return r.RunRelaysRemove(ctx, args[1:])
		}
	}

	fs := flag.NewFlagSet("relays", flag.ContinueOnError)
	fs.SetOutput(r.Err)
	jsonOutput := fs.Bool("json", false, "output JSON format")
	if err := fs.Parse(args); err != nil {
		return err
	}

	relays, err := r.Client.ListRelays(ctx)
	if err != nil {
		return err
	}

	if *jsonOutput {
		enc := json.NewEncoder(r.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(relays)
	}

	PrintHeader(r.Out, fmt.Sprintf("Configured Relays (%d)", len(relays)))
	if len(relays) == 0 {
		fmt.Fprintln(r.Out, "  No relays configured. Add one with 'loom relays add <multiaddress>'.")
		return nil
	}

	w := NewTableWriter(r.Out)
	fmt.Fprintln(w, "STATUS\tPEER ID\tADDRESS")
	for _, relay := range relays {
		status := StatusDot(relay.Status == "connected", relay.Status)
		peerID := relay.PeerID
		if peerID == "" {
			peerID = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", status, truncate(peerID, 20), relay.Address)
	}
	w.Flush()
	return nil
}

// RunRelaysAdd adds a relay multiaddress.
func (r *Runner) RunRelaysAdd(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: loom relays add <multiaddress>")
	}
	addr := args[0]
	info, err := r.Client.AddRelay(ctx, addr)
	if err != nil {
		return err
	}
	fmt.Fprintf(r.Out, "%s Added relay %s (Peer: %s)\n", Colorize(colorGreen, "✓"), info.Address, info.PeerID)
	return nil
}

// RunRelaysRemove removes a relay by address or peer ID.
func (r *Runner) RunRelaysRemove(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: loom relays remove <peer-id-or-address>")
	}
	target := args[0]
	if err := r.Client.RemoveRelay(ctx, target); err != nil {
		return err
	}
	fmt.Fprintf(r.Out, "%s Removed relay %s\n", Colorize(colorGreen, "✓"), target)
	return nil
}

// RunPair handles ephemeral PIN pairing commands:
//   loom pair (or loom pair generate)
//   loom pair join <code> [--relay <addr>]
//   loom pair status
func (r *Runner) RunPair(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "generate" {
		session, err := r.Client.GeneratePairingCode(ctx, 10*time.Minute)
		if err != nil {
			return fmt.Errorf("failed to generate pairing code: %w", err)
		}
		PrintHeader(r.Out, "Ephemeral Device Pairing")
		fmt.Fprintf(r.Out, "  Pairing PIN:  %s\n", Colorize(colorBold+colorCyan, session.Formatted))
		fmt.Fprintf(r.Out, "  Expires in:   %s (%s)\n",
			Colorize(colorYellow, "10m 00s"),
			session.ExpiresAt.Format("15:04:05 MST"))
		fmt.Fprintf(r.Out, "  Topic Hash:   %s\n\n", session.Topic)
		fmt.Fprintln(r.Out, "  To pair another device, run the following command on that device:")
		fmt.Fprintf(r.Out, "    %s\n\n", Colorize(colorBold, "loom pair join "+session.Formatted))
		return nil
	}

	sub := args[0]
	switch sub {
	case "join":
		if len(args) < 2 {
			return fmt.Errorf("usage: loom pair join <6-digit-pin> [--relay <relay-multiaddr>]")
		}
		pin := args[1]
		var relay string
		for i := 2; i < len(args); i++ {
			arg := args[i]
			if (arg == "--relay" || arg == "-relay") && i+1 < len(args) {
				relay = args[i+1]
				i++
			} else if strings.HasPrefix(arg, "--relay=") {
				relay = strings.SplitN(arg, "=", 2)[1]
			}
		}

		fmt.Fprintf(r.Out, "Redeeming pairing PIN %s...\n", pin)
		payload, err := r.Client.ClaimPairingCode(ctx, pin, relay)
		if err != nil {
			return fmt.Errorf("pairing failed: %w", err)
		}

		fmt.Fprintf(r.Out, "%s Successfully paired with private network!\n", Colorize(colorGreen, "✓"))
		fmt.Fprintf(r.Out, "  Network ID:   %s\n", payload.NetworkID)
		if payload.NetworkName != "" {
			fmt.Fprintf(r.Out, "  Network Name: %s\n", payload.NetworkName)
		}
		if payload.IssuedBy != "" {
			fmt.Fprintf(r.Out, "  Paired Peer:  %s\n", payload.IssuedBy)
		}
		return nil

	case "status":
		session, err := r.Client.GetPairingSession(ctx)
		if err != nil {
			return fmt.Errorf("failed to fetch pairing status: %w", err)
		}
		if session == nil {
			fmt.Fprintln(r.Out, "No active pairing session.")
			return nil
		}
		remaining := time.Until(session.ExpiresAt).Round(time.Second)
		if remaining <= 0 {
			fmt.Fprintln(r.Out, "No active pairing session (expired).")
			return nil
		}
		PrintHeader(r.Out, "Active Pairing Session")
		fmt.Fprintf(r.Out, "  Pairing PIN:  %s\n", Colorize(colorBold+colorCyan, session.Formatted))
		fmt.Fprintf(r.Out, "  Remaining:    %s\n", remaining)
		fmt.Fprintf(r.Out, "  Topic Hash:   %s\n", session.Topic)
		return nil

	default:
		return fmt.Errorf("unknown pair subcommand %q. Available: generate, join <code>, status", sub)
	}
}

// RunACL displays access control lists, service grants, folder permissions, and blocked devices.
func (r *Runner) RunACL(ctx context.Context, args []string) error {
	state, err := r.Client.GetState(ctx)
	if err != nil {
		return err
	}

	PrintHeader(r.Out, "Access Control Overview (ACL)")

	// 1. Blocked Devices
	PrintHeader(r.Out, fmt.Sprintf("Blocked Devices (%d)", len(state.BlockedDevices)))
	if len(state.BlockedDevices) == 0 {
		fmt.Fprintln(r.Out, "  No devices blocked.")
	} else {
		for _, b := range state.BlockedDevices {
			fmt.Fprintf(r.Out, "  %s  %s\n", Colorize(colorRed, "✕"), b)
		}
	}
	fmt.Fprintln(r.Out)

	// 2. Service Access Grants
	PrintHeader(r.Out, fmt.Sprintf("Service Access Grants (%d)", len(state.Services)))
	if len(state.Services) == 0 {
		fmt.Fprintln(r.Out, "  No services published.")
	} else {
		w := NewTableWriter(r.Out)
		fmt.Fprintln(w, "SERVICE\tTARGET\tALLOWED PEERS")
		for _, svc := range state.Services {
			allowed := "All network peers"
			if len(svc.AllowedPeers) > 0 {
				allowed = strings.Join(svc.AllowedPeers, ", ")
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", svc.Name, svc.Target, allowed)
		}
		w.Flush()
	}
	fmt.Fprintln(r.Out)

	// 3. Folder Permissions
	PrintHeader(r.Out, fmt.Sprintf("Folder Permissions (%d)", len(state.Folders)))
	if len(state.Folders) == 0 {
		fmt.Fprintln(r.Out, "  No folders configured.")
	} else {
		w := NewTableWriter(r.Out)
		fmt.Fprintln(w, "FOLDER\tDESTINATION\tREAD ONLY\tALLOWED PEERS")
		for _, f := range state.Folders {
			ro := "No"
			if f.ReadOnly {
				ro = Colorize(colorYellow, "Yes")
			}
			allowed := "Default (destination only)"
			if len(f.AllowedPeers) > 0 {
				allowed = strings.Join(f.AllowedPeers, ", ")
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", f.Name, f.PeerName, ro, allowed)
		}
		w.Flush()
	}
	return nil
}
