package main

import (
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"runtime"
)

func openLocalBrowser(rawURL string) error {
	name, args, err := browserCommand(runtime.GOOS, rawURL)
	if err != nil {
		return err
	}
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
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
