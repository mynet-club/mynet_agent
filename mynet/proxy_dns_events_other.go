//go:build !darwin

package mynet

import "context"

func hookDNSChangeEvents(ctx context.Context) <-chan struct{} { return nil }
func effectiveHookDNSServers() ([]string, error)              { return nil, nil }

func hookSystemDNSOps() systemDNSOps { return nativeSystemDNSOps() }
