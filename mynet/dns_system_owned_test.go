package mynet

import (
	"errors"
	"reflect"
	"testing"
)

func fakeSystemDNS(t *testing.T) (systemDNSOps, map[string][]string, *int) {
	t.Helper()
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_DATA_HOME", "")
	services := map[string][]string{"Wi-Fi": nil, "Ethernet": {"192.0.2.53"}}
	writes := new(int)
	ops := systemDNSOps{
		list: func() ([]string, error) { return []string{"Wi-Fi", "Ethernet"}, nil },
		read: func(iface string) ([]string, string, error) {
			return append([]string{}, services[iface]...), iface, nil
		},
		write: func(iface string, servers []string) error {
			*writes++
			services[iface] = append([]string{}, servers...)
			return nil
		},
		probe: func(string) error { return nil }, verify: func() error { return nil },
	}
	return ops, services, writes
}
func TestSystemDNSRestoresEachInterfaceAndPreservesUserEdits(t *testing.T) {
	ops, services, _ := fakeSystemDNS(t)
	if err := applySystemDNSMulti("", []string{"198.51.100.53"}, ops); err != nil {
		t.Fatal(err)
	}
	b := LoadDNSBackup()
	if len(b.Services) != 2 || len(b.Services["Wi-Fi"]) != 0 || !reflect.DeepEqual(b.Services["Ethernet"], []string{"192.0.2.53"}) {
		t.Fatalf("per-interface backup: %+v", b)
	}
	services["Ethernet"] = []string{"203.0.113.53"}
	if err := restoreSystemDNS(ops); err != nil {
		t.Fatal(err)
	}
	if len(services["Wi-Fi"]) != 0 || !reflect.DeepEqual(services["Ethernet"], []string{"203.0.113.53"}) {
		t.Fatalf("user DNS overwritten: %v", services)
	}
	if LoadDNSBackup().Applied {
		t.Fatal("restore not marked complete")
	}
}
func TestSystemDNSPartialApplyRollsBackOnlyModifiedInterfaces(t *testing.T) {
	ops, services, _ := fakeSystemDNS(t)
	write := ops.write
	ops.write = func(iface string, servers []string) error {
		if iface == "Ethernet" && reflect.DeepEqual(servers, []string{"198.51.100.53"}) {
			return errors.New("cannot apply")
		}
		return write(iface, servers)
	}
	if err := applySystemDNSMulti("", []string{"198.51.100.53"}, ops); err == nil {
		t.Fatal("failure hidden")
	}
	if len(services["Wi-Fi"]) != 0 || !reflect.DeepEqual(services["Ethernet"], []string{"192.0.2.53"}) {
		t.Fatalf("rollback failed %v", services)
	}
	if LoadDNSBackup().Applied {
		t.Fatal("rollback left stale ownership")
	}
}
func TestSystemDNSVerificationFailureRestoresOriginals(t *testing.T) {
	ops, services, _ := fakeSystemDNS(t)
	ops.verify = func() error { return errors.New("resolver unavailable") }
	if err := applySystemDNSMulti("", []string{"198.51.100.53"}, ops); err == nil {
		t.Fatal("verification failure hidden")
	}
	if len(services["Wi-Fi"]) != 0 || !reflect.DeepEqual(services["Ethernet"], []string{"192.0.2.53"}) {
		t.Fatalf("rollback failed: %v", services)
	}
}
func TestSystemDNSRestoreWithoutOwnershipDoesNotWrite(t *testing.T) {
	ops, _, writes := fakeSystemDNS(t)
	if err := restoreSystemDNS(ops); err != nil {
		t.Fatal(err)
	}
	if *writes != 0 {
		t.Fatal("no backup must not reset user DNS to DHCP")
	}
}
func TestSystemDNSRestoreFailureRetainsOwnershipForRetry(t *testing.T) {
	ops, services, _ := fakeSystemDNS(t)
	if err := applySystemDNSMulti("", []string{"198.51.100.53"}, ops); err != nil {
		t.Fatal(err)
	}
	write := ops.write
	ops.write = func(iface string, servers []string) error {
		if iface == "Ethernet" {
			return errors.New("temporarily unavailable")
		}
		return write(iface, servers)
	}
	if err := restoreSystemDNS(ops); err == nil {
		t.Fatal("restore failure hidden")
	}
	if !LoadDNSBackup().Applied {
		t.Fatal("lost cleanup obligation")
	}
	ops.write = write
	if err := restoreSystemDNS(ops); err != nil {
		t.Fatal(err)
	}
	if len(services["Wi-Fi"]) != 0 || !reflect.DeepEqual(services["Ethernet"], []string{"192.0.2.53"}) {
		t.Fatalf("retry failed %v", services)
	}
}
