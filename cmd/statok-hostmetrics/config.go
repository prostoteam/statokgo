package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/prostoteam/statokgo/internal/collectors/mongo"
	"github.com/prostoteam/statokgo/internal/integrations"
)

const (
	configEnvVar       = "STATOK_CONFIG"
	defaultConfigName  = "hostmetrics.yaml"
	mongoRetryInterval = time.Minute
)

type fileConfig struct {
	Agent        agentConfig        `yaml:"agent"`
	Integrations integrationsConfig `yaml:"integrations"`
}

type agentConfig struct {
	Workload *string `yaml:"workload"`
}

type integrationsConfig struct {
	Mongo mongoConfig `yaml:"mongo"`
}

type mongoConfig struct {
	Enabled   bool                  `yaml:"enabled"`
	Instances []mongoInstanceConfig `yaml:"instances"`
}

type mongoInstanceConfig struct {
	URI string `yaml:"uri"`
}

type runtimeConfig struct {
	Workload       string
	MongoEnabled   bool
	MongoInstances []mongo.Instance
}

type configSource struct {
	Path     string
	Explicit bool
}

func resolveConfigSource(flagPath string) (configSource, []string, error) {
	if path := strings.TrimSpace(flagPath); path != "" {
		path = expandHome(path)
		return configSource{Path: path, Explicit: true}, []string{path}, nil
	}
	if envPath := strings.TrimSpace(os.Getenv(configEnvVar)); envPath != "" {
		envPath = expandHome(envPath)
		return configSource{Path: envPath, Explicit: true}, []string{envPath}, nil
	}
	paths := defaultConfigPaths()
	for _, path := range paths {
		if fileExists(path) {
			return configSource{Path: path}, paths, nil
		}
	}
	return configSource{}, paths, nil
}

func defaultConfigPaths() []string {
	var paths []string
	userPath := userConfigPath()
	systemPath := filepath.Join("/etc", "statok", defaultConfigName)
	if os.Geteuid() == 0 {
		if systemPath != "" {
			paths = append(paths, systemPath)
		}
		if userPath != "" {
			paths = append(paths, userPath)
		}
		return paths
	}
	if userPath != "" {
		paths = append(paths, userPath)
	}
	if systemPath != "" {
		paths = append(paths, systemPath)
	}
	return paths
}

func userConfigPath() string {
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, "statok", defaultConfigName)
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".config", "statok", defaultConfigName)
}

func expandHome(path string) string {
	if path == "" {
		return path
	}
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}

func loadFileConfig(source configSource) (*fileConfig, error) {
	if strings.TrimSpace(source.Path) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(source.Path)
	if err != nil {
		if os.IsNotExist(err) && !source.Explicit {
			return nil, nil
		}
		return nil, fmt.Errorf("read config %s: %w", source.Path, err)
	}
	cfg := &fileConfig{}
	if len(strings.TrimSpace(string(data))) == 0 {
		return cfg, nil
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", source.Path, err)
	}
	expandEnvStrings(cfg)
	return cfg, nil
}

func resolveRuntimeConfig(cfg *fileConfig, workloadFlag string, workloadFlagSet bool) (runtimeConfig, error) {
	workload, err := resolveWorkload(cfg, workloadFlag, workloadFlagSet)
	if err != nil {
		return runtimeConfig{}, err
	}
	out := runtimeConfig{Workload: workload}
	if cfg == nil || !cfg.Integrations.Mongo.Enabled {
		return out, nil
	}
	if len(cfg.Integrations.Mongo.Instances) == 0 {
		return runtimeConfig{}, errors.New("mongo integration enabled but no instances configured")
	}
	instances := make([]mongo.Instance, 0, len(cfg.Integrations.Mongo.Instances))
	for i, inst := range cfg.Integrations.Mongo.Instances {
		uri := strings.TrimSpace(inst.URI)
		if uri == "" {
			return runtimeConfig{}, fmt.Errorf("mongo.instances[%d].uri is empty", i)
		}
		label, err := integrations.InstanceLabelFromURI(uri)
		if err != nil {
			return runtimeConfig{}, fmt.Errorf("mongo.instances[%d]: %w", i, err)
		}
		instances = append(instances, mongo.Instance{
			URI:   uri,
			Label: label,
		})
	}
	out.MongoEnabled = true
	out.MongoInstances = instances
	return out, nil
}

func resolveWorkload(cfg *fileConfig, workloadFlag string, workloadFlagSet bool) (string, error) {
	if cfg != nil && cfg.Agent.Workload != nil {
		workload := strings.TrimSpace(*cfg.Agent.Workload)
		if workload == "" {
			return "", errors.New("statok: workload is empty")
		}
		return workload, nil
	}
	workload := strings.TrimSpace(workloadFlag)
	if workloadFlagSet {
		if workload == "" {
			return "", errors.New("statok: workload is empty")
		}
		return workload, nil
	}
	if workload != "" {
		return workload, nil
	}
	host, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("statok: workload not set and hostname lookup failed: %w", err)
	}
	workload = strings.TrimSpace(host)
	if workload == "" {
		return "", errors.New("statok: workload not set and hostname is empty")
	}
	return workload, nil
}

func expandEnvStrings(v any) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return
	}
	expandEnvValue(rv.Elem())
}

func expandEnvValue(v reflect.Value) {
	if !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() {
			v.SetString(os.ExpandEnv(v.String()))
		}
	case reflect.Ptr:
		if !v.IsNil() {
			expandEnvValue(v.Elem())
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			field := v.Field(i)
			if !field.CanSet() && field.Kind() == reflect.String {
				continue
			}
			expandEnvValue(field)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			expandEnvValue(v.Index(i))
		}
	}
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}
