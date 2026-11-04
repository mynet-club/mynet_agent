package utc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type DiskStore struct{ Directory string }
type diskEnvelope struct {
	Version uint32 `json:"version"`
	Record  Record `json:"record"`
	Digest  string `json:"digest"`
}

func (s DiskStore) path(scope string, pair uint64, suffix string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", scope, pair)))
	return filepath.Join(s.Directory, hex.EncodeToString(sum[:])+suffix)
}
func (s DiskStore) Load(scope string, pair uint64) (*Record, error) {
	path := s.path(scope, pair, ".json")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 131072 {
		return nil, fmt.Errorf("UTC coordinator journal is not a bounded private file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope diskEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("UTC coordinator journal has trailing data")
	}
	content, err := json.Marshal(envelope.Record)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(content)
	r := envelope.Record
	if envelope.Version != 1 || envelope.Digest != hex.EncodeToString(sum[:]) || r.Plan.Scope != scope || r.Plan.PairID != pair || r.Plan.validate() != nil || r.Watermark < r.Plan.Generation {
		return nil, fmt.Errorf("UTC coordinator journal integrity/scope failure")
	}
	switch r.Phase {
	case "INTENT", "PREPARED", "APPLYING", "VERIFYING", "EFFECTIVE", "FAILED", "COMPENSATING", "BLOCKED", "BLOCK_EXPECTED":
	default:
		return nil, fmt.Errorf("unknown UTC coordinator phase")
	}
	if r.BlockPlan != nil {
		p := r.BlockPlan
		if p.validate() != nil || p.Scope != scope || p.PairID != pair || p.OperationID != r.Plan.OperationID || p.Generation != r.Watermark || p.Generation <= r.Plan.Generation {
			return nil, fmt.Errorf("invalid UTC compensation journal")
		}
	}
	return &r, nil
}
func (s DiskStore) Save(record Record) error {
	if s.Directory == "" || !filepath.IsAbs(s.Directory) {
		return fmt.Errorf("UTC coordinator needs an absolute state directory")
	}
	if err := os.MkdirAll(s.Directory, 0700); err != nil {
		return err
	}
	content, err := json.Marshal(record)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(content)
	raw, err := json.Marshal(diskEnvelope{Version: 1, Record: record, Digest: hex.EncodeToString(sum[:])})
	if err != nil {
		return err
	}
	if len(raw) > 131072 {
		return fmt.Errorf("UTC coordinator journal capacity exceeded")
	}
	return writePrivate(pathForRecord(s, record), raw)
}

func pathForRecord(s DiskStore, record Record) string {
	return s.path(record.Plan.Scope, record.Plan.PairID, ".json")
}

func writePrivate(path string, raw []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".utc-private-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(raw)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(temp, path); err != nil {
		return err
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		directory, err := os.Open(dir)
		if err != nil {
			return err
		}
		err = directory.Sync()
		closeErr := directory.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	return nil
}
