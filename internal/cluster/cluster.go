package cluster

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

type SettlementNode struct {
	ID        string
	IsLeader  bool
	Crashed   bool
	Log       []string
	CommitIdx int
	mu        sync.RWMutex
}

type NodeStatus struct {
	ID        string `json:"id"`
	IsLeader  bool   `json:"is_leader"`
	Crashed   bool   `json:"crashed"`
	LogSize   int    `json:"log_size"`
	CommitIdx int    `json:"commit_idx"`
}

type ClusterStatus struct {
	LeaderID string                 `json:"leader_id"`
	Nodes    map[string]*NodeStatus `json:"nodes"`
}

type Cluster struct {
	Nodes    map[string]*SettlementNode
	LeaderID string
	mu       sync.RWMutex
}

func (c *Cluster) GetStatus() *ClusterStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()

	status := &ClusterStatus{
		LeaderID: c.LeaderID,
		Nodes:    make(map[string]*NodeStatus),
	}

	for id, node := range c.Nodes {
		node.mu.RLock()
		status.Nodes[id] = &NodeStatus{
			ID:        node.ID,
			IsLeader:  node.IsLeader,
			Crashed:   node.Crashed,
			LogSize:   len(node.Log),
			CommitIdx: node.CommitIdx,
		}
		node.mu.RUnlock()
	}

	return status
}

func NewCluster() *Cluster {
	c := &Cluster{
		Nodes:    make(map[string]*SettlementNode),
		LeaderID: "node-a", // node-a is default leader
	}

	c.Nodes["node-a"] = &SettlementNode{ID: "node-a", IsLeader: true}
	c.Nodes["node-b"] = &SettlementNode{ID: "node-b", IsLeader: false}
	c.Nodes["node-c"] = &SettlementNode{ID: "node-c", IsLeader: false}

	return c
}

// ProposeCommand replicates a transaction command across the cluster. Requires quorum of 2/3 nodes.
func (c *Cluster) ProposeCommand(ctx context.Context, cmd string) error {
	c.mu.RLock()
	leader := c.Nodes[c.LeaderID]
	c.mu.RUnlock()

	leader.mu.RLock()
	if leader.Crashed || !leader.IsLeader {
		leader.mu.RUnlock()
		return errors.New("cluster leader unavailable, election in progress")
	}
	leader.mu.RUnlock()

	slog.Info("cluster leader received command proposal", "leader", leader.ID, "command", cmd)

	// Leader appends to its own log
	leader.mu.Lock()
	leader.Log = append(leader.Log, cmd)
	logIdx := len(leader.Log) - 1
	leader.mu.Unlock()

	// Replicate to followers
	var wg sync.WaitGroup
	acks := 1 // Leader acknowledges its own append
	var ackMu sync.Mutex

	c.mu.RLock()
	for id, node := range c.Nodes {
		if id == c.LeaderID {
			continue
		}
		wg.Add(1)
		go func(n *SettlementNode) {
			defer wg.Done()
			n.mu.Lock()
			defer n.mu.Unlock()

			if n.Crashed {
				return
			}

			// Simulate replication delay
			time.Sleep(10 * time.Millisecond)

			n.Log = append(n.Log, cmd)
			n.CommitIdx = logIdx

			ackMu.Lock()
			acks++
			ackMu.Unlock()
		}(node)
	}
	c.mu.RUnlock()

	// Wait for replication attempts
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
	}

	// Quorum check (majority of 3 is 2)
	if acks >= 2 {
		leader.mu.Lock()
		leader.CommitIdx = logIdx
		leader.mu.Unlock()
		slog.Info("command committed on quorum", "leader", leader.ID, "acks", acks, "log_idx", logIdx)
		return nil
	}

	return errors.New("raft replication failed: failed to reach quorum")
}

// CrashNode simulates a node crashing (e.g. leader crash)
func (c *Cluster) CrashNode(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	node, exists := c.Nodes[id]
	if exists {
		node.mu.Lock()
		node.Crashed = true
		node.mu.Unlock()
		slog.Warn("node crashed", "node_id", id)

		// If leader crashed, trigger automatic failover election
		if id == c.LeaderID {
			go c.runElection()
		}
	}
}

// RecoverNode recovers a crashed node and synchronizes its log
func (c *Cluster) RecoverNode(id string) {
	c.mu.Lock()
	node, exists := c.Nodes[id]
	c.mu.Unlock()

	if exists {
		node.mu.Lock()
		node.Crashed = false
		node.mu.Unlock()
		slog.Info("node recovered, synchronizing log", "node_id", id)

		// Sync log from current leader
		c.mu.RLock()
		leader := c.Nodes[c.LeaderID]
		c.mu.RUnlock()

		leader.mu.RLock()
		leaderLog := make([]string, len(leader.Log))
		copy(leaderLog, leader.Log)
		leaderCommit := leader.CommitIdx
		leader.mu.RUnlock()

		node.mu.Lock()
		node.Log = leaderLog
		node.CommitIdx = leaderCommit
		node.mu.Unlock()
		slog.Info("node log synchronization complete", "node_id", id, "log_size", len(leaderLog))
	}
}

// Simulated election
func (c *Cluster) runElection() {
	slog.Info("leader lost: running election...")
	time.Sleep(100 * time.Millisecond) // election timeout simulation

	c.mu.Lock()
	defer c.mu.Unlock()

	// Find the active node with the longest log
	var bestCandidate *SettlementNode
	maxLogSize := -1

	for _, node := range c.Nodes {
		node.mu.RLock()
		if !node.Crashed && len(node.Log) > maxLogSize {
			maxLogSize = len(node.Log)
			bestCandidate = node
		}
		// Reset leader flag for all nodes
		node.mu.RUnlock()
		node.mu.Lock()
		node.IsLeader = false
		node.mu.Unlock()
	}

	if bestCandidate != nil {
		bestCandidate.mu.Lock()
		bestCandidate.IsLeader = true
		bestCandidate.mu.Unlock()

		c.LeaderID = bestCandidate.ID
		slog.Info("new leader elected successfully", "leader_id", bestCandidate.ID, "log_size", len(bestCandidate.Log))
	} else {
		slog.Error("election failed: no active nodes available")
	}
}
