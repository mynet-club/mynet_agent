//go:build windows

package utc

import (
	"context"
	"fmt"
)

func (s DiskStore) Lock(ctx context.Context, scope string, pair uint64) (func(), error) {
	return nil, fmt.Errorf("UTC coordinator durable locking is unsupported on Windows")
}
