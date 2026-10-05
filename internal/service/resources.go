package service

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Host and API-process utilization have different denominators. The process
// value counts one fully used CPU as 100%; it excludes the Zoekt child service.
type ResourceSample struct {
	SampledAt         time.Time `json:"sampled_at" bson:"sampled_at"`
	IntervalSeconds   float64   `json:"interval_seconds" bson:"interval_seconds"`
	LogicalCPUs       int       `json:"logical_cpus" bson:"logical_cpus"`
	HostCPUPercent    *float64  `json:"host_cpu_percent" bson:"host_cpu_percent"`
	ProcessCPUPercent *float64  `json:"process_cpu_percent" bson:"process_cpu_percent"`
	ProcessRSSBytes   *uint64   `json:"process_rss_bytes" bson:"process_rss_bytes"`
}

// Resource samples are immutable once published. Each external snapshot gets
// independent optional values so a caller cannot change a cached measurement.
func copyResources(sample ResourceSample) ResourceSample {
	if sample.HostCPUPercent != nil {
		v := *sample.HostCPUPercent
		sample.HostCPUPercent = &v
	}
	if sample.ProcessCPUPercent != nil {
		v := *sample.ProcessCPUPercent
		sample.ProcessCPUPercent = &v
	}
	if sample.ProcessRSSBytes != nil {
		v := *sample.ProcessRSSBytes
		sample.ProcessRSSBytes = &v
	}
	return sample
}

type cpuCounter struct {
	total, idle uint64
	valid       bool
}

func parseHostCPU(data string) cpuCounter {
	f := strings.Fields(strings.SplitN(data, "\n", 2)[0])
	if len(f) < 5 || f[0] != "cpu" {
		return cpuCounter{}
	}
	v := cpuCounter{valid: true}
	// guest and guest_nice are already included in user and nice.
	for i := 1; i < len(f) && i <= 8; i++ {
		n, e := strconv.ParseUint(f[i], 10, 64)
		if e != nil {
			return cpuCounter{}
		}
		v.total += n
		if i == 4 || i == 5 {
			v.idle += n
		}
	}
	return v
}
func hostCPU() cpuCounter {
	b, e := os.ReadFile("/proc/stat")
	if e != nil {
		return cpuCounter{}
	}
	return parseHostCPU(string(b))
}
func cpuPercentage(before, after cpuCounter) *float64 {
	if !before.valid || !after.valid || after.total <= before.total || after.idle < before.idle {
		return nil
	}
	total, idle := after.total-before.total, after.idle-before.idle
	if idle > total {
		return nil
	}
	v := 100 * float64(total-idle) / float64(total)
	return &v
}
func processCPU() (float64, bool) {
	var u syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &u) != nil {
		return 0, false
	}
	return float64(u.Utime.Sec+u.Stime.Sec) + float64(u.Utime.Usec+u.Stime.Usec)/1e6, true
}
func linuxProcessRSS() *uint64 {
	b, e := os.ReadFile("/proc/self/status")
	if e != nil {
		return nil
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[0] == "VmRSS:" && f[2] == "kB" {
			n, e := strconv.ParseUint(f[1], 10, 64)
			if e == nil {
				n *= 1024
				return &n
			}
		}
	}
	return nil
}
func (s *Service) sampleResources(ctx context.Context) {
	previous, host := time.Now(), hostCPU()
	process, valid := processCPU()
	initial := ResourceSample{SampledAt: previous.UTC(), LogicalCPUs: runtime.NumCPU(), ProcessRSSBytes: platformRSS(ctx)}
	s.metrics.mu.Lock()
	s.metrics.resources = initial
	s.metrics.mu.Unlock()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			newHost := hostCPU()
			hostPercent := platformHostCPU(ctx, host, newHost)
			newProcess, ok := processCPU()
			now := time.Now()
			interval := now.Sub(previous).Seconds()
			sample := ResourceSample{SampledAt: now.UTC(), IntervalSeconds: interval, LogicalCPUs: runtime.NumCPU(), HostCPUPercent: hostPercent, ProcessRSSBytes: platformRSS(ctx)}
			if valid && ok && interval > 0 && newProcess >= process {
				v := 100 * (newProcess - process) / interval
				sample.ProcessCPUPercent = &v
			}
			s.metrics.mu.Lock()
			s.metrics.resources = sample
			s.metrics.mu.Unlock()
			previous, host, process, valid = now, newHost, newProcess, ok
		}
	}
}
