package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// onlineStudio is setupStudio with three of Bob's clusters as the
// platform's pw code listing gives them: one on with its pw code connected,
// one stopped, and one running whose pw code is not connected.
func onlineStudio(t *testing.T) (*Module, *fakeMachine, string, map[string]bridgeMach) {
	t.Helper()
	m, f, id := setupStudio(t)
	machines := map[string]bridgeMach{
		"m-bob":  {ID: "m-bob", Kind: "cloud", Name: "bobs-cluster", Owner: "bob", Status: "connected"},
		"m-off":  {ID: "m-off", Kind: "cloud", Name: "stopped-cluster", Owner: "bob", Status: "unreachable"},
		"m-idle": {ID: "m-idle", Kind: "existing", Name: "idle-cluster", Owner: "bob", Status: "no_daemon"},
	}
	m.bridgeMachineFn = func(r *http.Request, id string) (bridgeMach, error) {
		if mach, ok := machines[id]; ok {
			return mach, nil
		}
		return bridgeMach{}, platform.KbError(404, "not yours")
	}
	m.bridgeMachinesFn = func(r *http.Request) ([]bridgeMach, error) {
		return []bridgeMach{machines["m-off"], machines["m-idle"], machines["m-bob"]}, nil
	}
	return m, f, id, machines
}

func listOffers(t *testing.T, m *Module) map[string]bridgeOfferT {
	t.Helper()
	w := httptest.NewRecorder()
	if err := m.bridgeMachinesRoute(w, setupReq(bobProxy)); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Machines []bridgeOfferT `json:"machines"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	got := map[string]bridgeOfferT{}
	for _, o := range out.Machines {
		got[o.MachineID] = o
	}
	return got
}

// The Code page offered the Studio's tools on a cluster that was not
// connected. Only one that is on, with its pw code connected (online as the
// Code page shows it), is offered; one that comes online later is offered
// then.
func TestOnlyAConnectedMachineIsOffered(t *testing.T) {
	m, _, _, machines := onlineStudio(t)
	got := listOffers(t, m)
	if o := got["m-bob"]; o.State != "offer" || !o.Available {
		t.Errorf("online: %+v", o)
	}
	for id, name := range map[string]string{"m-off": "stopped-cluster", "m-idle": "idle-cluster"} {
		if o := got[id]; o.State != "unavailable" || o.Available || o.Why != name+" is not connected. Start it, then add the tools." {
			t.Errorf("%s: %+v", id, o)
		}
	}
	mach := machines["m-idle"]
	mach.Status = "connected"
	machines["m-idle"] = mach
	if o := listOffers(t, m)["m-idle"]; o.State != "offer" || !o.Available {
		t.Errorf("online later: %+v", o)
	}
}

// Add on a machine that went offline between the listing and the click
// says so, and never opens pw ssh to it: from the Code page's offer, and
// from a chat's setup card.
func TestAddOnAMachineNotConnectedSaysSoWithoutPwSSH(t *testing.T) {
	m, f, id, machines := onlineStudio(t)
	status, out := setupAs(t, m, bobProxy, `{"machineId":"m-off"}`)
	if status != 409 || out["error"] != "stopped-cluster is not connected. Start it, then add the tools." {
		t.Errorf("%d %v", status, out)
	}
	// The chat's machine, bobs-cluster, goes off.
	mach := machines["m-bob"]
	mach.Status = "no_agent"
	machines["m-bob"] = mach
	status, out = setupAs(t, m, bobProxy, `{"conversationId":"`+id+`"}`)
	if status != 409 || out["error"] != "bobs-cluster is not connected. Start it, then add the tools." {
		t.Errorf("chat: %d %v", status, out)
	}
	if len(f.calls) != 0 {
		t.Errorf("pw ssh was opened: %v", f.calls)
	}
	// The card offers no Set up for me, and says why.
	if o := m.bridgeSetupOffer(setupReq(bobProxy), id); o.Available || o.State != "unavailable" || o.Why != "bobs-cluster is not connected. Start it, then add the tools." {
		t.Errorf("card: %+v", o)
	}
}

// A machine set up already stays so while it is off, but is not checked
// again until it is on.
func TestASetUpMachineStaysSetWhileOff(t *testing.T) {
	m, f, _, machines := onlineStudio(t)
	if status, out := setupAs(t, m, bobProxy, `{"machineId":"m-bob"}`); status != 200 || out["worked"] != true {
		t.Fatalf("%d %v", status, out)
	}
	mach := machines["m-bob"]
	mach.Status = "unreachable"
	machines["m-bob"] = mach
	if o := listOffers(t, m)["m-bob"]; o.State != "set" || o.Available {
		t.Errorf("%+v", o)
	}
	n := len(f.calls)
	if status, _ := setupAs(t, m, bobProxy, `{"machineId":"m-bob","check":true}`); status != 409 || len(f.calls) != n {
		t.Errorf("checked while off: %d, %d calls", status, len(f.calls)-n)
	}
}
