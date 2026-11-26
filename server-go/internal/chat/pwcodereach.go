package chat

import (
	"errors"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// When pw code cannot be reached on a machine, a person is told so in plain
// words, the same in the Library's New pw code chat here and when a chat's
// session cannot start: what ssh or the socket said goes to the log.

// unreached says whether err is pw code not being reached (its pipe or
// socket failed, or it did not answer in time) rather than an answer of
// its, and the machine's site where the error names one.
func unreached(err error) (site string, ok bool) {
	var de *pwcode.DaemonError
	if !errors.As(err, &de) || !de.Unreached {
		return "", false
	}
	return de.Site, true
}

// notAnswering is the reason pw code is not offered at site ("" for the
// machine the Studio drives for the viewer).
func (m *Module) notAnswering(site string) string {
	if site == "" || site == m.pwSite() {
		return "pw code does not answer for you on this Studio's machine now. Try again in a moment; if it goes on, tell the Studio's administrator."
	}
	return "pw code on " + remote.ShortName(site) + " does not answer for you now. Check that it runs there, then try again."
}

// plainReach is err, or the plain reason where it is pw code not being
// reached, which is logged as it was.
func (m *Module) plainReach(err error, what string) error {
	site, ok := unreached(err)
	if !ok {
		return err
	}
	m.A.Log.Warn("pw code chat: "+what, "site", site, "err", err.Error())
	return pwErr(502, m.notAnswering(site))
}

// pwSaid is a pw code failure as a chat shows it: said as pw code's, once.
func pwSaid(msg string) string {
	if strings.HasPrefix(msg, "pw code ") || strings.HasPrefix(msg, "pw code:") {
		return msg
	}
	return "pw code: " + msg
}
