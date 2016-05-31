// Package caddytls facilitates the management of TLS assets and integrates
// Let's Encrypt functionality into Caddy with first-class support for
// creating and renewing certificates automatically.
package caddytls

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"net"
	"os"
	"strings"

	"github.com/xenolf/lego/acme"
)

// HostQualifies returns true if the hostname alone
// appears eligible for automatic HTTPS. For example,
// localhost, empty hostname, and IP addresses are
// not eligible because we cannot obtain certificates
// for those names.
func HostQualifies(hostname string) bool {
	return hostname != "localhost" && // localhost is ineligible

		// hostname must not be empty
		strings.TrimSpace(hostname) != "" &&

		// must not contain wildcard (*) characters (until CA supports it)
		!strings.Contains(hostname, "*") &&

		// cannot be an IP address, see
		// https://community.letsencrypt.org/t/certificate-for-static-ip/84/2?u=mholt
		net.ParseIP(hostname) == nil
}

// existingCertAndKey returns true if the hostname has
// a certificate and private key in storage already,
// false otherwise.
func existingCertAndKey(hostname string) bool {
	_, err := os.Stat(storage.SiteCertFile(hostname))
	if err != nil {
		return false
	}
	_, err = os.Stat(storage.SiteKeyFile(hostname))
	if err != nil {
		return false
	}
	return true
}

// saveCertResource saves the certificate resource to disk. This
// includes the certificate file itself, the private key, and the
// metadata file.
func saveCertResource(cert acme.CertificateResource) error {
	err := os.MkdirAll(storage.Site(cert.Domain), 0700)
	if err != nil {
		return err
	}

	// Save cert
	err = ioutil.WriteFile(storage.SiteCertFile(cert.Domain), cert.Certificate, 0600)
	if err != nil {
		return err
	}

	// Save private key
	err = ioutil.WriteFile(storage.SiteKeyFile(cert.Domain), cert.PrivateKey, 0600)
	if err != nil {
		return err
	}

	// Save cert metadata
	jsonBytes, err := json.MarshalIndent(&cert, "", "\t")
	if err != nil {
		return err
	}
	err = ioutil.WriteFile(storage.SiteMetaFile(cert.Domain), jsonBytes, 0600)
	if err != nil {
		return err
	}

	return nil
}

// Revoke revokes the certificate for host via ACME protocol.
func Revoke(host string) error {
	if !existingCertAndKey(host) {
		return errors.New("no certificate and key for " + host)
	}

	// TODO: Use actual config?
	// TODO: Get email properly
	client, err := newACMEClient(&Config{}, true)
	if err != nil {
		return err
	}

	certFile := storage.SiteCertFile(host)
	certBytes, err := ioutil.ReadFile(certFile)
	if err != nil {
		return err
	}

	err = client.RevokeCertificate(certBytes)
	if err != nil {
		return err
	}

	err = os.Remove(certFile)
	if err != nil {
		return errors.New("certificate revoked, but unable to delete certificate file: " + err.Error())
	}

	return nil
}

// tlsSniSolver is a type that can solve tls-sni challenges using
// an existing listener and our custom, in-memory certificate cache.
type tlsSniSolver struct{}

// Present adds the challenge certificate to the cache.
func (s tlsSniSolver) Present(domain, token, keyAuth string) error {
	cert, err := acme.TLSSNI01ChallengeCert(keyAuth)
	if err != nil {
		return err
	}
	cacheCertificate(Certificate{
		Certificate: cert,
		Names:       []string{domain},
	})
	return nil
}

// CleanUp removes the challenge certificate from the cache.
func (s tlsSniSolver) CleanUp(domain, token, keyAuth string) error {
	uncacheCertificate(domain)
	return nil
}

// ConfigHolder is any type that has a Config; it presumably is
// connected to a hostname and port on which it is serving.
type ConfigHolder interface {
	TLSConfig() *Config
	Host() string
	Port() string
}

// QualifiesForManagedTLS returns true if c qualifies for
// for managed TLS (but not on-demand TLS specifically).
// It does NOT check to see if a cert and key already exist
// for the config. If the return value is true, you should
// be OK to set c.TLSConfig().Managed to true; then you should
// check that value in the future instead, because the process
// of setting up the config may make it look like it doesn't
// qualify even though it originally did.
func QualifiesForManagedTLS(c ConfigHolder) bool {
	if c == nil {
		return false
	}
	tlsConfig := c.TLSConfig()

	return (!tlsConfig.Manual || tlsConfig.OnDemand) && // user might provide own cert and key

		// if self-signed, we've already generated one to use
		!tlsConfig.SelfSigned &&

		// user can force-disable managed TLS at a TLS level
		c.Port() != "80" &&
		tlsConfig.LetsEncryptEmail != "off" &&

		// we get can't certs for some kinds of hostnames, but
		// on-demand TLS allows empty hostnames at startup
		(HostQualifies(c.Host()) || tlsConfig.OnDemand)
}

// DNSProviderConstructor is a function that takes credentials and
// returns a type that can solve the ACME DNS challenges.
type DNSProviderConstructor func(credentials ...string) (acme.ChallengeProvider, error)

// dnsProviders is the list of DNS providers that have been plugged in.
var dnsProviders = make(map[string]DNSProviderConstructor)

// RegisterDNSProvider registers provider by name for solving the ACME DNS challenge.
func RegisterDNSProvider(name string, provider DNSProviderConstructor) {
	dnsProviders[name] = provider
}

var (
	// DefaultEmail represents the Let's Encrypt account email to use if none provided.
	DefaultEmail string

	// Agreed indicates whether user has agreed to the Let's Encrypt SA.
	Agreed bool

	// CAUrl represents the default URL to the CA's ACME directory endpoint.
	CAUrl string
)