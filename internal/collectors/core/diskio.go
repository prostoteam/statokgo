package core

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/disk"

	statok "github.com/prostoteam/statokgo"
)

type DiskIOCollector struct {
	every time.Duration
}

func NewDiskIO(every time.Duration) *DiskIOCollector {
	return &DiskIOCollector{every: every}
}

func (c *DiskIOCollector) ID() string { return "core.diskio" }

func (c *DiskIOCollector) Every() time.Duration { return c.every }

func (c *DiskIOCollector) Collect(_ context.Context, host string) error {
	if runtime.GOOS != "linux" {
		return collectDiskIOGopsutil(host)
	}
	return collectDiskIOProc(host)
}

func collectDiskIOProc(host string) error {
	f, err := os.Open("/proc/diskstats")
	if err != nil {
		return fmt.Errorf("open /proc/diskstats: %w", err)
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

		statok.Count("host.disk.io_kb_total", float64(readBytes)/1024.0,
			hostLabel, deviceLabel, statok.Label("dir", "read"),
		)
		statok.Count("host.disk.io_kb_total", float64(writeBytes)/1024.0,
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
		return fmt.Errorf("scan /proc/diskstats: %w", err)
	}
	return nil
}

func collectDiskIOGopsutil(host string) error {
	stats, err := disk.IOCounters()
	if err != nil {
		return fmt.Errorf("disk.IOCounters: %w", err)
	}

	hostLabel := statok.Label("host", host)
	for dev, s := range stats {
		if skipDiskDevice(dev) {
			continue
		}

		deviceLabel := statok.Label("device", dev)

		statok.Count("host.disk.io_kb_total", float64(s.ReadBytes)/1024.0,
			hostLabel, deviceLabel, statok.Label("dir", "read"),
		)
		statok.Count("host.disk.io_kb_total", float64(s.WriteBytes)/1024.0,
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
	return nil
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
