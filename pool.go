package rawhttp

import (
	"net"
	"sync"
	"time"
)

const (
	DefaultMaxIdleConnsPerHost = 5
	DefaultIdleTimeout         = 90 * time.Second
)

// ConnPool manages a pool of idle connections for reuse.
// It is safe for concurrent use.
type ConnPool struct {
	mu             sync.Mutex
	conns          map[string][]*pooledConn // key: "scheme://host:port"
	maxIdlePerHost int
	idleTimeout    time.Duration
	closed         bool
}

// pooledConn wraps a net.Conn with metadata for pool management.
type pooledConn struct {
	conn   net.Conn
	idleAt time.Time
}

// NewConnPool creates a new connection pool with the specified limits.
// If maxIdlePerHost <= 0, DefaultMaxIdleConnsPerHost is used.
// If idleTimeout <= 0, DefaultIdleTimeout is used.
func NewConnPool(maxIdlePerHost int, idleTimeout time.Duration) *ConnPool {
	if maxIdlePerHost <= 0 {
		maxIdlePerHost = DefaultMaxIdleConnsPerHost
	}
	if idleTimeout <= 0 {
		idleTimeout = DefaultIdleTimeout
	}
	return &ConnPool{
		conns:          make(map[string][]*pooledConn),
		maxIdlePerHost: maxIdlePerHost,
		idleTimeout:    idleTimeout,
	}
}

// NewDefaultConnPool creates a connection pool with default settings.
func NewDefaultConnPool() *ConnPool {
	return NewConnPool(DefaultMaxIdleConnsPerHost, DefaultIdleTimeout)
}

// Get retrieves an idle connection for the given key, or returns nil if none available.
// The key should be in the format "scheme://host:port" (e.g., "https://example.com:443").
func (p *ConnPool) Get(key string) net.Conn {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}

	// Clean up expired connections for this key
	p.cleanupKeyLocked(key)

	conns := p.conns[key]
	if len(conns) == 0 {
		return nil
	}

	// Get the most recently used connection (LIFO for better cache locality)
	n := len(conns) - 1
	pc := conns[n]
	p.conns[key] = conns[:n]

	return pc.conn
}

// Put returns a connection to the pool for future reuse.
// Returns true if the connection was added to the pool, false if it was rejected
// (pool full, closed, or connection nil).
// The caller should close the connection if Put returns false.
func (p *ConnPool) Put(key string, conn net.Conn) bool {
	if conn == nil {
		return false
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return false
	}

	// Clean up expired connections for this key
	p.cleanupKeyLocked(key)

	conns := p.conns[key]
	if len(conns) >= p.maxIdlePerHost {
		// Pool is full for this host
		return false
	}

	p.conns[key] = append(conns, &pooledConn{
		conn:   conn,
		idleAt: time.Now(),
	})

	return true
}

// CloseAll closes all idle connections in the pool and marks the pool as closed.
// After calling CloseAll, the pool will reject new connections.
func (p *ConnPool) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.closed = true

	for key, conns := range p.conns {
		for _, pc := range conns {
			pc.conn.Close()
		}
		delete(p.conns, key)
	}
}

// CloseIdle closes all idle connections but keeps the pool open for new connections.
func (p *ConnPool) CloseIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for key, conns := range p.conns {
		for _, pc := range conns {
			pc.conn.Close()
		}
		delete(p.conns, key)
	}
}

// Len returns the total number of idle connections in the pool.
func (p *ConnPool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	total := 0
	for _, conns := range p.conns {
		total += len(conns)
	}
	return total
}

// LenForHost returns the number of idle connections for a specific host key.
func (p *ConnPool) LenForHost(key string) int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return len(p.conns[key])
}

// cleanupKeyLocked removes expired connections for a specific key.
// Must be called with p.mu held.
func (p *ConnPool) cleanupKeyLocked(key string) {
	conns := p.conns[key]
	if len(conns) == 0 {
		return
	}

	now := time.Now()
	valid := conns[:0] // reuse backing array

	for _, pc := range conns {
		if now.Sub(pc.idleAt) < p.idleTimeout {
			valid = append(valid, pc)
		} else {
			pc.conn.Close()
		}
	}

	if len(valid) == 0 {
		delete(p.conns, key)
	} else {
		p.conns[key] = valid
	}
}

// PoolKey generates a pool key from scheme, host, and port.
func PoolKey(scheme, host, port string) string {
	return scheme + "://" + host + ":" + port
}
