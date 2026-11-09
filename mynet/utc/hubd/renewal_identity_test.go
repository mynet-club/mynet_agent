package hubd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/mynet-club/mynet_agent/mynet/utc/hub"
)

func writeIdentity(t *testing.T, confDir string, localID uint64, bootID string, pid uint64, mode os.FileMode) {
	t.Helper()
	text := []byte("identity-v1 " + strconv.FormatUint(localID, 10) + " " + bootID + " " +
		strconv.FormatUint(pid, 10) + "\n")
	if err := os.WriteFile(filepath.Join(confDir, identityFileName), text, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(confDir, identityFileName), mode); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeRotatesWhenPublishedGrantExpires(t *testing.T) {
	key, public := testKey(t)
	config := coreConfig(t, "", public)
	now := time.Now()
	writeProjectionState(t, config, 5, 1, 1, 1, uint64(now.Add(-time.Minute).Unix()))
	writeStatus(t, config.StatusPath, "RUNNING", 5, 1, "APPLIED", "POLICY_LOADED")
	reply := baseReply(t, key, 2, 2, 2, 2, now.Add(5*time.Minute).Unix())
	server := newFakeHub(t, func(_ *fakeHub, _ int, request recordedRequest) []byte {
		if request.u8(1) != 2 || request.u8(12) != 1 {
			t.Errorf("expired pair op=%d rotate=%d", request.u8(1), request.u8(12))
		}
		writeStatus(t, config.StatusPath, "RUNNING", 6, 2, "APPLIED", "POLICY_LOADED")
		return successPayload(t, key, reply)
	})
	config.HubAddress = server.listener.Addr().String()
	runtime := newRuntime(t, config)
	if err := stepOnce(t, runtime); err != nil {
		t.Fatal(err)
	}
	projection, err := hub.Parse(readFile(t, filepath.Join(config.ConfDir, "utc.conf")))
	if err != nil {
		t.Fatal(err)
	}
	if projection.ConfigGen != 6 || projection.Peers[0].PairGen != 2 {
		t.Fatalf("rotated projection gen=%d peer=%+v", projection.ConfigGen, projection.Peers[0])
	}
}

func TestRuntimeBootstrapsIdentityFromGNBFile(t *testing.T) {
	key, public := testKey(t)
	config := coreConfig(t, "", public)
	config.Identity.BootID = ""
	config.Identity.PID = 0
	writeIdentity(t, config.ConfDir, testLocalID, testBoot, 123, 0600)
	reply := baseReply(t, key, 1, 1, 1, 2, time.Now().Add(5*time.Minute).Unix())
	server := newFakeHub(t, func(_ *fakeHub, _ int, request recordedRequest) []byte {
		if request.u8(1) != 1 {
			t.Errorf("bootstrap op = %d", request.u8(1))
		}
		writeStatus(t, config.StatusPath, "RUNNING", 5, 1, "APPLIED", "POLICY_LOADED")
		return successPayload(t, key, reply)
	})
	config.HubAddress = server.listener.Addr().String()
	runtime := newRuntime(t, config)
	if err := stepOnce(t, runtime); err != nil {
		t.Fatal(err)
	}
	projection, err := hub.Parse(readFile(t, filepath.Join(config.ConfDir, "utc.conf")))
	if err != nil {
		t.Fatal(err)
	}
	if projection.BootID != testBoot || projection.PID != 123 ||
		projection.ConfigGen != 5 {
		t.Fatalf("projection header boot=%s pid=%d gen=%d",
			projection.BootID, projection.PID, projection.ConfigGen)
	}
	if runtime.identity.BootID != testBoot || runtime.identity.PID != 123 {
		t.Fatalf("runtime identity = %+v", runtime.identity)
	}
}

func TestRuntimeFallsBackToStatusIdentity(t *testing.T) {
	_, public := testKey(t)
	config := coreConfig(t, "127.0.0.1:1", public)
	config.Identity.BootID = ""
	config.Identity.PID = 0
	now := time.Now()
	writeProjectionState(t, config, 5, 1, 1, 1, uint64(now.Add(5*time.Minute).Unix()))
	writeStatus(t, config.StatusPath, "RUNNING", 5, 1, "APPLIED", "POLICY_LOADED")
	runtime := newRuntime(t, config)
	if err := runtime.load(); err != nil {
		t.Fatal(err)
	}
	if runtime.identity.BootID != testBoot || runtime.identity.PID != 123 {
		t.Fatalf("status fallback identity = %+v", runtime.identity)
	}
}

func TestRuntimePrefersIdentityFileOverStatusHeader(t *testing.T) {
	key, _ := testKey(t)
	config, _ := assignState(t, key)
	otherBoot := "0123456789abcdef0123456789abcdef"
	writeIdentity(t, config.ConfDir, testLocalID, otherBoot, 777, 0600)
	runtime := newRuntime(t, config)
	if err := runtime.load(); err != nil {
		t.Fatal(err)
	}
	if runtime.identity.BootID != otherBoot || runtime.identity.PID != 777 {
		t.Fatalf("identity file was not preferred: %+v", runtime.identity)
	}
	if runtime.configGen != 5 {
		t.Fatalf("status config_gen high water lost: %d", runtime.configGen)
	}
}

func TestRuntimeRefusesForeignIdentityFile(t *testing.T) {
	_, public := testKey(t)
	config := coreConfig(t, "", public)
	config.Identity.BootID = ""
	config.Identity.PID = 0
	writeIdentity(t, config.ConfDir, testLocalID+1, testBoot, 123, 0600)
	server := newFakeHub(t, func(*fakeHub, int, recordedRequest) []byte { return nil })
	config.HubAddress = server.listener.Addr().String()
	runtime := newRuntime(t, config)
	err := runtime.load()
	if err == nil || !errors.Is(err, errState) {
		t.Fatalf("foreign identity error = %v", err)
	}
	if server.count() != 0 {
		t.Fatal("foreign identity reached the hub")
	}
	if _, statErr := os.Stat(filepath.Join(config.ConfDir, "utc.conf")); !os.IsNotExist(statErr) {
		t.Fatal("foreign identity published a projection")
	}
}

func TestRuntimeRefusesMalformedIdentityFile(t *testing.T) {
	cases := map[string]struct {
		body string
		mode os.FileMode
	}{
		"no newline":     {"identity-v1 10 " + testBoot + " 123", 0600},
		"bad boot":       {"identity-v1 10 abcd 123\n", 0600},
		"zero pid":       {"identity-v1 10 " + testBoot + " 0\n", 0600},
		"bad magic":      {"identity-v2 10 " + testBoot + " 123\n", 0600},
		"extra row":      {"identity-v1 10 " + testBoot + " 123\nextra\n", 0600},
		"world readable": {"identity-v1 10 " + testBoot + " 123\n", 0644},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			_, public := testKey(t)
			config := coreConfig(t, "", public)
			config.Identity.BootID = ""
			config.Identity.PID = 0
			path := filepath.Join(config.ConfDir, identityFileName)
			if err := os.WriteFile(path, []byte(testCase.body), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, testCase.mode); err != nil {
				t.Fatal(err)
			}
			server := newFakeHub(t, func(*fakeHub, int, recordedRequest) []byte { return nil })
			config.HubAddress = server.listener.Addr().String()
			runtime := newRuntime(t, config)
			if err := runtime.load(); err == nil || !errors.Is(err, errState) {
				t.Fatalf("malformed identity error = %v", err)
			}
			if server.count() != 0 {
				t.Fatal("malformed identity reached the hub")
			}
		})
	}
}

func TestRuntimeFailsClosedWithoutAnyIdentity(t *testing.T) {
	_, public := testKey(t)
	config := coreConfig(t, "", public)
	config.Identity.BootID = ""
	config.Identity.PID = 0
	server := newFakeHub(t, func(*fakeHub, int, recordedRequest) []byte { return nil })
	config.HubAddress = server.listener.Addr().String()
	runtime := newRuntime(t, config)
	if err := runtime.load(); err == nil || !errors.Is(err, errState) {
		t.Fatalf("missing identity error = %v", err)
	}
	if server.count() != 0 {
		t.Fatal("missing identity reached the hub")
	}
}

func TestRuntimeRefusesMalformedIdentityMidRun(t *testing.T) {
	key, _ := testKey(t)
	config, _ := assignState(t, key)
	before := readFile(t, filepath.Join(config.ConfDir, "utc.conf"))
	path := filepath.Join(config.ConfDir, identityFileName)
	if err := os.WriteFile(path, []byte("utc.identity half written"), 0600); err != nil {
		t.Fatal(err)
	}
	server := newFakeHub(t, func(*fakeHub, int, recordedRequest) []byte { return nil })
	config.HubAddress = server.listener.Addr().String()
	runtime := newRuntime(t, config)
	if err := stepOnce(t, runtime); err == nil {
		t.Fatal("malformed identity was accepted mid-run")
	}
	if server.count() != 0 {
		t.Fatal("malformed identity reached the hub")
	}
	if !bytes.Equal(before, readFile(t, filepath.Join(config.ConfDir, "utc.conf"))) {
		t.Fatal("malformed identity rewrote utc.conf")
	}
}

func TestRuntimeRenewalWriteFailureKeepsOldProjection(t *testing.T) {
	key, _ := testKey(t)
	config, _ := assignState(t, key)
	old := readFile(t, filepath.Join(config.ConfDir, "utc.conf"))
	before, err := loadVerified(config.VerifiedPath)
	if err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(50 * time.Minute).Unix()
	reply := baseReply(t, key, 1, 1, 1, 2, later)
	server := newFakeHub(t, func(*fakeHub, int, recordedRequest) []byte {
		return successPayload(t, key, reply)
	})
	config.HubAddress = server.listener.Addr().String()
	if err := os.Chmod(config.ConfDir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(config.ConfDir, 0700) })
	runtime := newRuntime(t, config)
	if err := stepOnce(t, runtime); err == nil {
		t.Fatal("renewal publish failure was reported as success")
	}
	if !bytes.Equal(old, readFile(t, filepath.Join(config.ConfDir, "utc.conf"))) {
		t.Fatal("failed renewal overwrote the old utc.conf")
	}
	after, err := loadVerified(config.VerifiedPath)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("verified watermark changed on a failed publish: %+v", after)
	}
}

func TestRuntimeStepStopsOnContextCancelWhileWaitingApplied(t *testing.T) {
	key, public := testKey(t)
	config := coreConfig(t, "", public)
	config.ApplyTimeout = 5 * time.Second
	config.StatusPoll = 10 * time.Millisecond
	reply := baseReply(t, key, 1, 1, 1, 2, time.Now().Add(5*time.Minute).Unix())
	server := newFakeHub(t, func(*fakeHub, int, recordedRequest) []byte {
		return successPayload(t, key, reply)
	})
	config.HubAddress = server.listener.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(100*time.Millisecond, cancel)
	runtime := newRuntime(t, config)
	if err := runtime.load(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.step(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("step error = %v, want context.Canceled", err)
	}
}
