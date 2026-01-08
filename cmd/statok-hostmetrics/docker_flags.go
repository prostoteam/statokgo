package main

import (
	"flag"
	"log"
	"time"
)

type dockerFlags struct {
	Enabled       bool
	Sock          string
	Label         string
	MaxContainers int
	Concurrency   int
	Timeout       time.Duration
}

func (f *dockerFlags) Register(fs *flag.FlagSet) {
	fs.BoolVar(&f.Enabled, "docker", false, "enable Docker container CPU metrics")
	fs.StringVar(&f.Sock, "docker-sock", "", "Docker socket (default: from DOCKER_HOST or /var/run/docker.sock)")
	fs.StringVar(&f.Label, "docker-label", "service", "container label mode: service or container")
	fs.IntVar(&f.MaxContainers, "docker-max-containers", 200, "max containers to scrape per interval")
	fs.IntVar(&f.Concurrency, "docker-concurrency", 8, "concurrent Docker stats requests per interval")
	fs.DurationVar(&f.Timeout, "docker-timeout", 5*time.Second, "timeout for each Docker scrape interval")
}

func (f *dockerFlags) Collector() *dockerCPUCollector {
	if !f.Enabled {
		return nil
	}

	sock := f.Sock
	if sock == "" {
		sock = defaultDockerSock()
	}

	c, err := newDockerCPUCollector(sock, f.Label, f.MaxContainers, f.Concurrency, f.Timeout)
	if err != nil {
		log.Printf("docker: disabled: %v", err)
		return nil
	}
	return c
}
