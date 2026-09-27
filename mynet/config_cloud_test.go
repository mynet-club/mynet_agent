package mynet

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAllCloudNodesFollowsEveryPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if page == "1" {
			fmt.Fprint(w, `{"success":true,"data":{"nodes":{"data":[{"node_id":"1"}],"last_page":2,"next_page_url":"ignored"}}}`)
			return
		}
		fmt.Fprint(w, `{"success":true,"data":{"nodes":{"data":[{"node_id":"2"}],"last_page":2,"next_page_url":null}}}`)
	}))
	defer srv.Close()

	rows, err := allCloudNodes(newBackClient(srv.URL, "token"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("want all 2 rows, got %d", len(rows))
	}
}
