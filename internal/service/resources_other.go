//go:build !darwin

package service

import "context"

func platformRSS(context.Context) *uint64 { return linuxProcessRSS() }
func platformHostCPU(_ context.Context, before, after cpuCounter) *float64 {
	return cpuPercentage(before, after)
}
