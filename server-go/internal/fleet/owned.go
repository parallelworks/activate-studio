package fleet

// SessionsOf is the pw code sessions of the standing agents that act for
// the person with this user id.
func (f *Fleet) SessionsOf(userID string) []string {
	out := []string{}
	if userID == "" {
		return out
	}
	for _, a := range f.List() {
		if a.OwnerUser != nil && a.OwnerUser.ID == userID && a.Session != "" {
			out = append(out, a.Session)
		}
	}
	return out
}
