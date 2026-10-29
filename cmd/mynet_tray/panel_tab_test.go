package main

import "testing"

func TestPanelTabKeySharesPagesAndSeparatesInstances(t *testing.T) {
	for _, url := range []string{
		PanelURL(LocalAPI),
		DiagURL(LocalAPI),
		ProxyURL(LocalAPI),
		PanelURL(LocalAPI) + "home?from=tray",
		"http://127.0.0.1:8096/ui/",
	} {
		key, ok := panelTabKey(url)
		if !ok || key != "http://"+LocalAPI+"/gui/" {
			t.Fatalf("%s: %s %v", url, key, ok)
		}
	}
	other, ok := panelTabKey(PanelURL("127.0.0.1:9999"))
	if !ok || other == "http://"+LocalAPI+"/gui/" {
		t.Fatal("different API instances shared a tab key")
	}
	for _, url := range []string{
		"https://example.com/",
		"javascript:alert(1)",
		"http://user:pass@localhost/gui/",
		"http://localhost/gui/../secret",
		"http://localhost/app/dashboard",
	} {
		if _, ok := panelTabKey(url); ok {
			t.Fatalf("unrelated URL accepted: %s", url)
		}
	}
}
