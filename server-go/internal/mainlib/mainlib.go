// Package mainlib decides who may change the main library (the knowledge
// base at KB_ROOT, library id "kb").
//
// On a Studio without sign-in it is its one person's folder, and they may
// write in it. On a Studio with sign-in it is read with the Studio's own
// account by everyone, so it is read only for each person who signs in,
// and only its administrators change it: the deployment's owner (PW_USER)
// and the accounts STUDIO_ADMINS names. A request with no signed-in person
// on such a Studio never writes. People keep their own writable libraries
// (My library, and libraries they build on their systems), written as them.
package mainlib

import (
	"net/http"
	"os"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// ReadOnly is the reason a signed-in person who is not an administrator
// is given for a refused write in the main library.
const ReadOnly = "On a Studio with sign-in, the main library is read only: only its administrators change it. Use My library, or a library of your own on one of your systems, for your files."

// Settings is the refusal of a change to how one of the deployment's
// libraries is indexed (its depth or meaning) by a signed-in person who
// does not administer the Studio.
const Settings = "On a Studio with sign-in, the deployment's libraries are indexed as its administrators set them. Set the depth of a library of your own instead."

// Attach is the refusal of a chat attachment: attachments are kept in the
// main library, where everyone who signs in could read them.
const Attach = "On a Studio with sign-in, chat attachments would go into the main library, which is read only for you. Put the file in My library and ask about it there."

// Person is who asks: whether sign-in is on, and the person signed in.
type Person struct {
	AuthEnabled bool
	ID          string
	Username    string
	Email       string
	Groups      []string
}

// Getenv reads the settings; tests replace it.
var Getenv = os.Getenv

// Admin says whether p administers this Studio: its owner (PW_USER) or an
// account STUDIO_ADMINS names, by user name, id or e-mail, or a group as
// group:NAME. Names are matched without case.
func Admin(p Person) bool {
	if p.ID == "" && p.Username == "" && p.Email == "" {
		return false
	}
	is := func(name string) bool {
		name = strings.TrimSpace(name)
		if name == "" {
			return false
		}
		if g, ok := strings.CutPrefix(name, "group:"); ok {
			for _, have := range p.Groups {
				if have != "" && strings.EqualFold(have, g) {
					return true
				}
			}
			return false
		}
		for _, have := range []string{p.Username, p.ID, p.Email} {
			if have != "" && strings.EqualFold(have, name) {
				return true
			}
		}
		return false
	}
	if is(Getenv("PW_USER")) {
		return true
	}
	for _, n := range strings.FieldsFunc(Getenv("STUDIO_ADMINS"), func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		if is(n) {
			return true
		}
	}
	return false
}

// MayWrite says whether p may change the main library, and why not.
func MayWrite(p Person) (bool, string) {
	if !p.AuthEnabled || Admin(p) {
		return true, ""
	}
	return false, ReadOnly
}

// User is the person u on a Studio with sign-in on or off, as an agent
// that acts for them later (a Fleet wake) knows them.
func User(authEnabled bool, u *auth.User) Person {
	p := Person{AuthEnabled: authEnabled}
	if u != nil {
		p.ID, p.Username, p.Groups = u.ID, u.Username, u.Groups
		if e, ok := u.Claims["email"].(string); ok {
			p.Email = e
		}
	}
	return p
}

// Of is who asks in r. When who it is cannot be told, it is a person
// with no account on a Studio with sign-in, who never writes.
func Of(a *app.App, r *http.Request) Person {
	p := Person{AuthEnabled: true}
	v, err := platform.Of(a).ViewerOf(r)
	if err != nil || v == nil {
		if err != nil && !a.Cfg.AuthEnabled() {
			p.AuthEnabled = false
		}
		return p
	}
	p.AuthEnabled = v.AuthEnabled
	if v.User != nil {
		p.ID, p.Username = v.User.ID, v.User.Username
	}
	if u := auth.UserOf(r); u != nil && (p.ID == "" || u.ID == p.ID) {
		p.ID, p.Username = u.ID, u.Username
		p.Groups = u.Groups
		if e, ok := u.Claims["email"].(string); ok {
			p.Email = e
		}
	}
	return p
}

// Request says whether the person in r may change the main library, and
// why not.
func Request(a *app.App, r *http.Request) (bool, string) {
	return MayWrite(Of(a, r))
}
