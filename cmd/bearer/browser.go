package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func openLocalBrowser(rawURL string) error {
	name, args, err := browserCommand(runtime.GOOS, rawURL)
	if err != nil {
		return err
	}
	cmd := exec.Command(name, args...)
	cmd.Env = browserEnvironment(os.Environ())
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func browserEnvironment(environ []string) []string {
	clean := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, ok := strings.Cut(entry, "=")
		if ok && (strings.EqualFold(name, "OSANWE_SECRET") || strings.EqualFold(name, "OSANWE_RECEIPT")) {
			continue
		}
		clean = append(clean, entry)
	}
	return clean
}

func browserCommand(goos, rawURL string) (string, []string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return "", nil, fmt.Errorf("bearer: browser URL must be an absolute local http URL")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", nil, fmt.Errorf("bearer: refusing to open a non-loopback browser URL")
	}

	switch goos {
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", rawURL}, nil
	case "darwin":
		return "open", []string{rawURL}, nil
	case "linux":
		return "xdg-open", []string{rawURL}, nil
	default:
		return "", nil, fmt.Errorf("bearer: automatic browser opening is unsupported on %s", goos)
	}
}
