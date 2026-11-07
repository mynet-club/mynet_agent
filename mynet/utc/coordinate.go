package utc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// CoordinatorInstall is the local, operator-installed execution descriptor for
// the coordination CLI. Node binaries, configuration directories and control
// sockets come only from this file; a plan or a remote request can never supply
// an arbitrary executable or trust root.
type CoordinatorInstall struct {
	Version        uint32        `json:"version"`
	StoreDirectory string        `json:"store_directory"`
	Nodes          []NodeInstall `json:"nodes"`
}

type NodeInstall struct {
	NodeID     uint64 `json:"node_id"`
	Instance   string `json:"instance"`
	ConfDir    string `json:"conf_dir"`
	GNBBinary  string `json:"gnb_binary"`
	UTCDSocket string `json:"utcd_socket"`
}

// LoadCoordinatorInstall reads the installed execution descriptor with the same
// private-file rules as the trust file.
func LoadCoordinatorInstall(path string) (DiskStore, map[uint64]Node, error) {
	var file CoordinatorInstall
	if err := readCanonicalPrivate(path, &file, 131072, true); err != nil {
		return DiskStore{}, nil, err
	}
	if file.Version != 1 || file.StoreDirectory == "" || !filepath.IsAbs(file.StoreDirectory) ||
		len(file.Nodes) == 0 || len(file.Nodes) > 16 {
		return DiskStore{}, nil, errors.New("UTC coordinator install requires version=1, an absolute store directory and bounded nodes")
	}
	nodes := map[uint64]Node{}
	for _, installed := range file.Nodes {
		if installed.NodeID == 0 || !token(installed.Instance) || !filepath.IsAbs(installed.ConfDir) ||
			!filepath.IsAbs(installed.GNBBinary) || !filepath.IsAbs(installed.UTCDSocket) {
			return DiskStore{}, nil, errors.New("UTC coordinator install has an invalid node entry")
		}
		if _, duplicate := nodes[installed.NodeID]; duplicate {
			return DiskStore{}, nil, errors.New("UTC coordinator install repeats a node id")
		}
		nodes[installed.NodeID] = &ExecNode{
			NodeID: installed.NodeID, Instance: installed.Instance, ConfDir: installed.ConfDir,
			GNBBinary: installed.GNBBinary, UTCDSocket: installed.UTCDSocket,
		}
	}
	return DiskStore{Directory: file.StoreDirectory}, nodes, nil
}

// LoadPlan reads a pair plan and checks its intrinsic invariants, including the
// manifest digest. Authorization is still evaluated by the Authority adapter.
func LoadPlan(path string) (Plan, error) {
	var plan Plan
	if err := readCanonicalPrivate(path, &plan, 131072, true); err != nil {
		return plan, err
	}
	if err := plan.validate(); err != nil {
		return plan, err
	}
	return plan, nil
}

// ParsePlan parses plan JSON supplied by the trusted local caller. Unknown
// fields, trailing data and invalid manifests are rejected.
func ParsePlan(raw []byte) (Plan, error) {
	var plan Plan
	if len(raw) == 0 || len(raw) > 131072 {
		return plan, errors.New("UTC plan JSON is empty or too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return plan, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return plan, errors.New("UTC plan JSON has trailing data")
	}
	if err := plan.validate(); err != nil {
		return plan, err
	}
	return plan, nil
}

// RunPlan executes or resumes one pair operation through the persistent
// coordinator. A previous journal with the same operation id is resumed by the
// coordinator itself; older generations and conflicting ids are refused.
func RunPlan(ctx context.Context, store DiskStore, nodes map[uint64]Node, authority Authority, plan Plan,
	now ...func() time.Time) (Record, error) {
	coordinator := Coordinator{Store: store, Authority: authority, Nodes: nodes, Now: firstClock(now)}
	return coordinator.Run(ctx, plan)
}

func firstClock(now []func() time.Time) func() time.Time {
	if len(now) > 0 {
		return now[0]
	}
	return nil
}

// ResumePair resumes the durably recorded operation for scope/pair without a
// new plan file. It never invents a generation and never revives an older path:
// interrupted APPLYING/VERIFYING journals compensate with a larger generation.
func ResumePair(ctx context.Context, store DiskStore, nodes map[uint64]Node, authority Authority,
	scope string, pair uint64, now ...func() time.Time) (Record, error) {
	if !token(scope) || pair == 0 {
		return Record{}, errors.New("UTC resume requires a valid scope and pair")
	}
	record, err := store.Load(scope, pair)
	if err != nil {
		return Record{}, err
	}
	if record == nil {
		return Record{}, errors.New("UTC resume has no durable operation for this pair")
	}
	coordinator := Coordinator{Store: store, Authority: authority, Nodes: nodes, Now: firstClock(now)}
	return coordinator.Run(ctx, record.Plan)
}

// CoordinateStatus reports the durable record only; it never upgrades a journal
// write into APPLIED/VERIFIED/EFFECTIVE on its own.
func CoordinateStatus(store DiskStore, scope string, pair uint64) (*Record, error) {
	if !token(scope) || pair == 0 {
		return nil, errors.New("UTC status requires a valid scope and pair")
	}
	return store.Load(scope, pair)
}

// NewGeneration derives the next operation for renewal/compensation. It keeps
// the same operation id family but always advances generation and recomputes
// the manifest, so a renewed lease can never reuse an older authorization.
func NewGeneration(previous Plan, generation, validUntil uint64, leaseRefs []string) (Plan, error) {
	if previous.OperationID == "" || generation <= previous.Generation || validUntil == 0 || len(leaseRefs) == 0 {
		return Plan{}, errors.New("UTC renewal requires a larger generation, a deadline and lease references")
	}
	next := previous
	next.OperationID = fmt.Sprintf("%s-g%d", previous.OperationID, generation)
	next.Generation = generation
	next.ValidUntil = validUntil
	next.LeaseRefs = append([]string(nil), leaseRefs...)
	next.Manifest = next.Digest()
	return next, nil
}

// WritePlan stores a plan as a bounded private file for the CLI.
func WritePlan(path string, plan Plan) error {
	if err := plan.validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
