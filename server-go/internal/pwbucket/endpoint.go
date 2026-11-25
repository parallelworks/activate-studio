package pwbucket

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
)

// Another address to ask a store at than its own: a VPC endpoint, or a
// gateway of the site's (docs/LIBRARIES.md, Buckets).

// The stores' own private endpoints: an S3 interface endpoint, and Google's
// Private Service Connect.
var (
	vpceRe = regexp.MustCompile(`^([a-z0-9-]+\.)?vpce-[a-z0-9-]+\.s3\.[a-z0-9-]+\.vpce\.(amazonaws\.com|amazonaws\.com\.cn|c2s\.ic\.gov|sc2s\.sgov\.gov|csp\.hci\.ic\.gov|cloud\.adc-e\.uk|amazonaws\.eu)$`)
	pscRe  = regexp.MustCompile(`^storage-[a-z0-9-]+\.p\.googleapis\.com$`)
)

// Endpoints is the hosts an administrator lists (STUDIO_BUCKET_ENDPOINTS),
// each with its port unless that is 443:
// the only ones a person may choose where people sign in, and taken besides
// the stores' own private endpoints where nobody does.
func Endpoints() []string {
	return strings.FieldsFunc(strings.ToLower(os.Getenv("STUDIO_BUCKET_ENDPOINTS")), func(r rune) bool { return r == ',' || r == ' ' })
}

// CheckEndpoint is an address a person gives for the store of a bucket in
// csp ("" when that is not known yet), checked and made plain
// ("https://host[:port]"); "" for none. The bucket's credential goes there,
// so it is one of the store's own private endpoints or a host the
// administrator listed. Where several people use the Studio (shared) it is a
// listed one alone: a private endpoint's name is anyone's to make, at an
// address of their choosing on the Studio's network.
func CheckEndpoint(s, csp string, shared bool) (string, error) {
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
	listed := slices.ContainsFunc(Endpoints(), func(e string) bool { return atPort(e) == atPort(u.Host) })
	if u.Scheme != "https" && !(u.Scheme == "http" && !shared && listed && loopback(host)) {
		return "", fmt.Errorf("an endpoint is asked over https")
	}
	if shared && !listed {
		return "", fmt.Errorf("%s is not one of the endpoints this Studio's administrator lists", host)
	}
	private := u.Port() == "" && (csp != Google && vpceRe.MatchString(host) || csp != AWS && pscRe.MatchString(host))
	if !listed && !private {
		return "", fmt.Errorf("%s is not a private endpoint of the bucket's store (a VPC endpoint of S3, or Google's Private Service Connect); another host has to be in STUDIO_BUCKET_ENDPOINTS, in the Studio's environment", host)
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

// atPort is a host with the port asked, https's where it names none: a listed
// host is that port of it alone, since another may be anyone's.
func atPort(host string) string {
	u, err := url.Parse("//" + strings.ToLower(host))
	if err != nil {
		return ""
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return net.JoinHostPort(u.Hostname(), port)
}

func loopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
}

// siteEndpoint is where the environment says S3 is (AWS_ENDPOINT_URL_S3,
// AWS_ENDPOINT_URL), which the pw CLI's own requests heed too; nil for
// AWS's. One that is not an address is an error: S3 is then not asked at
// all, since it may be meant to be asked there alone.
func siteEndpoint() (*url.URL, error) {
	for _, k := range []string{"AWS_ENDPOINT_URL_S3", "AWS_ENDPOINT_URL"} {
		v := strings.TrimSpace(os.Getenv(k))
		if v == "" {
			continue
		}
		u, err := url.Parse(v)
		if err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			// Its value is the administrator's: they have it.
			return nil, &SiteError{k}
		}
		return u, nil
	}
	return nil, nil
}

// SiteError is an address of S3's in the Studio's environment that is none.
type SiteError struct{ Name string }

func (e *SiteError) Error() string {
	return e.Name + " in the Studio's environment is not an address (https://host); buckets in AWS are not read until it is corrected"
}

// CheckSite is what is wrong with the addresses the environment gives, for
// the Studio to say as it starts.
func CheckSite() error {
	_, err := siteEndpoint()
	return err
}

// The names the platform gives a bucket, which go into the host asked.
var (
	// regionRe is a region of AWS's.
	regionRe = regexp.MustCompile(`^[a-z]{2,4}(-[a-z]+)+-[0-9]{1,2}$`)
	// storeRe is a bucket's name in its store.
	storeRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9_-]|\.[a-z0-9]){1,220}$`)
	// legacyRe is a name S3 took in us-east-1 until 2018 as well, which has
	// no ".." in it either. It goes into a path, never into a host (hostRe).
	legacyRe = regexp.MustCompile(`^[A-Za-z0-9._-]{3,255}$`)
	// hostRe is a bucket's name that a host can start with, under the store's
	// certificate.
	hostRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
)

func storeName(csp, name string) bool {
	return storeRe.MatchString(name) || csp == AWS && legacyRe.MatchString(name) && !strings.Contains(name, "..")
}

// checkNames refuses a region or a bucket's name that is not one.
func checkNames(b Bucket) error {
	if (b.CSP == AWS || b.CSP == Google) && b.Store != "" && !storeName(b.CSP, b.Store) {
		return errors.New("the platform gives a name for the bucket in its store that is not one")
	}
	if b.CSP == AWS && b.Region != "" && !regionRe.MatchString(b.Region) {
		return errors.New("the platform gives a region for the bucket that is not one")
	}
	return nil
}
