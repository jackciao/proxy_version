package services

import (
	"encoding/base64"
	"strings"
	"testing"

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
