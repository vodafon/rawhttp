package rawhttp

import (
	"net"
	"sync"
	"testing"
	"time"
)

func TestNewConnPool_Defaults(t *testing.T) {
	tests := []struct {
		name               string
		maxIdlePerHost     int
		idleTimeout        time.Duration
		wantMaxIdlePerHost int
		wantIdleTimeout    time.Duration
	}{
		{
			name:               "zero values use defaults",
			maxIdlePerHost:     0,
			idleTimeout:        0,
			wantMaxIdlePerHost: DefaultMaxIdleConnsPerHost,
			wantIdleTimeout:    DefaultIdleTimeout,
		},
		{
			name:               "negative values use defaults",
			maxIdlePerHost:     -1,
			idleTimeout:        -1,
			wantMaxIdlePerHost: DefaultMaxIdleConnsPerHost,
			wantIdleTimeout:    DefaultIdleTimeout,
		},
		{
			name:               "custom values preserved",
			maxIdlePerHost:     10,
			idleTimeout:        60 * time.Second,
			wantMaxIdlePerHost: 10,
			wantIdleTimeout:    60 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := NewConnPool(tt.maxIdlePerHost, tt.idleTimeout)

			if pool.maxIdlePerHost != tt.wantMaxIdlePerHost {
				t.Errorf("maxIdlePerHost = %d, want %d", pool.maxIdlePerHost, tt.wantMaxIdlePerHost)
			}

			if pool.idleTimeout != tt.wantIdleTimeout {
				t.Errorf("idleTimeout = %v, want %v", pool.idleTimeout, tt.wantIdleTimeout)
			}

			if pool.conns == nil {
				t.Error("conns map is nil")
			}
		})
	}
}

func TestNewDefaultConnPool(t *testing.T) {
	pool := NewDefaultConnPool()

	if pool.maxIdlePerHost != DefaultMaxIdleConnsPerHost {
		t.Errorf("maxIdlePerHost = %d, want %d", pool.maxIdlePerHost, DefaultMaxIdleConnsPerHost)
	}

	if pool.idleTimeout != DefaultIdleTimeout {
		t.Errorf("idleTimeout = %v, want %v", pool.idleTimeout, DefaultIdleTimeout)
	}
}

func TestConnPool_PutGet(t *testing.T) {
	pool := NewDefaultConnPool()
	key := "https://example.com:443"

	// Create mock connection using net.Pipe
	client, server := net.Pipe()
	defer server.Close()

	// Put connection
	ok := pool.Put(key, client)
	if !ok {
		t.Fatal("Put() returned false")
	}

	// Get connection
	conn := pool.Get(key)
	if conn == nil {
		t.Fatal("Get() returned nil")
	}

	if conn != client {
		t.Error("Get() returned different connection")
	}

	// Get again should return nil (pool empty)
	conn2 := pool.Get(key)
	if conn2 != nil {
		t.Error("Get() should return nil when pool is empty")
	}

	conn.Close()
}

func TestConnPool_LIFO(t *testing.T) {
	pool := NewConnPool(10, DefaultIdleTimeout)
	key := "https://example.com:443"

	// Create multiple mock connections
	conn1Client, conn1Server := net.Pipe()
	conn2Client, conn2Server := net.Pipe()
	conn3Client, conn3Server := net.Pipe()
	defer conn1Server.Close()
	defer conn2Server.Close()
	defer conn3Server.Close()

	// Put in order: 1, 2, 3
	pool.Put(key, conn1Client)
	pool.Put(key, conn2Client)
	pool.Put(key, conn3Client)

	// Get should return in LIFO order: 3, 2, 1
	got3 := pool.Get(key)
	got2 := pool.Get(key)
	got1 := pool.Get(key)

	if got3 != conn3Client {
		t.Error("LIFO order incorrect: first Get should return conn3")
	}
	if got2 != conn2Client {
		t.Error("LIFO order incorrect: second Get should return conn2")
	}
	if got1 != conn1Client {
		t.Error("LIFO order incorrect: third Get should return conn1")
	}

	got3.Close()
	got2.Close()
	got1.Close()
}

func TestConnPool_MaxPerHost(t *testing.T) {
	maxIdle := 2
	pool := NewConnPool(maxIdle, DefaultIdleTimeout)
	key := "https://example.com:443"

	// Create connections
	conn1Client, conn1Server := net.Pipe()
	conn2Client, conn2Server := net.Pipe()
	conn3Client, conn3Server := net.Pipe()
	defer conn1Server.Close()
	defer conn2Server.Close()
	defer conn3Server.Close()

	// Put first two should succeed
	if !pool.Put(key, conn1Client) {
		t.Error("Put(conn1) should succeed")
	}
	if !pool.Put(key, conn2Client) {
		t.Error("Put(conn2) should succeed")
	}

	// Third should be rejected (pool full)
	if pool.Put(key, conn3Client) {
		t.Error("Put(conn3) should fail when pool is full")
	}

	// Clean up
	pool.CloseAll()
	conn3Client.Close()
}

func TestConnPool_MultipleHosts(t *testing.T) {
	pool := NewDefaultConnPool()
	key1 := "https://example1.com:443"
	key2 := "https://example2.com:443"

	conn1Client, conn1Server := net.Pipe()
	conn2Client, conn2Server := net.Pipe()
	defer conn1Server.Close()
	defer conn2Server.Close()

	pool.Put(key1, conn1Client)
	pool.Put(key2, conn2Client)

	// Get from key1 should return conn1
	got1 := pool.Get(key1)
	if got1 != conn1Client {
		t.Error("Get(key1) returned wrong connection")
	}

	// Get from key2 should return conn2
	got2 := pool.Get(key2)
	if got2 != conn2Client {
		t.Error("Get(key2) returned wrong connection")
	}

	// Both should now be empty
	if pool.Get(key1) != nil {
		t.Error("key1 should be empty")
	}
	if pool.Get(key2) != nil {
		t.Error("key2 should be empty")
	}

	got1.Close()
	got2.Close()
}

func TestConnPool_Expiration(t *testing.T) {
	// Use very short timeout for testing
	pool := NewConnPool(5, 10*time.Millisecond)
	key := "https://example.com:443"

	connClient, connServer := net.Pipe()
	defer connServer.Close()

	pool.Put(key, connClient)

	// Wait for expiration
	time.Sleep(50 * time.Millisecond)

	// Get should return nil (connection expired)
	got := pool.Get(key)
	if got != nil {
		t.Error("Get() should return nil for expired connection")
		got.Close()
	}
}

func TestConnPool_CloseAll(t *testing.T) {
	pool := NewDefaultConnPool()
	key := "https://example.com:443"

	connClient, connServer := net.Pipe()
	defer connServer.Close()

	pool.Put(key, connClient)

	// CloseAll
	pool.CloseAll()

	// Pool should be marked closed
	if !pool.closed {
		t.Error("pool should be marked closed")
	}

	// Get should return nil
	if pool.Get(key) != nil {
		t.Error("Get() should return nil after CloseAll")
	}

	// Put should fail
	conn2Client, conn2Server := net.Pipe()
	defer conn2Server.Close()
	if pool.Put(key, conn2Client) {
		t.Error("Put() should fail after CloseAll")
	}
	conn2Client.Close()
}

func TestConnPool_CloseIdle(t *testing.T) {
	pool := NewDefaultConnPool()
	key := "https://example.com:443"

	conn1Client, conn1Server := net.Pipe()
	defer conn1Server.Close()

	pool.Put(key, conn1Client)

	// CloseIdle
	pool.CloseIdle()

	// Pool should NOT be marked closed
	if pool.closed {
		t.Error("pool should not be marked closed after CloseIdle")
	}

	// Get should return nil (connections were closed)
	if pool.Get(key) != nil {
		t.Error("Get() should return nil after CloseIdle")
	}

	// Put should still work
	conn2Client, conn2Server := net.Pipe()
	defer conn2Server.Close()
	if !pool.Put(key, conn2Client) {
		t.Error("Put() should succeed after CloseIdle")
	}

	pool.CloseAll()
}

func TestConnPool_Len(t *testing.T) {
	pool := NewConnPool(10, DefaultIdleTimeout)
	key1 := "https://example1.com:443"
	key2 := "https://example2.com:443"

	if pool.Len() != 0 {
		t.Errorf("Len() = %d, want 0", pool.Len())
	}

	conn1Client, conn1Server := net.Pipe()
	conn2Client, conn2Server := net.Pipe()
	conn3Client, conn3Server := net.Pipe()
	defer conn1Server.Close()
	defer conn2Server.Close()
	defer conn3Server.Close()

	pool.Put(key1, conn1Client)
	if pool.Len() != 1 {
		t.Errorf("Len() = %d, want 1", pool.Len())
	}

	pool.Put(key1, conn2Client)
	if pool.Len() != 2 {
		t.Errorf("Len() = %d, want 2", pool.Len())
	}

	pool.Put(key2, conn3Client)
	if pool.Len() != 3 {
		t.Errorf("Len() = %d, want 3", pool.Len())
	}

	pool.CloseAll()
}

func TestConnPool_LenForHost(t *testing.T) {
	pool := NewConnPool(10, DefaultIdleTimeout)
	key1 := "https://example1.com:443"
	key2 := "https://example2.com:443"

	if pool.LenForHost(key1) != 0 {
		t.Errorf("LenForHost(key1) = %d, want 0", pool.LenForHost(key1))
	}

	conn1Client, conn1Server := net.Pipe()
	conn2Client, conn2Server := net.Pipe()
	conn3Client, conn3Server := net.Pipe()
	defer conn1Server.Close()
	defer conn2Server.Close()
	defer conn3Server.Close()

	pool.Put(key1, conn1Client)
	pool.Put(key1, conn2Client)
	pool.Put(key2, conn3Client)

	if pool.LenForHost(key1) != 2 {
		t.Errorf("LenForHost(key1) = %d, want 2", pool.LenForHost(key1))
	}

	if pool.LenForHost(key2) != 1 {
		t.Errorf("LenForHost(key2) = %d, want 1", pool.LenForHost(key2))
	}

	pool.CloseAll()
}

func TestConnPool_PutNil(t *testing.T) {
	pool := NewDefaultConnPool()
	key := "https://example.com:443"

	if pool.Put(key, nil) {
		t.Error("Put(nil) should return false")
	}
}

func TestConnPool_GetFromClosed(t *testing.T) {
	pool := NewDefaultConnPool()
	pool.CloseAll()

	if pool.Get("any-key") != nil {
		t.Error("Get() from closed pool should return nil")
	}
}

func TestConnPool_Concurrent(t *testing.T) {
	pool := NewConnPool(100, DefaultIdleTimeout)
	key := "https://example.com:443"

	var wg sync.WaitGroup
	numGoroutines := 50

	// Concurrent puts and gets
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			connClient, connServer := net.Pipe()
			defer connServer.Close()

			if pool.Put(key, connClient) {
				// If put succeeded, try to get
				if got := pool.Get(key); got != nil {
					got.Close()
				}
			} else {
				connClient.Close()
			}
		}()
	}

	wg.Wait()
	pool.CloseAll()
}

func TestPoolKey(t *testing.T) {
	tests := []struct {
		scheme string
		host   string
		port   string
		want   string
	}{
		{
			scheme: "https",
			host:   "example.com",
			port:   "443",
			want:   "https://example.com:443",
		},
		{
			scheme: "http",
			host:   "example.com",
			port:   "80",
			want:   "http://example.com:80",
		},
		{
			scheme: "https",
			host:   "api.example.com",
			port:   "8443",
			want:   "https://api.example.com:8443",
		},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := PoolKey(tt.scheme, tt.host, tt.port); got != tt.want {
				t.Errorf("PoolKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConnPool_CleanupKeyLocked(t *testing.T) {
	// Use short timeout for testing
	pool := NewConnPool(10, 20*time.Millisecond)
	key := "https://example.com:443"

	// Add some connections
	conn1Client, conn1Server := net.Pipe()
	conn2Client, conn2Server := net.Pipe()
	defer conn1Server.Close()
	defer conn2Server.Close()

	pool.Put(key, conn1Client)

	// Wait a bit, then add another
	time.Sleep(30 * time.Millisecond)
	pool.Put(key, conn2Client)

	// First connection should be expired, second should still be valid
	// Get will trigger cleanup
	got := pool.Get(key)
	if got != conn2Client {
		t.Error("Get() should return the non-expired connection")
	}

	// Only one connection should have been available
	if pool.Get(key) != nil {
		t.Error("Pool should be empty after getting the one valid connection")
	}

	got.Close()
}
