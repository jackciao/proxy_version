package services

import (
	"encoding/base64"
	"strings"
	"testing"

	"proxy_version/internal/models"

	"golang.org/x/crypto/curve25519"
)

func TestGenerateX25519KeysReturnsMatchingRealityKeyPair(t *testing.T) {
	publicKey, privateKey := generateX25519Keys()

	publicBytes, err := base64.RawURLEncoding.DecodeString(publicKey)
	if err != nil {
		t.Fatalf("public key is not base64url: %v", err)
	}
	privateBytes, err := base64.RawURLEncoding.DecodeString(privateKey)
	if err != nil {
		t.Fatalf("private key is not base64url: %v", err)
	}
	if len(publicBytes) != curve25519.PointSize {
		t.Fatalf("public key length = %d, want %d", len(publicBytes), curve25519.PointSize)
	}
	if len(privateBytes) != curve25519.ScalarSize {
		t.Fatalf("private key length = %d, want %d", len(privateBytes), curve25519.ScalarSize)
	}

	derivedPublic, err := curve25519.X25519(privateBytes, curve25519.Basepoint)
	if err != nil {
		t.Fatalf("derive public key: %v", err)
	}
	if string(derivedPublic) != string(publicBytes) {
		t.Fatal("public key does not match private key")
	}
}

func TestGenerateSingBoxConfigForVLESSRealityVision(t *testing.T) {
	_, privateKey := generateX25519Keys()
	service := NewProxyService()

	config, err := service.generateSingBoxConfig(map[string]interface{}{
		"protocol":   "vless",
		"uuid":       "11111111-1111-4111-8111-111111111111",
		"port":       float64(443),
		"transport":  "tcp",
		"security":   "reality",
		"privateKey": privateKey,
		"shortId":    "0123456789abcdef",
		"serverName": "www.apple.com",
	}, false, nil)
	if err != nil {
		t.Fatalf("generate config: %v", err)
	}

	inbounds := config["inbounds"].([]map[string]interface{})
	inbound := inbounds[0]
	if _, ok := inbound["multiplex"]; ok {
		t.Fatal("server inbound should not include multiplex/brutal config")
	}

	tlsConfig := inbound["tls"].(map[string]interface{})
	reality := tlsConfig["reality"].(map[string]interface{})
	if reality["private_key"] != privateKey {
		t.Fatal("reality private key was not preserved")
	}
	if tlsConfig["server_name"] != "www.apple.com" {
		t.Fatal("reality server_name was not preserved")
	}
}

func TestGenerateConfigForAnyTLSUsesTLSAndShareablePassword(t *testing.T) {
	service := NewProxyService()

	nodeConfig, err := service.GenerateConfig("anytls", "example.com", 443, emptyNodeConfig())
	if err != nil {
		t.Fatalf("generate anytls config: %v", err)
	}
	if nodeConfig["protocol"] != "anytls" {
		t.Fatalf("protocol = %v, want anytls", nodeConfig["protocol"])
	}
	password, ok := nodeConfig["password"].(string)
	if !ok || password == "" {
		t.Fatal("anytls password was not generated")
	}
	if _, err := base64.StdEncoding.DecodeString(password); err != nil {
		t.Fatalf("anytls password should be base64 encoded: %v", err)
	}

	singboxConfig, err := service.generateSingBoxConfig(nodeConfig, false, nil)
	if err != nil {
		t.Fatalf("generate sing-box config: %v", err)
	}
	inbound := singboxConfig["inbounds"].([]map[string]interface{})[0]
	if inbound["type"] != "anytls" {
		t.Fatalf("inbound type = %v, want anytls", inbound["type"])
	}
	tlsConfig := inbound["tls"].(map[string]interface{})
	if tlsConfig["server_name"] != "example.com" {
		t.Fatalf("server_name = %v, want example.com", tlsConfig["server_name"])
	}
}

func TestGenerateConfigRequiresDomainForTLSProtocols(t *testing.T) {
	service := NewProxyService()
	if _, err := service.GenerateConfig("anytls", "", 443, emptyNodeConfig()); err == nil {
		t.Fatal("anytls without domain should be rejected")
	}
}

func TestTrojanGRPCSingBoxConfigIncludesTLSAndTransport(t *testing.T) {
	service := NewProxyService()
	nodeConfig, err := service.GenerateConfig("trojan-grpc-tls", "example.com", 443, emptyNodeConfig())
	if err != nil {
		t.Fatalf("generate trojan config: %v", err)
	}
	singboxConfig, err := service.generateSingBoxConfig(nodeConfig, false, nil)
	if err != nil {
		t.Fatalf("generate sing-box config: %v", err)
	}
	inbound := singboxConfig["inbounds"].([]map[string]interface{})[0]
	if _, ok := inbound["tls"].(map[string]interface{}); !ok {
		t.Fatal("trojan inbound missing tls")
	}
	transport := inbound["transport"].(map[string]interface{})
	if transport["type"] != "grpc" {
		t.Fatalf("transport type = %v, want grpc", transport["type"])
	}
}

func TestListenAddressConflictsHandlesIPv6AndWildcards(t *testing.T) {
	tests := []struct {
		name      string
		localAddr string
		port      int
		selected  string
		want      bool
	}{
		{"exact ipv6 bracket", "[2603:c021:4005:dbab::1]:443", 443, "2603:c021:4005:dbab::1", true},
		{"exact ipv6 netstat", "2603:c021:4005:dbab::1:443", 443, "2603:c021:4005:dbab::1", true},
		{"ipv6 wildcard", "[::]:443", 443, "2603:c021:4005:dbab::1", true},
		{"netstat ipv6 wildcard", ":::443", 443, "2603:c021:4005:dbab::1", true},
		{"ipv4 wildcard does not block ipv6", "0.0.0.0:443", 443, "2603:c021:4005:dbab::1", false},
		{"ipv4 wildcard blocks ipv4", "0.0.0.0:443", 443, "203.0.113.10", true},
		{"wrong port", "[::]:8443", 443, "2603:c021:4005:dbab::1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := listenAddressConflicts(tt.localAddr, tt.port, tt.selected); got != tt.want {
				t.Fatalf("listenAddressConflicts(%q, %d, %q) = %v, want %v", tt.localAddr, tt.port, tt.selected, got, tt.want)
			}
		})
	}
}

func emptyNodeConfig() models.NodeConfig {
	return models.NodeConfig{}
}

func TestValidateCamouflageDomainRejectsUnsafeNames(t *testing.T) {
	badDomains := []string{"", "../example.com", "example.com/evil", "example.com\\evil", "example.com\x00evil"}
	for _, domain := range badDomains {
		if err := validateCamouflageDomain(domain); err == nil {
			t.Fatalf("validateCamouflageDomain(%q) returned nil", domain)
		}
	}
	if err := validateCamouflageDomain("example.com"); err != nil {
		t.Fatalf("valid domain rejected: %v", err)
	}
}

func TestInjectMediaItemsRendersRealPostersAndRemovesMockScript(t *testing.T) {
	items := []mediaItem{{
		Title:  "Example Show",
		Year:   "2024",
		Rating: "8.8",
		Poster: "https://static.tvmaze.com/uploads/images/original_untouched/1/1.jpg",
	}}

	page := injectMediaItems(streamVaultIndexHTML, items)
	if !strings.Contains(page, "Example Show") {
		t.Fatal("rendered page does not contain media title")
	}
	if !strings.Contains(page, `<img src="https://static.tvmaze.com/uploads/images/original_untouched/1/1.jpg"`) {
		t.Fatal("rendered page does not contain poster image")
	}
	if strings.Contains(page, "Generate mock content cards") || strings.Contains(page, "Midnight Protocol") {
		t.Fatal("mock content script was not removed")
	}
}
