package telegram

import "sync"

// pendingAsk is one in-flight Ask/AskBatch awaiting user input. cb carries button
// taps (buffered so deliverCallback never blocks the update loop); alter carries
// a single typed reply line after the user chose [alter…].
type pendingAsk struct {
	id    string
	cb    chan string
	alter chan string

	mu       sync.Mutex
	altering bool // true once the question is waiting for an alter line
}

// wantAlter marks this question as awaiting a typed reply line.
func (p *pendingAsk) wantAlter() {
	p.mu.Lock()
	p.altering = true
	p.mu.Unlock()
}

func (p *pendingAsk) isAltering() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.altering
}

func (p *pendingAsk) doneAltering() {
	p.mu.Lock()
	p.altering = false
	p.mu.Unlock()
}

// askRegistry routes inbound callbacks and alter replies to the Ask/AskBatch call
// that is waiting for them. It is safe for concurrent use: the update loop calls
// deliver*, while Ask goroutines register/unregister.
type askRegistry struct {
	mu      sync.Mutex
	pending map[string]*pendingAsk
}

func newAskRegistry() *askRegistry {
	return &askRegistry{pending: make(map[string]*pendingAsk)}
}

// register creates and stores a single-item pending question keyed by id.
func (r *askRegistry) register(id string) *pendingAsk {
	return r.registerBatch(id, 1)
}

// registerBatch creates a pending question sized for n items. The cb buffer is
// generous enough that a burst of taps (e.g. rapid per-item confirms) never
// blocks the update loop.
func (r *askRegistry) registerBatch(id string, n int) *pendingAsk {
	p := &pendingAsk{
		id:    id,
		cb:    make(chan string, n+1),
		alter: make(chan string, 1),
	}
	r.mu.Lock()
	r.pending[id] = p
	r.mu.Unlock()
	return p
}

func (r *askRegistry) unregister(id string) {
	r.mu.Lock()
	delete(r.pending, id)
	r.mu.Unlock()
}

// deliverCallback routes a tapped button's data to the owning question. It
// returns true if a waiting question consumed it. Never blocks.
func (r *askRegistry) deliverCallback(data string) bool {
	id := questionID(data)
	r.mu.Lock()
	p := r.pending[id]
	r.mu.Unlock()
	if p == nil {
		return false
	}
	select {
	case p.cb <- data:
		return true
	default:
		return false
	}
}

// deliverAlter routes a typed reply line to whichever question is currently
// awaiting an alter reply. It returns true if one consumed the line. If several
// were somehow awaiting, the first found wins; in practice gates are serialized.
func (r *askRegistry) deliverAlter(line string) bool {
	r.mu.Lock()
	var target *pendingAsk
	for _, p := range r.pending {
		if p.isAltering() {
			target = p
			break
		}
	}
	r.mu.Unlock()
	if target == nil {
		return false
	}
	target.doneAltering()
	select {
	case target.alter <- line:
		return true
	default:
		return false
	}
}
