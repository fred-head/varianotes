package auth

import (
	"crypto/tls"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestRequestIdentityIgnoresForwardedHeadersFromUntrustedPeer(t *testing.T) {
	identity := NewRequestIdentity([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	request := httptest.NewRequest("GET", "http://notes.example.test", nil)
	request.RemoteAddr = "198.51.100.20:1234"
	request.Header.Set("X-Forwarded-For", "203.0.113.9")
	request.Header.Set("X-Real-IP", "203.0.113.10")
	request.Header.Set("X-Forwarded-Proto", "https")
	if got := identity.ClientIP(request).String(); got != "198.51.100.20" {
		t.Fatalf("trusted spoofed client IP: %s", got)
	}
	if got := identity.Scheme(request); got != "http" {
		t.Fatalf("trusted spoofed scheme: %s", got)
	}
}

func TestRequestIdentityUsesRightmostUntrustedHopFromTrustedProxy(t *testing.T) {
	identity := NewRequestIdentity([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	request := httptest.NewRequest("GET", "http://notes.example.test", nil)
	request.RemoteAddr = "10.0.0.2:8080"
	request.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.3")
	request.Header.Set("X-Forwarded-Proto", "https")
	if got := identity.ClientIP(request).String(); got != "203.0.113.7" {
		t.Fatalf("wrong forwarded client IP: %s", got)
	}
	if got := identity.Scheme(request); got != "https" {
		t.Fatalf("wrong forwarded scheme: %s", got)
	}
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("X-Forwarded-Proto", "http")
	if got := identity.Scheme(request); got != "https" {
		t.Fatalf("forwarded header downgraded TLS: %s", got)
	}
}

func TestRequestIdentityRejectsMalformedForwardingChain(t *testing.T) {
	identity := NewRequestIdentity([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	request := httptest.NewRequest("GET", "http://notes.example.test", nil)
	request.RemoteAddr = "10.0.0.2:8080"
	request.Header.Set("X-Forwarded-For", "203.0.113.7, attacker")
	if got := identity.ClientIP(request).String(); got != "10.0.0.2" {
		t.Fatalf("accepted malformed forwarding chain: %s", got)
	}
}
