package webhook

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

// checkURL says what's wrong with an endpoint's URL, if anything. In
// production, it must be https, and can't name an address in our network.
func checkURL(raw string, production bool) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http"):
		return errors.New("must be an absolute http or https URL")
	case production && u.Scheme != "https":
		return errors.New("must be https")
	case u.User != nil:
		return errors.New("must not contain a username or password")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
		return checkAddr(ip, production)
	}
	return nil // a name: the dialer checks what it resolves to, every time
}

// checkAddr refuses addresses a webhook must never reach: link-local ones,
// like the cloud's metadata service, 169.254.169.254, always. Loopback and
// private ones, our own network, in production.
func checkAddr(ip netip.Addr, production bool) error {
	ip = ip.Unmap()
	if ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsMulticast() ||
		production && (ip.IsLoopback() || ip.IsPrivate()) {
		return fmt.Errorf("must not point at %s, an internal address", ip)
	}
	return nil
}

// NewClient returns the client deliveries go out with. Its dialer checks the
// address it actually connects to, after DNS: a check of the URL alone is
// fooled by a name that resolves to 10.0.0.1, or that changes after we look.
func NewClient(production bool) *http.Client {
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return err
			}
			return checkAddr(ap.Addr(), production)
		},
	}
	return &http.Client{
		// The whole delivery. A receiver should answer at once, and do its
		// work later: slow is the same as down.
		Timeout: 10 * time.Second,
		// No proxy from the environment: it would connect for us, unchecked.
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: 5 * time.Second,
			MaxIdleConnsPerHost: 10,
		},
		// A redirect is a failure, not something to follow somewhere else.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
