package pwbucket

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// What a failed connection says, of addresses, certificates and the network,
// is for the administrator where several people use the Studio: CLI.Report
// is told it, and the person that the store could not be reached.

// unreachable is a failed request as a person is told of it.
type unreachable struct {
	what string
	err  error
}

func (e *unreachable) Error() string { return e.what }
func (e *unreachable) Unwrap() error { return e.err }

// quiet is err as the bucket's person is told of it.
func (c *CLI) quiet(err error) error {
	var u *url.Error
	var done *unreachable
	if c.Report == nil || err == nil || errors.As(err, &done) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &u) {
		return err
	}
	c.Report(redact(err.Error()))
	return &unreachable{"the store could not be reached at " + u.URL + "; the Studio's log says why", err}
}

// connRe is a failed connection in what pw says.
var connRe = regexp.MustCompile(`(?i)x509|certificate|\btls\b|handshake|proxyconnect|\b(dial|read|write) (tcp|udp|unix)|connection (refused|reset|timed out)|no route to host|no such host|network is unreachable|i/o timeout|\blookup \S|\bEOF\b|\b\d{1,3}(\.\d{1,3}){3}\b`)

// quietRun is what a pw run said as the bucket's person is told of it.
func (c *CLI) quietRun(command, msg string) string {
	if c.Report == nil || !connRe.MatchString(msg) {
		return msg
	}
	c.Report(command + ": " + msg)
	return "the pw CLI could not reach the platform or the store; the Studio's log says why"
}

// quietBody is an object's bytes, whose reading can fail as a request can.
type quietBody struct {
	io.ReadCloser
	c   *CLI
	ctx context.Context
	at  string
}

func (b *quietBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == nil || err == io.EOF || b.ctx.Err() != nil {
		return n, err
	}
	return n, b.c.quiet(&url.Error{Op: "Get", URL: b.at, Err: err})
}

// cloud is how the names start of the variables a cloud's programs take
// credentials, or where to find them, from.
var cloud = []string{"AWS_", "AMAZON_", "GOOGLE_", "GCLOUD_", "GCE_", "GCP_", "CLOUDSDK_", "BOTO_", "AZURE_", "ARM_", "MSI_", "IDENTITY_", "OCI_", "ORACLE_"}

// kept are those of them that name no credential and that pw heeds.
var kept = map[string]bool{"AWS_ENDPOINT_URL": true, "AWS_ENDPOINT_URL_S3": true, "AWS_USE_FIPS_ENDPOINT": true, "AWS_USE_DUALSTACK_ENDPOINT": true, "AWS_CA_BUNDLE": true}

// uncloud is an environment without the Studio's own cloud credentials: pw
// asks a store with what the platform gives it for the person, or not at
// all. Nor is it to ask the computer's own metadata service for one.
func uncloud(env []string, endpoint string) []string {
	out := make([]string, 0, len(env)+3)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, p := range cloud {
			drop = drop || strings.HasPrefix(strings.ToUpper(k), p)
		}
		if drop && !kept[k] || endpoint != "" && (k == "AWS_ENDPOINT_URL" || k == "AWS_ENDPOINT_URL_S3") {
			continue
		}
		out = append(out, kv)
	}
	if endpoint != "" {
		out = append(out, "AWS_ENDPOINT_URL="+endpoint)
	}
	// There is no such file, so none of the account's are looked for.
	return append(out, "AWS_EC2_METADATA_DISABLED=true", "AWS_CONFIG_FILE="+os.DevNull, "AWS_SHARED_CREDENTIALS_FILE="+os.DevNull)
}
