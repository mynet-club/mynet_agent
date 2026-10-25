package main

import "testing"

func TestPanelTabKeySharesPagesAndSeparatesInstances(t *testing.T) {
	for _, url := range []string{PanelURL(LocalAPI), DiagURL(LocalAPI), ProxyURL(LocalAPI), PanelURL(LocalAPI) + "?from=tray#diag"} {
		key, ok := panelTabKey(url)
		if !ok || key != PanelURL(LocalAPI) {
			t.Fatalf("%s: %s %v", url, key, ok)
		}
	}
	other, ok := panelTabKey(PanelURL("127.0.0.1:9999"))
	if !ok || other == PanelURL(LocalAPI) {
		t.Fatal("different API instances shared a tab key")
	}
	for _, url := range []string{"https://example.com/", "javascript:alert(1)", "http://user:pass@localhost/ui/", "http://localhost/ui/other"} {
		if _, ok := panelTabKey(url); ok {
			t.Fatalf("unrelated URL accepted: %s", url)
		}
	}
}
