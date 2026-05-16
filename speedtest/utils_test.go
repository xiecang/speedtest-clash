package speedtest

import (
	"testing"

	C "github.com/metacubex/mihomo/constant"
)

func TestShouldSkipProxyTestSkipsOnlyNonOutboundTypes(t *testing.T) {
	skipped := []C.AdapterType{
		C.Direct,
		C.Reject,
		C.RejectDrop,
		C.Compatible,
		C.Pass,
		C.Dns,
		C.Relay,
		C.Selector,
		C.Fallback,
		C.URLTest,
		C.LoadBalance,
	}
	for _, tp := range skipped {
		if !shouldSkipProxyTest(tp) {
			t.Fatalf("shouldSkipProxyTest(%s) = false, want true", tp)
		}
	}

	tested := []C.AdapterType{
		C.Shadowsocks,
		C.Vless,
		C.Trojan,
		C.AnyTLS,
		C.Mieru,
		C.Ssh,
		C.Sudoku,
		C.Masque,
		C.TrustTunnel,
	}
	for _, tp := range tested {
		if shouldSkipProxyTest(tp) {
			t.Fatalf("shouldSkipProxyTest(%s) = true, want false", tp)
		}
	}
}
