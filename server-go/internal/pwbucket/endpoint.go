package pwbucket

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// Another address to ask a store at than its own: a VPC endpoint, or a
// gateway of the site's (docs/LIBRARIES.md, Buckets).

// vpceRe are the addresses of the stores' own private endpoints: an S3
// interface endpoint, or Google's Private Service Connect.
var vpceRe = regexp.MustCompile(`^([a-z0-9-]+\.)?vpce-[a-z0-9-]+\.s3\.[a-z0-9-]+\.vpce\.(amazonaws\.com|amazonaws\.com\.cn|c2s\.ic\.gov|sc2s\.sgov\.gov|csp\.hci\.ic\.gov|cloud\.adc-e\.uk|amazonaws\.eu)$|^storage-[a-z0-9-]+\.p\.googleapis\.com$`)

// Endpoints is the hosts an administrator lets people name besides the
// stores' own private endpoints (STUDIO_BUCKET_ENDPOINTS).
func Endpoints() []string {
	return strings.FieldsFunc(strings.ToLower(os.Getenv("STUDIO_BUCKET_ENDPOINTS")), func(r rune) bool { return r == ',' || r == ' ' })
}

// CheckEndpoint is an address a person gives for a bucket's store, checked
// and made plain ("https://host[:port]"); "" for none. Where several people
// use the Studio (shared), what it asks on its own network is not for one of
// them to say: only the stores' own private endpoints and the hosts an
// administrator listed are taken.
func CheckEndpoint(s string, shared bool) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
		return "", fmt.Errorf("an endpoint is an address like https://bucket.vpce-0example.s3.us-gov-west-1.vpce.amazonaws.com")
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && !shared && loopback(host)) {
		return "", fmt.Errorf("an endpoint is asked over https")
	}
	if shared {
		ok := u.Port() == "" && vpceRe.MatchString(host)
		for _, e := range Endpoints() {
			ok = ok || e == host || e == strings.ToLower(u.Host)
		}
		if !ok {
			return "", fmt.Errorf("%s is not one of the endpoints this Studio's administrator allows; a VPC endpoint of S3 (vpce) is always taken", host)
		}
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

func loopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
}

// siteEndpoint is where the environment says S3 is (AWS_ENDPOINT_URL_S3,
// AWS_ENDPOINT_URL), which the pw CLI's own requests heed too; "" for AWS's.
func siteEndpoint() string {
	for _, k := range []string{"AWS_ENDPOINT_URL_S3", "AWS_ENDPOINT_URL"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return strings.TrimSuffix(v, "/")
		}
	}
	return ""
}
