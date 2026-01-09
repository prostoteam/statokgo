package core

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	gnet "github.com/shirou/gopsutil/v3/net"

	statok "github.com/prostoteam/statokgo"
)

type NetCollector struct {
	every time.Duration
}

func NewNet(every time.Duration) *NetCollector {
	return &NetCollector{every: every}
}

func (c *NetCollector) ID() string { return "core.net" }

func (c *NetCollector) Every() time.Duration { return c.every }

func (c *NetCollector) Collect(_ context.Context, host string) error {
	if runtime.GOOS != "linux" {
		return collectNetGopsutil(host)
	}
	return collectNetProc(host)
}

func collectNetProc(host string) error {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return fmt.Errorf("open /proc/net/dev: %w", err)
	}
	defer f.Close()

	hostLabel := statok.Label("host", host)

	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if lineNum <= 2 {
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) != 2 {
			continue
		}
		iface := strings.TrimSpace(parts[0])
		if iface == "" {
			continue
		}
		if skipNetInterface(iface) {
			continue
		}

		fields := strings.Fields(strings.TrimSpace(parts[1]))
		if len(fields) < 16 {
			continue
		}

		rxBytes, _ := parseUint(fields[0])
		rxPackets, _ := parseUint(fields[1])
		rxErrs, _ := parseUint(fields[2])
		rxDrop, _ := parseUint(fields[3])

		txBytes, _ := parseUint(fields[8])
		txPackets, _ := parseUint(fields[9])
		txErrs, _ := parseUint(fields[10])
		txDrop, _ := parseUint(fields[11])

		ifaceLabel := statok.Label("iface", iface)

		dirRx := statok.Label("dir", "rx")
		statok.Count("host.net.kb_total", float64(rxBytes)/1024.0, hostLabel, ifaceLabel, dirRx)
		statok.Count("host.net.packets_total", float64(rxPackets), hostLabel, ifaceLabel, dirRx)
		statok.Count("host.net.errors_total", float64(rxErrs), hostLabel, ifaceLabel, dirRx)
		statok.Count("host.net.dropped_total", float64(rxDrop), hostLabel, ifaceLabel, dirRx)

		dirTx := statok.Label("dir", "tx")
		statok.Count("host.net.kb_total", float64(txBytes)/1024.0, hostLabel, ifaceLabel, dirTx)
		statok.Count("host.net.packets_total", float64(txPackets), hostLabel, ifaceLabel, dirTx)
		statok.Count("host.net.errors_total", float64(txErrs), hostLabel, ifaceLabel, dirTx)
		statok.Count("host.net.dropped_total", float64(txDrop), hostLabel, ifaceLabel, dirTx)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan /proc/net/dev: %w", err)
	}
	return nil
}

func collectNetGopsutil(host string) error {
	stats, err := gnet.IOCounters(true)
	if err != nil {
		return fmt.Errorf("net.IOCounters: %w", err)
	}

	hostLabel := statok.Label("host", host)
	for _, s := range stats {
		if skipNetInterface(s.Name) {
			continue
		}

		ifaceLabel := statok.Label("iface", s.Name)

		dirRx := statok.Label("dir", "rx")
		statok.Count("host.net.kb_total", float64(s.BytesRecv)/1024.0, hostLabel, ifaceLabel, dirRx)
		statok.Count("host.net.packets_total", float64(s.PacketsRecv), hostLabel, ifaceLabel, dirRx)
		statok.Count("host.net.errors_total", float64(s.Errin), hostLabel, ifaceLabel, dirRx)
		statok.Count("host.net.dropped_total", float64(s.Dropin), hostLabel, ifaceLabel, dirRx)

		dirTx := statok.Label("dir", "tx")
		statok.Count("host.net.kb_total", float64(s.BytesSent)/1024.0, hostLabel, ifaceLabel, dirTx)
		statok.Count("host.net.packets_total", float64(s.PacketsSent), hostLabel, ifaceLabel, dirTx)
		statok.Count("host.net.errors_total", float64(s.Errout), hostLabel, ifaceLabel, dirTx)
		statok.Count("host.net.dropped_total", float64(s.Dropout), hostLabel, ifaceLabel, dirTx)
	}
	return nil
}

func skipNetInterface(name string) bool {
	// Loopback
	if name == "lo" {
		return true
	}

	// Docker / container veth pairs
	if strings.HasPrefix(name, "veth") {
		return true
	}

	// Docker bridge
	if name == "docker0" {
		return true
	}

	// User / Docker bridge networks like br-xxxxxxxx
	if strings.HasPrefix(name, "br-") {
		return true
	}

	// Common K8s / SDN interfaces
	if strings.HasPrefix(name, "cali") {
		return true
	}
	if strings.HasPrefix(name, "flannel.") {
		return true
	}
	if strings.HasPrefix(name, "tunl") {
		return true
	}

	return false
}
