package mynet

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

	// 直接构造 client 打 httptest（newBackClient 基址恒为中继，不收 base）
	rows, err := allCloudNodes(&backClient{
		base: strings.TrimRight(srv.URL, "/"), token: "token", hc: http.DefaultClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("want all 2 rows, got %d", len(rows))
	}
}
