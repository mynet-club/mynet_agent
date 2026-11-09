package hub

import (
	"bytes"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func multiResponse(pairID, seq uint64, address string, port uint16) Response {
	var manifest [32]byte
	manifest[0] = byte(seq)
	manifest[31] = 0xaa
	return Response{
		Grant: Grant{
			Scope: testScope, PairID: pairID, PairGen: seq, LeaseEpoch: seq + 100,
			PlacementEpoch: seq, ValidUntil: 200 + seq, TargetBindingDigest: manifest,
		},
		Binding: Binding{
			Scope: testScope, PairID: pairID, PairGen: seq, PlacementEpoch: seq,
			Port: port, Family: 4, Address: address, Kind: bindingKindAS,
		},
	}
}

func TestBuildProjectionMultiOrdersAndMapsPeers(t *testing.T) {
	identity := testIdentity()
	newer := multiResponse(31, 2, "203.0.113.8", 9444)
	older := multiResponse(30, 1, "203.0.113.7", 9443)

	projection, text, err := BuildProjectionMulti(identity, []PeerResponse{
		{PeerID: 21, Response: newer},
		{PeerID: 20, Response: older},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Peers) != 2 ||
		projection.Peers[0].PeerID != 20 || projection.Peers[1].PeerID != 21 {
		t.Fatalf("peers are not ordered by peer id: %+v", projection.Peers)
	}
	first := projection.Peers[0]
	if first.PairID != 30 || first.PairGen != 1 || first.LeaseEpoch != 101 ||
		first.PlacementEpoch != 1 || first.ExpiresAt != 201 ||
		first.Port != 9443 || first.Family != 4 || first.Mode != 1 {
		t.Fatalf("first peer did not map its reply: %+v", first)
	}
	if first.Manifest != hex.EncodeToString(older.Grant.TargetBindingDigest[:]) {
		t.Fatalf("first peer manifest = %s, want the grant digest", first.Manifest)
	}
	second := projection.Peers[1]
	if second.PairID != 31 || second.Port != 9444 ||
		second.Manifest != hex.EncodeToString(newer.Grant.TargetBindingDigest[:]) {
		t.Fatalf("second peer did not map its reply: %+v", second)
	}

	parsed, err := Parse(text)
	if err != nil {
		t.Fatalf("multi projection is not canonical: %v\n%s", err, text)
	}
	if !reflect.DeepEqual(parsed.Peers, projection.Peers) {
		t.Fatalf("parsed peers differ from returned projection: %+v", parsed.Peers)
	}
	lines := strings.Split(strings.TrimSuffix(string(text), "\n"), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[1], "peer 20 30 ") ||
		!strings.HasPrefix(lines[2], "peer 21 31 ") {
		t.Fatalf("unexpected projection rows:\n%s", text)
	}
}

func TestBuildProjectionMultiRejectsDuplicatePair(t *testing.T) {
	identity := testIdentity()
	_, _, err := BuildProjectionMulti(identity, []PeerResponse{
		{PeerID: 20, Response: multiResponse(30, 1, "203.0.113.7", 9443)},
		{PeerID: 21, Response: multiResponse(30, 2, "203.0.113.8", 9444)},
	})
	if err == nil {
		t.Fatal("accepted a duplicate pair id")
	}
}

func TestBuildProjectionMultiRejectsDuplicatePeer(t *testing.T) {
	identity := testIdentity()
	_, _, err := BuildProjectionMulti(identity, []PeerResponse{
		{PeerID: 20, Response: multiResponse(30, 1, "203.0.113.7", 9443)},
		{PeerID: 20, Response: multiResponse(31, 2, "203.0.113.8", 9444)},
	})
	if err == nil {
		t.Fatal("accepted a duplicate peer id")
	}
}

func TestBuildProjectionMultiRejectsTooManyPeers(t *testing.T) {
	identity := testIdentity()
	responses := make([]PeerResponse, maxPeers+1)
	for i := range responses {
		responses[i] = PeerResponse{
			PeerID:   uint64(1000 + i),
			Response: multiResponse(uint64(2000+i), uint64(i+1), "203.0.113.7", 9443),
		}
	}
	if _, _, err := BuildProjectionMulti(identity, responses); err == nil {
		t.Fatalf("accepted %d peers", len(responses))
	}
}

func TestBuildProjectionMultiRejectsAD(t *testing.T) {
	identity := testIdentity()
	ad := multiResponse(30, 1, "", 0)
	ad.Binding.Kind = bindingKindAD
	if _, _, err := BuildProjectionMulti(identity, []PeerResponse{{PeerID: 20, Response: ad}}); !errors.Is(err, ErrProjectionUnsupported) {
		t.Fatalf("A-D error = %v, want ErrProjectionUnsupported", err)
	}
}

func TestBuildProjectionMultiRejectsEmpty(t *testing.T) {
	if _, _, err := BuildProjectionMulti(testIdentity(), nil); err == nil {
		t.Fatal("accepted an empty peer set")
	}
}

func TestBuildProjectionMultiSingleMatchesBuildProjection(t *testing.T) {
	identity := testIdentity()
	response := multiResponse(30, 1, "203.0.113.7", 9443)

	wantProjection, wantText, err := BuildProjection(identity, response)
	if err != nil {
		t.Fatal(err)
	}
	gotProjection, gotText, err := BuildProjectionMulti(identity,
		[]PeerResponse{{PeerID: identity.PeerID, Response: response}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotText, wantText) {
		t.Fatalf("single-peer multi projection differs:\ngot  %s\nwant %s", gotText, wantText)
	}
	if !reflect.DeepEqual(gotProjection, wantProjection) {
		t.Fatalf("single-peer projection differs: %+v", gotProjection)
	}
}

func TestMergeProjections(t *testing.T) {
	first := sampleProjection()
	second := sampleProjection()
	second.Peers[0].PeerID = 21
	second.Peers[0].PairID = 31

	merged, text, err := MergeProjections([]Projection{second, first})
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Peers) != 2 || merged.Peers[0].PeerID != 20 || merged.Peers[1].PeerID != 21 {
		t.Fatalf("merged peers are not ordered: %+v", merged.Peers)
	}
	if _, err := Parse(text); err != nil {
		t.Fatalf("merged projection is not canonical: %v", err)
	}

	mismatched := second
	mismatched.ConfigGen++
	if _, _, err := MergeProjections([]Projection{first, mismatched}); err == nil {
		t.Fatal("accepted projections with differing headers")
	}
}
