package services

import (
	"strings"
	"testing"
)

func TestAimiliCountriesFromNodesOnlyIncludesAvailable(t *testing.T) {
	nodes := []map[string]interface{}{
		{"country": "日本", "probe_status": "available"},
		{"country": "日本", "probe_status": "available"},
		{"country": "美国", "probe_status": "not_checked"},
		{"country": "泰国", "probe_status": "unavailable"},
		{"country": "韩国", "probe_status": "available"},
	}

	countries := aimiliCountriesFromNodes(nodes)
	if len(countries) != 2 {
		t.Fatalf("expected 2 available countries, got %d: %#v", len(countries), countries)
	}
	if countries[0].Name != "日本" || countries[0].Count != 2 {
		t.Fatalf("unexpected first country: %#v", countries[0])
	}
	if countries[1].Name != "韩国" || countries[1].Count != 1 {
		t.Fatalf("unexpected second country: %#v", countries[1])
	}
}

func TestAimiliCountriesFromNodesReturnsEmptyWithoutAvailableNodes(t *testing.T) {
	nodes := []map[string]interface{}{
		{"country": "美国", "probe_status": "not_checked"},
		{"country": "泰国", "probe_status": "unavailable"},
	}

	if countries := aimiliCountriesFromNodes(nodes); len(countries) != 0 {
		t.Fatalf("expected no available countries, got %#v", countries)
	}
}

func TestAimiliBundleContainsPatchedManager(t *testing.T) {
	manager, err := aimiliBundle.ReadFile("aimili_bundle/vpngate_manager.py")
	if err != nil {
		t.Fatalf("read embedded manager: %v", err)
	}
	text := string(manager)
	for _, marker := range []string{
		"proxy_version fixed-region compatibility patch",
		"proxy_version full availability scan patch",
		"proxy_version missing intermediate certificate patch",
	} {
		if !strings.Contains(text, marker) {
			t.Fatalf("embedded manager is missing %q", marker)
		}
	}
}
