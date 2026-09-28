package config

import (
	"fmt"
	"net"
	"strings"
)

// ServerConfig controls the local WebUI listener and the host aliases it
// accepts in HTTP Host and Origin headers. The listener itself remains
// loopback-only.
type ServerConfig struct {
	Port  int      `json:"port" default:"10998"`
	Hosts []string `json:"hosts"`
}

func (c *ServerConfig) Normalize() {
	ApplyDefaults(c)
	if c.Hosts == nil {
		c.Hosts = []string{"127.0.0.1", "localhost"}
	}

	seen := make(map[string]struct{}, len(c.Hosts))
	hosts := make([]string, 0, len(c.Hosts))
	for _, host := range c.Hosts {
		host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			host = host[1 : len(host)-1]
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}
	c.Hosts = hosts
}

func (c ServerConfig) validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("server.port must be 1..65535, got %d", c.Port)
	}
	if len(c.Hosts) == 0 {
		return fmt.Errorf("server.hosts must contain at least one host")
	}
	for _, host := range c.Hosts {
		if !validHostAlias(host) {
			return fmt.Errorf("server.hosts contains invalid host %q", host)
		}
	}
	return nil
}

func validHostAlias(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) == 0 || len(host) > 253 || strings.Contains(host, ":") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-') {
				return false
			}
		}
	}
	return true
}
