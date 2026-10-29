package gateway

import (
	"fmt"
	"net"
	"sync"
)

type portStatus string

const (
	portReserved portStatus = "reserved"
	portRunning  portStatus = "running"
)

type portAllocation struct {
	InstanceID  int
	Generation  int
	PrimaryPort int
	Status      portStatus
}

type portBlockConflict struct {
	Port   int
	Reason string
}

func (c portBlockConflict) Error() string {
	return fmt.Sprintf("port block member %d is already %s", c.Port, c.Reason)
}

type PortAllocator struct {
	mu          sync.Mutex
	ports       map[int]portAllocation
	isListening func(int) bool
	blockSize   int
}

func NewPortAllocator(isListening func(int) bool) *PortAllocator {
	if isListening == nil {
		isListening = isTCPPortListening
	}
	return &PortAllocator{
		ports:       map[int]portAllocation{},
		isListening: isListening,
		blockSize:   1,
	}
}

func (a *PortAllocator) SetBlockSize(size int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if size <= 0 {
		size = 1
	}
	a.blockSize = size
}

func (a *PortAllocator) Reserve(instanceID, generation int, rng PortRange) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if rng.Start <= 0 || rng.End < rng.Start {
		return 0, fmt.Errorf("invalid port range %d-%d", rng.Start, rng.End)
	}
	blockSize := a.blockSize
	if blockSize <= 0 {
		blockSize = 1
	}
	lastPrimaryPort := rng.End - blockSize + 1
	for port := rng.Start; port <= lastPrimaryPort; port += blockSize {
		if conflict := a.blockConflictLocked(port, blockSize); conflict != nil {
			continue
		}
		allocation := portAllocation{
			InstanceID:  instanceID,
			Generation:  generation,
			PrimaryPort: port,
			Status:      portReserved,
		}
		for offset := 0; offset < blockSize; offset++ {
			a.ports[port+offset] = allocation
		}
		return port, nil
	}
	return 0, ErrNoFreePort
}

// ReserveExact reserves the supplied primary gateway port and its associated
// port block. It intentionally does not constrain the port to a locally
// configured range: the control plane owns exact port assignment.
func (a *PortAllocator) ReserveExact(instanceID, generation, port int) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	blockSize := a.blockSize
	if blockSize <= 0 {
		blockSize = 1
	}
	if port <= 0 || port > 65535 || port+blockSize-1 > 65535 {
		return 0, fmt.Errorf("invalid requested gateway port %d", port)
	}
	if conflict := a.blockConflictLocked(port, blockSize); conflict != nil {
		return 0, fmt.Errorf("requested gateway port %d is unavailable: %w: %w", port, conflict, ErrNoFreePort)
	}

	allocation := portAllocation{
		InstanceID:  instanceID,
		Generation:  generation,
		PrimaryPort: port,
		Status:      portReserved,
	}
	for offset := 0; offset < blockSize; offset++ {
		a.ports[port+offset] = allocation
	}
	return port, nil
}

func (a *PortAllocator) blockConflictLocked(port, blockSize int) *portBlockConflict {
	for offset := 0; offset < blockSize; offset++ {
		member := port + offset
		if _, exists := a.ports[member]; exists {
			return &portBlockConflict{Port: member, Reason: "reserved by AgentRuntime"}
		}
		if a.isListening(member) {
			return &portBlockConflict{Port: member, Reason: "listening"}
		}
	}
	return nil
}

func (a *PortAllocator) Commit(instanceID, generation, port int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	allocation, exists := a.ports[port]
	if !exists {
		return
	}
	if allocation.InstanceID != instanceID || allocation.Generation != generation {
		return
	}
	for member, memberAllocation := range a.ports {
		if memberAllocation.PrimaryPort != port {
			continue
		}
		memberAllocation.Status = portRunning
		a.ports[member] = memberAllocation
	}
}

func (a *PortAllocator) Release(port int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for member, allocation := range a.ports {
		if allocation.PrimaryPort == port {
			delete(a.ports, member)
		}
	}
}

func (a *PortAllocator) ListUsed() []int {
	a.mu.Lock()
	defer a.mu.Unlock()

	used := make([]int, 0, len(a.ports))
	for port := range a.ports {
		used = append(used, port)
	}
	return used
}

func isTCPPortListening(port int) bool {
	listener, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return true
	}
	_ = listener.Close()
	return false
}
