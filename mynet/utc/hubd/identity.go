package hubd

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	identityFileName = "utc.identity"
	maxIdentityBytes = 256
	zeroBootID       = "00000000000000000000000000000000"
)

// identityPath is the GNB-published boot identity. It sits beside utc.conf so
// the projection header is bound to the process that will consume it.
func (r *Runtime) identityPath() string {
	return filepath.Join(r.cfg.ConfDir, identityFileName)
}

// readIdentityFile parses "identity-v1 <local_id> <boot_id> <pid>\n". Only a
// missing file is recoverable; a malformed or foreign file fails closed so the
// runtime never publishes against a header the GNB did not write.
func readIdentityFile(path string, localID uint64) (string, uint64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > maxIdentityBytes {
		return "", 0, errors.New("utc.identity must be a bounded private file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	text := string(raw)
	if !strings.HasSuffix(text, "\n") || strings.Count(text, "\n") != 1 {
		return "", 0, errors.New("utc.identity is incomplete")
	}
	fields := strings.Fields(text)
	if len(fields) != 4 || fields[0] != "identity-v1" {
		return "", 0, errors.New("utc.identity header is invalid")
	}
	id, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return "", 0, errors.New("utc.identity local id is invalid")
	}
	if id != localID {
		return "", 0, fmt.Errorf("utc.identity belongs to local id %d", id)
	}
	if !validBootID(fields[2]) {
		return "", 0, errors.New("utc.identity boot id is invalid")
	}
	pid, err := strconv.ParseUint(fields[3], 10, 64)
	if err != nil || pid == 0 {
		return "", 0, errors.New("utc.identity pid is invalid")
	}
	return fields[2], pid, nil
}

func validBootID(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 16 && hex.EncodeToString(raw) == value
}
