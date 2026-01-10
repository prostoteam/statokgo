package core

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	gnet "github.com/shirou/gopsutil/v3/net"

	statok "github.com/prostoteam/statokgo"
)

type NetCollector struct {
	every time.Duration
	mu    sync.Mutex
	prev  map[string]netCounters
}

func NewNet(every time.Duration) *NetCollector {
	return &NetCollector{
		every: every,
		prev:  make(map[string]netCounters),
	}
}

func (c *NetCollector) ID() string { return "core.net" }

func (c *NetCollector) Every() time.Duration { return c.every }

func (c *NetCollector) Collect(_ context.Context, host string) error {
	var (
		snapshot map[string]netCounters
		err      error
	)
	if runtime.GOOS != "linux" {
		snapshot, err = readNetGopsutil()
	} else {
		snapshot, err = readNetProc()
	}
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	hostLabel := statok.Label("host", host)
	for iface, cur := range snapshot {
		prev, ok := c.prev[iface]
		c.prev[iface] = cur
		if !ok {
			continue
		}

		ifaceLabel := statok.Label("iface", iface)
		dirRx := statok.Label("dir", "rx")
		dirTx := statok.Label("dir", "tx")

		rxBytes := diffUint(prev.rxBytes, cur.rxBytes)
		if rxBytes > 0 {
			statok.Count("host.net.kb", float64(rxBytes)/1024.0, hostLabel, ifaceLabel, dirRx)
		}
		rxPackets := diffUint(prev.rxPackets, cur.rxPackets)
		if rxPackets > 0 {
			statok.Count("host.net.packets", float64(rxPackets), hostLabel, ifaceLabel, dirRx)
		}
		rxErrs := diffUint(prev.rxErrs, cur.rxErrs)
		if rxErrs > 0 {
			statok.Count("host.net.errors", float64(rxErrs), hostLabel, ifaceLabel, dirRx)
		}
		rxDrop := diffUint(prev.rxDrop, cur.rxDrop)
		if rxDrop > 0 {
			statok.Count("host.net.dropped", float64(rxDrop), hostLabel, ifaceLabel, dirRx)
		}

		txBytes := diffUint(prev.txBytes, cur.txBytes)
		if txBytes > 0 {
			statok.Count("host.net.kb", float64(txBytes)/1024.0, hostLabel, ifaceLabel, dirTx)
		}
		txPackets := diffUint(prev.txPackets, cur.txPackets)
		if txPackets > 0 {
			statok.Count("host.net.packets", float64(txPackets), hostLabel, ifaceLabel, dirTx)
		}
		txErrs := diffUint(prev.txErrs, cur.txErrs)
		if txErrs > 0 {
			statok.Count("host.net.errors", float64(txErrs), hostLabel, ifaceLabel, dirTx)
		}
		txDrop := diffUint(prev.txDrop, cur.txDrop)
		if txDrop > 0 {
			statok.Count("host.net.dropped", float64(txDrop), hostLabel, ifaceLabel, dirTx)
		}
	}

	return nil
}

type netCounters struct {
	rxBytes   uint64
	rxPackets uint64
	rxErrs    uint64
	rxDrop    uint64
	txBytes   uint64
	txPackets uint64
	txErrs    uint64
	txDrop    uint64
}

func readNetProc() (map[string]netCounters, error) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return nil, fmt.Errorf("open /proc/net/dev: %w", err)
	}
	defer f.Close()

	stats := make(map[string]netCounters)
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

		stats[iface] = netCounters{
			rxBytes:   rxBytes,
			rxPackets: rxPackets,
			rxErrs:    rxErrs,
			rxDrop:    rxDrop,
			txBytes:   txBytes,
			txPackets: txPackets,
			txErrs:    txErrs,
			txDrop:    txDrop,
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan /proc/net/dev: %w", err)
	}
	return stats, nil
}

func readNetGopsutil() (map[string]netCounters, error) {
	counters, err := gnet.IOCounters(true)
	if err != nil {
		return nil, fmt.Errorf("net.IOCounters: %w", err)
	}

	stats := make(map[string]netCounters, len(counters))
	for _, s := range counters {
		if skipNetInterface(s.Name) {
			continue
		}

		stats[s.Name] = netCounters{
			rxBytes:   s.BytesRecv,
			rxPackets: s.PacketsRecv,
			rxErrs:    s.Errin,
			rxDrop:    s.Dropin,
			txBytes:   s.BytesSent,
			txPackets: s.PacketsSent,
			txErrs:    s.Errout,
			txDrop:    s.Dropout,
		}
	}
	return stats, nil
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
