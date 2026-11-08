package platform

import (
	"os"
	"sync"
	"time"
)

// The chosen context is read again when access.json changes under the
// server: while Node still runs beside the Go server it can write the file
// too (access.ts chooseContext), and the two then agree on the context.

type choiceStamp struct {
	mod  time.Time
	size int64
	ok   bool
}

var choiceStamps sync.Map // *Platform -> choiceStamp

func (p *Platform) rereadChoice() {
	st, err := os.Stat(p.choiceFile())
	now := choiceStamp{}
	if err == nil {
		now = choiceStamp{mod: st.ModTime(), size: st.Size(), ok: true}
	}
	prev, seen := choiceStamps.Swap(p, now)
	if !seen || prev.(choiceStamp) == now {
		return
	}
	c := p.loadChoice()
	p.mu.Lock()
	moved := p.chosen != c
	p.chosen = c
	p.mu.Unlock()
	if moved {
		p.reach.reset()
	}
}
