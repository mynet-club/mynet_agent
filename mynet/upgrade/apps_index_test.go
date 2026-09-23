package upgrade

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// R4-2：ctl apps.json 形状可解析；驱动域只出 gnb/wireguard
func TestParseCtlAppsIndex(t *testing.T) {
	path := filepath.Join("..", "..", "..", "mynet_ctl", "docs", "knowledge-base", "spec", "fixtures", "apps", "apps.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skip("apps fixture missing")
	}
	var idx struct {
		Products []struct {
			Name        string `json:"name"`
			ExternalKey string `json:"external_key"`
			Releases    []struct {
				Version  string `json:"version"`
				Platform string `json:"platform"`
				URL      string `json:"url"`
			} `json:"releases"`
		} `json:"products"`
	}
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatal(err)
	}
	var arts []Artifact
	for _, p := range idx.Products {
		comp := ComponentMynet
		switch p.ExternalKey {
		case "gnb":
			comp = ComponentGNB
		case "wireguard":
			comp = ComponentWireGuard
		}
		for _, r := range p.Releases {
			arts = append(arts, Artifact{Component: comp, Version: r.Version, Platform: r.Platform, URL: r.URL})
		}
	}
	s := NewService("https://ctl.mynet.club")
	s.Fetch = func(string) ([]Artifact, error) { return arts, nil }
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("driver list=%v", list)
	}
}
