package libraries

import (
	"os"
	"regexp"
	"strings"
)

var homeClusterRe = regexp.MustCompile(`^(?:pw://[A-Za-z0-9_.@-]{1,120}/)?[A-Za-z0-9][A-Za-z0-9_.-]{0,119}$`)

// HomeCluster is the cluster the Studio runs beside, where spaces are made
// and where a library in a folder on this server is: STUDIO_HOME_CLUSTER,
// else the one a workflow deployment runs on (pw://PW_USER/PW_POOL_NAME);
// "" for none.
func HomeCluster() string {
	v := strings.TrimSpace(os.Getenv("STUDIO_HOME_CLUSTER"))
	if v == "" {
		u, p := os.Getenv("PW_USER"), os.Getenv("PW_POOL_NAME")
		if u == "" || p == "" {
			return ""
		}
		v = "pw://" + u + "/" + p
	}
	if !homeClusterRe.MatchString(v) {
		return ""
	}
	return v
}
