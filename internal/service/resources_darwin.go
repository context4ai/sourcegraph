//go:build darwin

package service

import (
	"context"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// macOS has no /proc. These bounded, fixed-argument commands run only on the
// resource sampler, never on query handlers. RSS is current memory, not peak RSS.
func platformRSS(parent context.Context) *uint64 {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/bin/ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return nil
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(output)), 10, 64)
	if err != nil || n > math.MaxUint64/1024 {
		return nil
	}
	n *= 1024
	return &n
}

func platformHostCPU(parent context.Context, _, _ cpuCounter) *float64 {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/usr/bin/top", "-l", "2", "-s", "1", "-n", "0")
	command.Env = append(os.Environ(), "LC_ALL=C")
	output, err := command.Output()
	if err != nil {
		return nil
	}
	return parseDarwinCPU(string(output))
}

// Use the second interval sample, not top's first cumulative sample.
func parseDarwinCPU(output string) *float64 {
	var value *float64
	samples := 0
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "CPU usage:") {
			continue
		}
		samples++
		fields := strings.Fields(line)
		value = nil
		for i, f := range fields {
			if f == "idle" && i > 0 {
				idle, err := strconv.ParseFloat(strings.TrimSuffix(fields[i-1], "%"), 64)
				if err == nil && !math.IsNaN(idle) && idle >= 0 && idle <= 100 {
					n := 100 - idle
					value = &n
				}
			}
		}
	}
	if samples < 2 {
		return nil
	}
	return value
}
