package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	gnet "github.com/shirou/gopsutil/v3/net"

	statok "github.com/prostoteam/statokgo"
)

func main() {
	host := os.Getenv("STATOK_HOST")
	if host == "" {
		host = os.Getenv("STATOK_ENDPOINT") // backwards-compat fallback
	}
	if host == "" {
		host = "statok.dev0101.xyz"
	}
	endpoint := statok.EndpointFromHost(host)

	_, err := statok.Init(statok.Config{
		Endpoint:          endpoint,
		QueueSize:         64_000,
		MaxBatchSize:      2_000,
		MaxSeriesPerBatch: 5_000,
		FlushInterval:     2 * time.Second,
		LocalAggCounters:  true,
		ValueMode:         statok.ValueAggregationBatch,
	})
	if err != nil {
		log.Fatalf("statok: init failed: %v", err)
	}

	host, err = os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	StartCollectors(ctx, host)

	<-ctx.Done()

	flushCtx, flushCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer flushCancel()
	if client := statok.Default(); client != nil {
		_ = client.Close(flushCtx)
	}
}

// StartCollectors launches background collectors grouped by cadence.
func StartCollectors(ctx context.Context, host string) {
	go collectFast(ctx, host)
	go collectSlow(ctx, host)
}

func collectFast(ctx context.Context, host string) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	cpuUsage := newCPUUsageCollector()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			collectMem(host)
			collectNet(host)
			collectDiskIO(host)
			collectCPULoad(host)
			cpuUsage.Collect(host)
		}
	}
}

func collectSlow(ctx context.Context, host string) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			collectFS(host)
		}
	}
}

func collectMem(host string) {
	if runtime.GOOS != "linux" {
		collectMemGopsutil(host)
		return
	}

	f, err := os.Open("/proc/meminfo")
	if err != nil {
		log.Printf("collectMem: open /proc/meminfo: %v", err)
		return
	}
	defer f.Close()

	valuesKB := map[string]uint64{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		val, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		valuesKB[key] = val
	}
	if err := scanner.Err(); err != nil {
		log.Printf("collectMem: scan /proc/meminfo: %v", err)
		return
	}

	memTotal := valuesKB["MemTotal"]
	memFree := valuesKB["MemFree"]
	memAvailable := valuesKB["MemAvailable"]
	swapTotal := valuesKB["SwapTotal"]
	swapFree := valuesKB["SwapFree"]

	memUsed := uint64(0)
	if memTotal >= memAvailable {
		memUsed = memTotal - memAvailable
	}
	swapUsed := uint64(0)
	if swapTotal >= swapFree {
		swapUsed = swapTotal - swapFree
	}

	hostLabel := statok.Label("host", host)

	emitMem := func(typ string, v uint64) {
		statok.Value("host.mem.capacity_kb", float64(v),
			hostLabel,
			statok.Label("type", typ),
		)
	}

	emitMem("total", memTotal)
	emitMem("used", memUsed)
	emitMem("free", memFree)
	emitMem("available", memAvailable)

	emitSwap := func(typ string, v uint64) {
		statok.Value("host.swap.capacity_kb", float64(v),
			hostLabel,
			statok.Label("type", typ),
		)
	}

	emitSwap("total", swapTotal)
	emitSwap("used", swapUsed)
	emitSwap("free", swapFree)
}

func collectMemGopsutil(host string) {
	vm, err := mem.VirtualMemory()
	if err != nil {
		log.Printf("collectMem: VirtualMemory: %v", err)
		return
	}
	sm, err := mem.SwapMemory()
	if err != nil {
		log.Printf("collectMem: SwapMemory: %v", err)
		return
	}

	memTotalKB := vm.Total / 1024
	memFreeKB := vm.Free / 1024
	memAvailKB := vm.Available / 1024
	memUsedKB := (vm.Total - vm.Available) / 1024

	swapTotalKB := sm.Total / 1024
	swapFreeKB := sm.Free / 1024
	swapUsedKB := sm.Used / 1024

	hostLabel := statok.Label("host", host)

	emitMem := func(typ string, v uint64) {
		statok.Value("host.mem.capacity_kb", float64(v),
			hostLabel,
			statok.Label("type", typ),
		)
	}

	emitMem("total", memTotalKB)
	emitMem("used", memUsedKB)
	emitMem("free", memFreeKB)
	emitMem("available", memAvailKB)

	emitSwap := func(typ string, v uint64) {
		statok.Value("host.swap.capacity_kb", float64(v),
			hostLabel,
			statok.Label("type", typ),
		)
	}

	emitSwap("total", swapTotalKB)
	emitSwap("used", swapUsedKB)
	emitSwap("free", swapFreeKB)
}

func collectFS(host string) {
	if runtime.GOOS != "linux" {
		collectFSGopsutil(host)
		return
	}

	f, err := os.Open("/proc/self/mounts")
	if err != nil {
		log.Printf("collectFS: open /proc/self/mounts: %v", err)
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		device := fields[0]
		mount := fields[1]
		fsType := fields[2]

		if skipFSType(fsType) || skipMountpoint(mount) {
			continue
		}

		emitFSStat(host, device, mount)
	}
	if err := scanner.Err(); err != nil {
		log.Printf("collectFS: scan /proc/self/mounts: %v", err)
	}
}

func collectFSGopsutil(host string) {
	partitions, err := disk.Partitions(true)
	if err != nil {
		log.Printf("collectFS: disk.Partitions: %v", err)
		return
	}

	for _, p := range partitions {
		if skipFSType(p.Fstype) || skipMountpoint(p.Mountpoint) {
			continue
		}
		emitFSStat(host, p.Device, p.Mountpoint)
	}
}

func emitFSStat(host string, device, mount string) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(mount, &st); err != nil {
		return
	}

	totalBytes := uint64(st.Blocks) * uint64(st.Bsize)
	freeBytes := uint64(st.Bfree) * uint64(st.Bsize)
	usedBytes := uint64(0)
	if totalBytes >= freeBytes {
		usedBytes = totalBytes - freeBytes
	}

	totalInodes := uint64(st.Files)
	freeInodes := uint64(st.Ffree)
	usedInodes := uint64(0)
	if totalInodes >= freeInodes {
		usedInodes = totalInodes - freeInodes
	}

	totalKB := totalBytes / 1024
	freeKB := freeBytes / 1024
	usedKB := usedBytes / 1024

	hostLabel := statok.Label("host", host)
	mountLabel := statok.Label("mount", mount)
	deviceLabel := statok.Label("device", device)

	emitSpace := func(typ string, v uint64) {
		statok.Value("host.fs.capacity_kb", float64(v),
			hostLabel,
			mountLabel,
			deviceLabel,
			statok.Label("type", typ),
		)
	}
	emitInodes := func(typ string, v uint64) {
		statok.Value("host.fs.inodes_count", float64(v),
			hostLabel,
			mountLabel,
			deviceLabel,
			statok.Label("type", typ),
		)
	}

	emitSpace("total", totalKB)
	emitSpace("used", usedKB)
	emitSpace("free", freeKB)

	emitInodes("total", totalInodes)
	emitInodes("used", usedInodes)
	emitInodes("free", freeInodes)
}

func skipFSType(fsType string) bool {
	switch fsType {
	case "proc", "sysfs", "tmpfs", "devtmpfs", "devpts", "cgroup", "cgroup2",
		"overlay", "squashfs", "debugfs", "mqueue", "rpc_pipefs", "nsfs",
		"tracefs", "autofs", "binfmt_misc", "pstore", "fusectl", "configfs":
		return true
	default:
		return false
	}
}

func skipMountpoint(mount string) bool {
	if mount == "/proc" || mount == "/sys" || mount == "/dev" {
		return true
	}
	if strings.HasPrefix(mount, "/run") {
		return true
	}
	return false
}

func collectDiskIO(host string) {
	if runtime.GOOS != "linux" {
		collectDiskIOGopsutil(host)
		return
	}

	f, err := os.Open("/proc/diskstats")
	if err != nil {
		log.Printf("collectDiskIO: open /proc/diskstats: %v", err)
		return
	}
	defer f.Close()

	hostLabel := statok.Label("host", host)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(strings.TrimSpace(scanner.Text()))
		if len(fields) < 14 {
			continue
		}
		dev := fields[2]
		if skipDiskDevice(dev) {
			continue
		}

		readCompleted, _ := parseUint(fields[3])
		sectorsRead, _ := parseUint(fields[5])
		writeCompleted, _ := parseUint(fields[7])
		sectorsWritten, _ := parseUint(fields[9])
		timeInIOms, _ := parseUint(fields[12])

		const sectorSize = 512
		readBytes := sectorsRead * sectorSize
		writeBytes := sectorsWritten * sectorSize

		deviceLabel := statok.Label("device", dev)

		statok.Count("host.disk.io_bytes_total", float64(readBytes),
			hostLabel, deviceLabel, statok.Label("dir", "read"),
		)
		statok.Count("host.disk.io_bytes_total", float64(writeBytes),
			hostLabel, deviceLabel, statok.Label("dir", "write"),
		)

		statok.Count("host.disk.io_ops_total", float64(readCompleted),
			hostLabel, deviceLabel, statok.Label("dir", "read"),
		)
		statok.Count("host.disk.io_ops_total", float64(writeCompleted),
			hostLabel, deviceLabel, statok.Label("dir", "write"),
		)

		statok.Count("host.disk.io_time_ms_total", float64(timeInIOms),
			hostLabel, deviceLabel,
		)
	}
	if err := scanner.Err(); err != nil {
		log.Printf("collectDiskIO: scan /proc/diskstats: %v", err)
	}
}

func collectDiskIOGopsutil(host string) {
	stats, err := disk.IOCounters()
	if err != nil {
		log.Printf("collectDiskIO: IOCounters: %v", err)
		return
	}

	hostLabel := statok.Label("host", host)
	for dev, s := range stats {
		if skipDiskDevice(dev) {
			continue
		}

		deviceLabel := statok.Label("device", dev)

		statok.Count("host.disk.io_bytes_total", float64(s.ReadBytes),
			hostLabel, deviceLabel, statok.Label("dir", "read"),
		)
		statok.Count("host.disk.io_bytes_total", float64(s.WriteBytes),
			hostLabel, deviceLabel, statok.Label("dir", "write"),
		)

		statok.Count("host.disk.io_ops_total", float64(s.ReadCount),
			hostLabel, deviceLabel, statok.Label("dir", "read"),
		)
		statok.Count("host.disk.io_ops_total", float64(s.WriteCount),
			hostLabel, deviceLabel, statok.Label("dir", "write"),
		)

		if s.IoTime > 0 {
			statok.Count("host.disk.io_time_ms_total", float64(s.IoTime),
				hostLabel, deviceLabel,
			)
		}
	}
}

func skipDiskDevice(dev string) bool {
	if strings.HasPrefix(dev, "loop") || strings.HasPrefix(dev, "ram") {
		return true
	}
	if strings.HasPrefix(dev, "dm-") {
		return true
	}
	if strings.HasPrefix(dev, "sd") || strings.HasPrefix(dev, "vd") || strings.HasPrefix(dev, "xvd") {
		if hasTrailingDigit(dev) {
			return true
		}
	}
	if strings.HasPrefix(dev, "nvme") && strings.Contains(dev, "p") {
		return true
	}
	return false
}

func hasTrailingDigit(s string) bool {
	if s == "" {
		return false
	}
	last := s[len(s)-1]
	return last >= '0' && last <= '9'
}

func collectCPULoad(host string) {
	hostLabel := statok.Label("host", host)

	if runtime.GOOS != "linux" {
		avg, err := load.Avg()
		if err != nil {
			log.Printf("collectCPULoad: load.Avg: %v", err)
			return
		}
		statok.Value("host.cpu.load1", avg.Load1, hostLabel)
		statok.Value("host.cpu.load5", avg.Load5, hostLabel)
		statok.Value("host.cpu.load15", avg.Load15, hostLabel)
		return
	}

	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		log.Printf("collectCPULoad: read /proc/loadavg: %v", err)
		return
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return
	}
	load1, err1 := strconv.ParseFloat(fields[0], 64)
	load5, err2 := strconv.ParseFloat(fields[1], 64)
	load15, err3 := strconv.ParseFloat(fields[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return
	}
	statok.Value("host.cpu.load1", load1, hostLabel)
	statok.Value("host.cpu.load5", load5, hostLabel)
	statok.Value("host.cpu.load15", load15, hostLabel)
}

type cpuTimes struct {
	user      uint64
	nice      uint64
	system    uint64
	idle      uint64
	iowait    uint64
	irq       uint64
	softirq   uint64
	steal     uint64
	guest     uint64
	guestNice uint64
}

type cpuUsageCollector struct {
	mu   sync.Mutex
	prev map[string]cpuTimes
}

func newCPUUsageCollector() *cpuUsageCollector {
	return &cpuUsageCollector{
		prev: make(map[string]cpuTimes),
	}
}

func (c *cpuUsageCollector) Collect(host string) {
	snapshot, err := readCPUTimes()
	if err != nil {
		log.Printf("cpuUsage: %v", err)
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	hostLabel := statok.Label("host", host)

	for name, cur := range snapshot {
		prev, ok := c.prev[name]
		c.prev[name] = cur
		if !ok {
			continue
		}

		deltaUser := diff(prev.user, cur.user)
		deltaNice := diff(prev.nice, cur.nice)
		deltaSystem := diff(prev.system, cur.system)
		deltaIdle := diff(prev.idle, cur.idle)
		deltaIOWait := diff(prev.iowait, cur.iowait)
		deltaIRQ := diff(prev.irq, cur.irq)
		deltaSoftIRQ := diff(prev.softirq, cur.softirq)
		deltaSteal := diff(prev.steal, cur.steal)

		deltaTotal := deltaUser + deltaNice + deltaSystem + deltaIdle +
			deltaIOWait + deltaIRQ + deltaSoftIRQ + deltaSteal
		if deltaTotal == 0 {
			continue
		}

		cpuLabel := statok.Label("cpu", normalizeCPUName(name))

		emit := func(mode string, delta uint64) {
			if delta == 0 {
				return
			}
			pct := 100.0 * float64(delta) / float64(deltaTotal)
			statok.Value("host.cpu.usage_pct", pct,
				hostLabel, cpuLabel, statok.Label("mode", mode),
			)
		}

		emit("user", deltaUser)
		emit("nice", deltaNice)
		emit("system", deltaSystem)
		emit("idle", deltaIdle)
		emit("iowait", deltaIOWait)
		emit("irq", deltaIRQ)
		emit("softirq", deltaSoftIRQ)
		emit("steal", deltaSteal)
	}
}

func diff(a, b uint64) uint64 {
	if b >= a {
		return b - a
	}
	return 0
}

func normalizeCPUName(raw string) string {
	if raw == "cpu" {
		return "all"
	}
	return strings.TrimPrefix(raw, "cpu")
}

func readCPUTimes() (map[string]cpuTimes, error) {
	if runtime.GOOS != "linux" {
		return readCPUTimesGopsutil()
	}
	return readCPUTimesProc()
}

func readCPUTimesProc() (map[string]cpuTimes, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	result := make(map[string]cpuTimes)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		name := fields[0]

		var vals [10]uint64
		for i := 0; i < len(vals) && 1+i < len(fields); i++ {
			v, err := parseUint(fields[1+i])
			if err != nil {
				v = 0
			}
			vals[i] = v
		}

		result[name] = cpuTimes{
			user:      vals[0],
			nice:      vals[1],
			system:    vals[2],
			idle:      vals[3],
			iowait:    vals[4],
			irq:       vals[5],
			softirq:   vals[6],
			steal:     vals[7],
			guest:     vals[8],
			guestNice: vals[9],
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, errors.New("no cpu lines in /proc/stat")
	}
	return result, nil
}

func readCPUTimesGopsutil() (map[string]cpuTimes, error) {
	per, err := cpu.Times(true)
	if err != nil {
		return nil, err
	}
	if len(per) == 0 {
		return nil, errors.New("cpu: no times returned")
	}

	result := make(map[string]cpuTimes, len(per)+1)
	agg := cpuTimes{}

	scale := func(v float64) uint64 {
		if v < 0 {
			return 0
		}
		return uint64(v * 100)
	}

	for i, st := range per {
		ct := cpuTimes{
			user:      scale(st.User),
			nice:      scale(st.Nice),
			system:    scale(st.System),
			idle:      scale(st.Idle),
			iowait:    scale(st.Iowait),
			irq:       scale(st.Irq),
			softirq:   scale(st.Softirq),
			steal:     scale(st.Steal),
			guest:     scale(st.Guest),
			guestNice: scale(st.GuestNice),
		}
		name := fmt.Sprintf("cpu%d", i)
		result[name] = ct

		agg.user += ct.user
		agg.nice += ct.nice
		agg.system += ct.system
		agg.idle += ct.idle
		agg.iowait += ct.iowait
		agg.irq += ct.irq
		agg.softirq += ct.softirq
		agg.steal += ct.steal
		agg.guest += ct.guest
		agg.guestNice += ct.guestNice
	}

	result["cpu"] = agg
	return result, nil
}

func collectNet(host string) {
	if runtime.GOOS != "linux" {
		collectNetGopsutil(host)
		return
	}

	f, err := os.Open("/proc/net/dev")
	if err != nil {
		log.Printf("collectNet: open /proc/net/dev: %v", err)
		return
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
		// 🔽 NEW: skip noisy / virtual interfaces
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
		statok.Count("host.net.bytes_total", float64(rxBytes), hostLabel, ifaceLabel, dirRx)
		statok.Count("host.net.packets_total", float64(rxPackets), hostLabel, ifaceLabel, dirRx)
		statok.Count("host.net.errors_total", float64(rxErrs), hostLabel, ifaceLabel, dirRx)
		statok.Count("host.net.dropped_total", float64(rxDrop), hostLabel, ifaceLabel, dirRx)

		dirTx := statok.Label("dir", "tx")
		statok.Count("host.net.bytes_total", float64(txBytes), hostLabel, ifaceLabel, dirTx)
		statok.Count("host.net.packets_total", float64(txPackets), hostLabel, ifaceLabel, dirTx)
		statok.Count("host.net.errors_total", float64(txErrs), hostLabel, ifaceLabel, dirTx)
		statok.Count("host.net.dropped_total", float64(txDrop), hostLabel, ifaceLabel, dirTx)
	}
	if err := scanner.Err(); err != nil {
		log.Printf("collectNet: scan /proc/net/dev: %v", err)
	}
}

func collectNetGopsutil(host string) {
	stats, err := gnet.IOCounters(true)
	if err != nil {
		log.Printf("collectNet: IOCounters: %v", err)
		return
	}

	hostLabel := statok.Label("host", host)
	for _, s := range stats {
		// 🔽 NEW: skip noisy / virtual interfaces
		if skipNetInterface(s.Name) {
			continue
		}

		ifaceLabel := statok.Label("iface", s.Name)

		dirRx := statok.Label("dir", "rx")
		statok.Count("host.net.bytes_total", float64(s.BytesRecv), hostLabel, ifaceLabel, dirRx)
		statok.Count("host.net.packets_total", float64(s.PacketsRecv), hostLabel, ifaceLabel, dirRx)
		statok.Count("host.net.errors_total", float64(s.Errin), hostLabel, ifaceLabel, dirRx)
		statok.Count("host.net.dropped_total", float64(s.Dropin), hostLabel, ifaceLabel, dirRx)

		dirTx := statok.Label("dir", "tx")
		statok.Count("host.net.bytes_total", float64(s.BytesSent), hostLabel, ifaceLabel, dirTx)
		statok.Count("host.net.packets_total", float64(s.PacketsSent), hostLabel, ifaceLabel, dirTx)
		statok.Count("host.net.errors_total", float64(s.Errout), hostLabel, ifaceLabel, dirTx)
		statok.Count("host.net.dropped_total", float64(s.Dropout), hostLabel, ifaceLabel, dirTx)
	}
}

func parseUint(s string) (uint64, error) {
	return strconv.ParseUint(strings.TrimSpace(s), 10, 64)
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
