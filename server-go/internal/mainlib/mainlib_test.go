package mainlib

import "testing"

func TestWhoMayWriteTheMainLibrary(t *testing.T) {
	env := map[string]string{"PW_USER": "owner", "STUDIO_ADMINS": " Ada@platform.example, bob-id,\tgroup:kb-editors "}
	defer func(g func(string) string) { Getenv = g }(Getenv)
	Getenv = func(k string) string { return env[k] }
	for _, c := range []struct {
		name string
		p    Person
		want bool
	}{
		{"sign-in off: its one person", Person{}, true},
		{"sign-in off: anyone named", Person{Username: "eve"}, true},
		{"sign-in on, nobody signed in", Person{AuthEnabled: true}, false},
		{"the deployment's owner", Person{AuthEnabled: true, Username: "Owner"}, true},
		{"named by e-mail", Person{AuthEnabled: true, Username: "ada", Email: "ada@platform.example"}, true},
		{"named by id", Person{AuthEnabled: true, ID: "bob-id", Username: "bob"}, true},
		{"in a named group", Person{AuthEnabled: true, Username: "cy", Groups: []string{"KB-Editors"}}, true},
		{"anyone else", Person{AuthEnabled: true, ID: "e1", Username: "eve", Email: "eve@platform.example", Groups: []string{"team"}}, false},
		{"a name that is a group's", Person{AuthEnabled: true, Username: "kb-editors"}, false},
		{"group: alone names nobody", Person{AuthEnabled: true, Username: "group:"}, false},
	} {
		if got, why := MayWrite(c.p); got != c.want || (!got && why != ReadOnly) {
			t.Errorf("%s: %v %q, want %v", c.name, got, why, c.want)
		}
	}
	env = map[string]string{}
	if ok, _ := MayWrite(Person{AuthEnabled: true, Username: "owner"}); ok {
		t.Error("with no owner or administrators named, nobody signed in writes")
	}
}
