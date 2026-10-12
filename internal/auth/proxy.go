package auth

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type RequestIdentity struct{ trustedProxies []netip.Prefix }

func NewRequestIdentity(prefixes []netip.Prefix) *RequestIdentity {
	return &RequestIdentity{trustedProxies: append([]netip.Prefix(nil), prefixes...)}
}

func (i *RequestIdentity) ClientIP(r *http.Request) netip.Addr {
	peer, ok := parseRemoteAddress(r.RemoteAddr)
	if !ok {
		return netip.Addr{}
	}
	if !i.trusted(peer) {
		return peer
	}
	forwarded := parseForwardedAddresses(r.Header.Get("X-Forwarded-For"))
	if len(forwarded) == 0 {
		if candidate, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); err == nil {
			return candidate.Unmap()
		}
		return peer
	}
	for index := len(forwarded) - 1; index >= 0; index-- {
		if !i.trusted(forwarded[index]) {
			return forwarded[index]
		}
	}
	return forwarded[0]
}

func (i *RequestIdentity) Scheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	peer, ok := parseRemoteAddress(r.RemoteAddr)
	if ok && i.trusted(peer) {
		values := strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")
		if len(values) > 0 {
			value := strings.ToLower(strings.TrimSpace(values[len(values)-1]))
			if value == "http" || value == "https" {
				return value
			}
		}
	}
	return "http"
}

func (i *RequestIdentity) trusted(address netip.Addr) bool {
	for _, prefix := range i.trustedProxies {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func parseRemoteAddress(value string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil {
		host = strings.Trim(strings.TrimSpace(value), "[]")
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return address.Unmap(), true
}

func parseForwardedAddresses(value string) []netip.Addr {
	parts := strings.Split(value, ",")
	result := make([]netip.Addr, 0, len(parts))
	for _, part := range parts {
		address, err := netip.ParseAddr(strings.TrimSpace(part))
		if err != nil {
			return nil
		}
		result = append(result, address.Unmap())
	}
	return result
}
