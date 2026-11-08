package app

// NodeCaller is the caller for value requests to Node as it stands now
// (nil before AttachNode or without Node), for modules that resolve it per
// request rather than at Register time.
func (a *App) NodeCaller() *Node { return a.nodeAPI() }
