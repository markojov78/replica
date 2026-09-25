package storage

import (
	"context"
	"log"
	"sync"
	"time"

	"replica/internal/apiclient"
)

const replicaRecoveryInterval = 30 * time.Second

func (r *Runtime) replicaWorkMutex(id uint) *sync.Mutex {
	mu, _ := r.replicaWork.LoadOrStore(id, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

func (r *Runtime) replicasSnapshot() []apiclient.Replica {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return append([]apiclient.Replica(nil), r.replicas...)
}

func (r *Runtime) recordReplicaFailure(replica apiclient.Replica, err error) {
	previous, loaded := r.replicaFailures.Swap(replica.ID, err.Error())
	if !loaded || previous != err.Error() {
		log.Printf("storage runtime replica unavailable; will retry replica_id=%d uri=%s error=%v", replica.ID, replica.URI, err)
	}
	r.stopReplicaWatcher(replica.ID)
}

func (r *Runtime) replicaRecoveryLoop(ctx context.Context) {
	ticker := time.NewTicker(replicaRecoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.retryReplicaInitialization(ctx)
		}
	}
}

func (r *Runtime) retryReplicaInitialization(ctx context.Context) {
	for _, replica := range r.replicasSnapshot() {
		if !replicaIsActive(replica) {
			r.replicaFailures.Delete(replica.ID)
			continue
		}
		_, failed := r.replicaFailures.Load(replica.ID)
		if replica.Type != "removable" && !failed && r.replicaWatcherExists(replica.ID) {
			continue
		}
		// One worker per replica at most. A blocked mount must not hold up the
		// heartbeat, other replicas, or accumulate workers on successive ticks.
		mu := r.replicaWorkMutex(replica.ID)
		if !mu.TryLock() {
			continue
		}
		go func(id uint) {
			defer mu.Unlock()
			r.recoverReplica(ctx, id)
		}(replica.ID)
	}
}

// Caller holds the replica work mutex, shared with scan/reconcile commands and
// watcher reports. Recovery only reports observations; the coordinator still
// decides and schedules all replication.
func (r *Runtime) recoverReplica(ctx context.Context, id uint) {
	replica, ok := r.findReplica(id)
	if !ok || !replicaIsActive(replica) || ctx.Err() != nil {
		return
	}
	if err := checkReplicaAvailable(replica); err != nil {
		r.recordReplicaFailure(replica, err)
		return
	}
	if _, err := r.refreshReplicaFiles(ctx, id); err != nil {
		r.recordReplicaFailure(replica, err)
		return
	}
	if err := r.reportReplicaLocalChanges(ctx, replica); err != nil {
		r.recordReplicaFailure(replica, err)
		return
	}
	if err := r.ensureReplicaWatcher(ctx, replica); err != nil {
		r.recordReplicaFailure(replica, err)
		return
	}
	if _, failed := r.replicaFailures.LoadAndDelete(id); failed {
		log.Printf("storage runtime replica recovered replica_id=%d uri=%s", id, replica.URI)
	}
}
