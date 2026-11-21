package libraries

import (
	"reflect"
	"testing"
)

// Every kind of library today reads as one place, one account and one
// index, from its record alone, so older libraries and lists need nothing.
func TestAccessOfEachKind(t *testing.T) {
	src, dest := "/home/ada/work", "ada@box"
	for _, c := range []struct {
		name     string
		d        UserDef
		writable bool
		want     Access
	}{
		{"built over pw ssh", UserDef{Transport: "pw", Site: "pw://ada/cluster-a", Build: &RemoteBuild{SourceRoot: src}}, true,
			Access{Where: "system", System: "cluster-a", Via: "pw", ReadAs: "you", Write: true, Index: "built"}},
		{"connected over ssh", UserDef{Transport: "ssh", Site: dest, Destination: &dest, Existing: ptr(true), SourceRoot: &src}, false,
			Access{Where: "system", System: dest, Via: "ssh", ReadAs: "you", Index: "connected"}},
		{"built by the workflow", UserDef{Site: "cluster-b", Run: &WorkflowRun{}}, false,
			Access{Where: "system", System: "cluster-b", Via: "workflow", ReadAs: "you", Index: "built"}},
		{"My library", UserDef{Transport: "pw", Site: "cluster-a", Space: &SpaceInfo{Account: "ada"}, Build: &RemoteBuild{SourceRoot: src}}, true,
			Access{Where: "system", System: "cluster-a", Via: "pw", ReadAs: "you", Write: true, Index: "built"}},
		{"shared with them", UserDef{Transport: "pw", Site: "cluster-a", Shared: &SharedInfo{From: "bob", Access: "read"}}, false,
			Access{Where: "system", System: "cluster-a", Via: "pw", ReadAs: "you", SharedFrom: "bob", Index: "built"}},
	} {
		if got := UserAccess(c.d, c.writable); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	for _, c := range []struct {
		name string
		l    Library
		want Access
	}{
		{"the main library", Library{Primary: true, Writable: true}, Access{Where: "here", ReadAs: "studio", Write: true, Index: "built"}},
		{"a folder built here", Library{Build: &LocalBuild{}}, Access{Where: "here", ReadAs: "studio", Index: "built"}},
		{"an index connected here", Library{}, Access{Where: "here", ReadAs: "studio", Index: "connected"}},
		{"a bucket", Library{Bucket: &BucketOf{}}, Access{Where: "bucket", ReadAs: "studio", Index: "built"}},
	} {
		if got := Public(c.l).Access; !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

// A space over plain ssh is written only on a Studio one person uses.
func TestWritableByOverSSH(t *testing.T) {
	dest := "ada@box"
	d := UserDef{Transport: "ssh", Site: dest, Destination: &dest, Space: &SpaceInfo{Account: "ada"}}
	if !d.WritableBy("single", false) || d.WritableBy("single", true) || d.WritableBy("ada", true) {
		t.Fatal("plain ssh is written only with sign-in off")
	}
	d.Transport = "pw"
	if !d.WritableBy("ada", true) {
		t.Fatal("over pw ssh with sign-in on")
	}
}
