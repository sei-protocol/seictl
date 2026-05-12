package server

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

// SECURITY POSTURE — trusted-header mode
//
// The trust boundary is the POD, not the loopback interface. Loopback
// bind keeps the sidecar off the pod network, but every container in
// the pod shares the network namespace and can reach 127.0.0.1:7777
// directly with a forged X-Remote-User. With shareProcessNamespace:
// true (the current SeiNode default), a colocated container can also
// read /proc/<sidecar>/mem and exfiltrate the unlocked keyring —
// memory-read is the load-bearing threat, not header forgery; this
// middleware does not defend against it.
//
// Pod-level isolation requirements (enforced by the controller-side
// PR): hostNetwork, hostPID, and hostIPC MUST all be false.
// hostNetwork would expose 127.0.0.1 to every other hostNetwork pod
// on the node. hostPID/hostIPC would let off-pod processes attach.

const (
	// AuthnModeUnauthenticated: sidecar binds all interfaces; every
	// caller is trusted. Acceptable only on validator-only pod
	// networks.
	AuthnModeUnauthenticated = ""

	// AuthnModeTrustedHeader pairs the sidecar with an in-pod
	// kube-rbac-proxy on TLS :8443. The proxy performs TokenReview +
	// SAR against the K8s API and forwards passed requests to
	// 127.0.0.1:7777 with X-Remote-User naming the authenticated
	// identity.
	AuthnModeTrustedHeader = "trusted-header"

	remoteUserHeader = "X-Remote-User"
)

// bypassPaths skip the X-Remote-User check in trusted-header mode:
//   - /v0/healthz: kubelet readiness probe (no auth headers).
//   - /v0/livez:   kubelet liveness probe (no auth headers).
//   - /v0/metrics: Prometheus scrape (no SA token in the scrape job).
//
// kube-rbac-proxy must include all three in its --allow-paths so
// probes and scrapes traverse the proxy without TokenReview.
var bypassPaths = map[string]struct{}{
	"/v0/healthz": {},
	"/v0/livez":   {},
	"/v0/metrics": {},
}

// AuthnMode reads SEI_SIDECAR_AUTHN_MODE and returns the canonical
// value. Strict: an unrecognized non-empty value is an error, so a
// typo (e.g. "trusted_header" with underscore) cannot silently
// degrade a hardened deployment to wide-open :7777.
func AuthnMode() (string, error) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("SEI_SIDECAR_AUTHN_MODE")))
	switch raw {
	case "", "unauthenticated":
		return AuthnModeUnauthenticated, nil
	case AuthnModeTrustedHeader:
		return AuthnModeTrustedHeader, nil
	default:
		return "", fmt.Errorf("SEI_SIDECAR_AUTHN_MODE=%q is not recognized (allowed: \"\", \"unauthenticated\", %q)", raw, AuthnModeTrustedHeader)
	}
}

// BindAddress returns the listen address for the given mode. The
// loopback bind in trusted-header mode is load-bearing — it confines
// the listen socket to the pod's network namespace so the only path
// to :7777 is through the in-pod proxy.
func BindAddress(port, mode string) string {
	if mode == AuthnModeTrustedHeader {
		return "127.0.0.1:" + port
	}
	return ":" + port
}

// trustedHeaderMiddleware enforces X-Remote-User on every path
// outside bypassPaths. The header check requires EXACTLY one
// non-empty value: a misconfigured proxy that appends instead of
// overwriting would let an attacker-supplied value arrive first, so
// len != 1 fails closed rather than silently trusting the first.
func trustedHeaderMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := bypassPaths[r.URL.Path]; ok {
			next.ServeHTTP(w, r)
			return
		}
		vals := r.Header.Values(remoteUserHeader)
		if len(vals) != 1 || vals[0] == "" {
			writeError(w, http.StatusUnauthorized, "expected exactly one non-empty "+remoteUserHeader+" header")
			return
		}
		next.ServeHTTP(w, r)
	})
}
