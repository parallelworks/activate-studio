package conversations

import (
	"net/http"
	"strings"
	"unicode"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

// Who is the request's person as the routes need it.
type Who struct {
	// UserID and Username are req.user's, '' without a verified user.
	UserID, Username string
	// ViewerID is viewerOf(req)?.id: 'local' with sign-in off.
	ViewerID string
	// Admin: they administer this Studio, with sign-in on.
	Admin bool
}

// Routes are the conversation routes over a store.
type Routes struct {
	Store         *Store
	AuthEnabled   func() bool
	SharedHistory func() bool
	Held          func(r *http.Request, conversationID string) []Holder
	// Who identifies the request; ok false hands it to Node, which knows
	// the viewer where this server does not.
	Who func(r *http.Request) (Who, bool)
	// Renamed, when set, gives a title a person gave a chat to the pw code
	// session it runs in, and says why the session did not take it, or "".
	Renamed func(r *http.Request, c *Conversation) string
	// ToNode passes a request on.
	ToNode func(w http.ResponseWriter, r *http.Request, why string)
}

// VisibleTo says whether who may read c. One with no owner, made before
// sign-in was turned on or by a visitor who had not signed in, is its
// administrators' where chats are not shared.
func (rt *Routes) VisibleTo(c *Conversation, who Who) bool {
	// A chat that drives a pw code session on another of its owner's
	// machines holds that session's turns, copied from their machine: it
	// is theirs alone, even where chat history is shared.
	relayed := c.PWCode != nil && c.PWCode.Relay
	if !rt.AuthEnabled() || rt.SharedHistory() && !relayed {
		return true
	}
	return rt.MayChange(c, who)
}

// FindVisible is conversation id as who may read it.
func (rt *Routes) FindVisible(id string, who Who) (*Conversation, error) {
	c := rt.Store.Get(id)
	if c == nil || !rt.VisibleTo(c, who) {
		return nil, apierr.New(404, "conversation not found")
	}
	return c, nil
}

// OnlyOwner is the refusal of a change to a chat the person may read and
// whose it is not.
const OnlyOwner = "Only the person whose chat this is can change it."

// OnlyAdmins is the refusal of a change to a chat with no owner.
const OnlyAdmins = "Only an administrator of this Studio can change a chat that has no owner."

// SignInToChat is the refusal of a chat to a visitor who has not signed in:
// it would have no owner, so the visitor could not see it.
const SignInToChat = "Sign in to chat. On a Studio with sign-in, each chat belongs to the person who started it."

// MayChange says whether who may change c. With sign-in that is its owner
// alone, shared history or not and whoever administers the Studio: others
// read it. One with no owner is its administrators'.
func (rt *Routes) MayChange(c *Conversation, who Who) bool {
	if !rt.AuthEnabled() {
		return true
	}
	if c.OwnerName() == "" {
		return who.Admin
	}
	return who.Username == c.OwnerName()
}

// FindOwn is conversation id to be changed by who: not found where they may
// not see it, refused where they may only read it.
func (rt *Routes) FindOwn(id string, who Who) (*Conversation, error) {
	c, err := rt.FindVisible(id, who)
	if err != nil {
		return nil, err
	}
	if !rt.MayChange(c, who) {
		if c.OwnerName() == "" {
			return nil, apierr.New(403, OnlyAdmins)
		}
		return nil, apierr.New(403, OnlyOwner)
	}
	return c, nil
}

// Summary is summary(c); own says whether the person it is for may change it.
func Summary(c *Conversation, own bool) *jsonobj.Object {
	o := reqbody.O("id", c.ID)
	if c.Title != nil {
		reqbody.Put(o, "title", *c.Title)
	}
	var owner any
	if c.Owner != nil {
		owner = *c.Owner
	}
	reqbody.Put(o, "owner", owner)
	reqbody.Put(o, "messageCount", len(c.Messages))
	reqbody.Put(o, "createdAt", c.CreatedAt)
	reqbody.Put(o, "updatedAt", c.UpdatedAt)
	reqbody.Put(o, "isOwner", own)
	reqbody.Put(o, "canCollaborate", own)
	return o
}

// Register adds the seven routes.
func (rt *Routes) Register(mux *http.ServeMux, h func(pattern string, fn func(http.ResponseWriter, *http.Request) error)) {
	h("GET /api/chat/conversations", rt.list)
	h("GET /api/chat/conversations/{id}", rt.get)
	h("POST /api/chat/conversations", rt.create)
	h("PATCH /api/chat/conversations/{id}", rt.patch)
	h("POST /api/chat/steer", rt.steer)
	h("POST /api/chat/conversations/delete", rt.bulkDelete)
	h("DELETE /api/chat/conversations/{id}", rt.deleteOne)
}

func (rt *Routes) who(w http.ResponseWriter, r *http.Request) (Who, bool) {
	who, ok := rt.Who(r)
	if !ok {
		rt.ToNode(w, r, "the viewer is identified by Node")
	}
	return who, ok
}

func (rt *Routes) list(w http.ResponseWriter, r *http.Request) error {
	who, ok := rt.who(w, r)
	if !ok {
		return nil
	}
	out := []*jsonobj.Object{}
	for _, c := range Sorted(rt.Store.List()) {
		if rt.VisibleTo(c, who) {
			out = append(out, Summary(c, rt.MayChange(c, who)))
		}
	}
	return reqbody.OK(w, out)
}

func (rt *Routes) get(w http.ResponseWriter, r *http.Request) error {
	who, ok := rt.who(w, r)
	if !ok {
		return nil
	}
	c, err := rt.FindVisible(r.PathValue("id"), who)
	if err != nil {
		return err
	}
	var held []Holder
	if rt.Held != nil {
		held = rt.Held(r, c.ID)
	}
	own := rt.MayChange(c, who)
	c = WithOwnCommands(WithHolders(rt.Store.WithUnanswered(c), held, who.ViewerID), who.ViewerID, rt.AuthEnabled())
	o := reqbody.Parse(c)
	// What answers it, for one stored before that was kept too.
	if e := c.AnsweredBy(); e != "" {
		reqbody.Put(o, "engine", e)
	}
	reqbody.Put(o, "isOwner", own)
	reqbody.Put(o, "canCollaborate", own)
	return reqbody.OK(w, o)
}

func (rt *Routes) create(w http.ResponseWriter, r *http.Request) error {
	who, ok := rt.who(w, r)
	if !ok {
		return nil
	}
	b, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	if rt.AuthEnabled() && who.Username == "" {
		return apierr.New(401, SignInToChat)
	}
	var title *string
	if t, has := b.M()["title"]; has && t != nil {
		// title ?? null: a non-string title is stored as given by Node; a
		// string is the case the client sends
		if s, isStr := t.(string); isStr {
			title = &s
		} else {
			s := reqbody.JSString(t)
			title = &s
		}
	}
	return reqbody.OK(w, Summary(rt.Store.Create(title, who.Username), true))
}

func (rt *Routes) patch(w http.ResponseWriter, r *http.Request) error {
	who, ok := rt.who(w, r)
	if !ok {
		return nil
	}
	if _, err := rt.FindOwn(r.PathValue("id"), who); err != nil {
		return err
	}
	b, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	var title *string
	if s := b.Str("title"); s != "" {
		title = &s
	}
	c := rt.Store.Rename(r.PathValue("id"), title)
	if c == nil {
		return apierr.New(404, "conversation not found")
	}
	o := Summary(c, true)
	if rt.Renamed != nil && title != nil {
		if why := rt.Renamed(r, c); why != "" {
			reqbody.Put(o, "sessionKeeps", why)
		}
	}
	return reqbody.OK(w, o)
}

func (rt *Routes) steer(w http.ResponseWriter, r *http.Request) error {
	who, ok := rt.who(w, r)
	if !ok {
		return nil
	}
	b, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	conv := b.Get("conversationId")
	id := b.Str("id")
	text := jsTrim(b.Str("text"))
	if !reqbody.Truthy(conv) || id == "" || text == "" {
		return apierr.New(400, "conversationId, id, and text are required")
	}
	cid := reqbody.JSString(conv)
	if _, err := rt.FindOwn(cid, who); err != nil {
		return err
	}
	return reqbody.OK(w, reqbody.O("queued", rt.Store.QueueSteer(cid, id, sliceRunes(text, 4000))))
}

func (rt *Routes) bulkDelete(w http.ResponseWriter, r *http.Request) error {
	who, ok := rt.who(w, r)
	if !ok {
		return nil
	}
	b, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	raw, isList := b.Get("ids").([]any)
	if !isList || len(raw) == 0 {
		return apierr.New(400, "ids must be a non-empty list")
	}
	if len(raw) > 1000 {
		raw = raw[:1000]
	}
	ids := map[string]bool{}
	var order []string
	for _, x := range raw {
		s := reqbody.JSString(x)
		if x == nil {
			s = "null"
		}
		if !ids[s] {
			ids[s] = true
			order = append(order, s)
		}
	}
	gone := rt.Store.Delete(ids, func(c *Conversation) bool {
		return rt.VisibleTo(c, who) && rt.MayChange(c, who)
	})
	goneSet := map[string]bool{}
	for _, id := range gone {
		goneSet[id] = true
	}
	skipped := []string{}
	for _, id := range order {
		if !goneSet[id] {
			skipped = append(skipped, id)
		}
	}
	if gone == nil {
		gone = []string{}
	}
	return reqbody.OK(w, reqbody.O("deleted", gone, "skipped", skipped))
}

func (rt *Routes) deleteOne(w http.ResponseWriter, r *http.Request) error {
	who, ok := rt.who(w, r)
	if !ok {
		return nil
	}
	id := r.PathValue("id")
	// With sign-in, one that is not there is answered as another person's.
	if _, err := rt.FindOwn(id, who); err != nil && (rt.AuthEnabled() || rt.Store.Get(id) != nil) {
		return err
	}
	rt.Store.Delete(map[string]bool{id: true}, func(c *Conversation) bool { return rt.MayChange(c, who) })
	return reqbody.OK(w, reqbody.O("deleted", id))
}

func jsTrim(s string) string {
	return trimSpace(s)
}

// trimSpace is String.prototype.trim: whitespace and the BOM.
func trimSpace(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == 0xfeff })
}
