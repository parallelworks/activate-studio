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

func (p *Platform) choiceStampNow() choiceStamp {
	st, err := os.Stat(p.choiceFile())
	if err != nil {
		return choiceStamp{}
	}
	return choiceStamp{mod: st.ModTime(), size: st.Size(), ok: true}
}

// markChoiceRead records the file as read now (at start), so a change
// after it is read again.
func (p *Platform) markChoiceRead() { choiceStamps.Store(p, p.choiceStampNow()) }

func (p *Platform) rereadChoice() {
	now := p.choiceStampNow()
	prev, seen := choiceStamps.Swap(p, now)
	if !seen || prev.(choiceStamp) == now {
		return
	}
	c, stopped := p.loadChoice()
	p.mu.Lock()
	moved := p.chosen != c || p.stopped != stopped
	p.chosen, p.stopped = c, stopped
	p.mu.Unlock()
	if moved {
		p.reach.reset()
	}
}
