// Package webhook carries the configurable outgoing calls, and the control of their target.
//
// # The risk, named
//
// A URL typed into the interface becomes an HTTP request issued **from inside the cluster**, by
// the worker, which carries a service account token. It is server-side request forgery offered on
// a plate, and the interesting targets are known:
//
//   - `169.254.169.254` — the cloud metadata, hence instance credentials;
//   - the Kubernetes API itself;
//   - the cluster's internal services, which nothing else exposes;
//   - the pod's loopback.
//
// # The defence follows the house precedent
//
// The Redmine and Gitea plugins refuse writing **in their transport**, before the
// network, rather than through the callers' discipline — because a structural guarantee is not
// forgotten. The same act here: the check is in the dialler's `Control`, called by the network
// stack just before connecting, with the **actually resolved** address.
//
// That is what defeats DNS rebinding. Checking the URL then letting the client resolve again
// leaves a window in which the name changes its answer; here, there is no second resolution to
// fool — what is checked is what is connected.
//
// # What is refused, and what is not
//
// Refused: the private ranges, the loopback, link-local, multicast, and any redirect. Allowed
// subject to an explicit declaration by the deployment: private targets — a local dashboard needs
// `localhost` — and clear text.
//
// This package does not decide **what** to send. It decides only that we will not go where we
// must not.
package webhook

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
)

// ErrBlockedTarget reports a target the transport refuses to reach.
//
// Named so that the caller can tell it from a network outage: "refused" is fixed in the reaction's
// configuration, "unreachable" at the recipient's, and confusing the two sends one hunting in the
// wrong place.
var ErrBlockedTarget = errors.New("target refused")

// cgnat is the operators' carrier-grade NAT range (RFC 6598).
//
// Absent from the standard library's predicates, and yet of the same order as the private ranges:
// it names the hosting provider's network, not the Internet.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// checkAddress refuses an address the transport must not reach.
//
// Takes the **resolved** address, as the network stack is about to dial it. That is what tells
// this check from an inspection of a URL: a name that answers `93.184.216.34` at the check and
// `127.0.0.1` at the connection does not pass twice here, it passes once — at the moment that
// counts.
func checkAddress(address string, allowPrivate bool) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: adresse %q illisible", ErrBlockedTarget, address)
	}

	parsed, err := netip.ParseAddr(host)
	if err != nil {
		// The network stack dials only addresses. A name here would be the sign that the check is in the
		// wrong place, and letting it through would cancel the whole mechanism.
		return fmt.Errorf("%w: %q is not a resolved address", ErrBlockedTarget, host)
	}

	// An IPv4 address wrapped in IPv6 must be judged on its substance: `::ffff:169.254.169.254` does
	// name the cloud metadata, and the IPv6 predicates would not see it.
	if parsed.Is4In6() {
		parsed = parsed.Unmap()
	}

	if reason := forbidden(parsed); reason != "" {
		if allowPrivate && privatelyReachable(parsed) {
			return nil
		}
		return fmt.Errorf("%w: %s (%s)", ErrBlockedTarget, parsed, reason)
	}
	return nil
}

// forbidden names what forbids an address, or returns an empty string.
//
// The reason is returned rather than a boolean: "refused" with no ground sends one hunting for an
// outage, whereas "loopback" is fixed in one read.
func forbidden(address netip.Addr) string {
	switch {
	case !address.IsValid():
		return "invalid address"
	case address.IsUnspecified():
		return "unspecified address"
	case address.IsLoopback():
		return "boucle locale"
	case address.IsLinkLocalUnicast():
		// The most dangerous: `169.254.169.254` serves instance metadata at every hosting provider,
		// credentials included.
		return "link-local — instance metadata"
	case address.IsLinkLocalMulticast(), address.IsInterfaceLocalMulticast(), address.IsMulticast():
		return "multicast"
	case address.IsPrivate():
		return "private range"
	case cgnat.Contains(address):
		return "carrier-grade NAT range"
	default:
		return ""
	}
}

// privatelyReachable says that an address falls under the deployment's explicit authorization.
//
// It covers the loopback and the private ranges — enough to run a local dashboard or reach an
// internal service deliberately named. It **never** covers link-local: instance metadata is not a
// target a deployment wants to reach, and the blanket authorization must not reopen it on the
// side.
func privatelyReachable(address netip.Addr) bool {
	return address.IsLoopback() || address.IsPrivate() || cgnat.Contains(address)
}
