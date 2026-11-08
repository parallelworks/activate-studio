package tasks

import (
	"sync/atomic"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsites"
)

// The engine and agent sites Register made, for the chat's delegate,
// task_status and agent_* tools (internal/chat).
var (
	currentEngine atomic.Pointer[Engine]
	currentSites  atomic.Pointer[agentsites.Sites]
)

// CurrentEngine is the registered task engine, or nil.
func CurrentEngine() *Engine { return currentEngine.Load() }

// CurrentSites is the registered agent sites, or nil.
func CurrentSites() *agentsites.Sites { return currentSites.Load() }
