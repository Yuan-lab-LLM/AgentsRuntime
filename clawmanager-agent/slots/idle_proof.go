package slots

// Called with worker.mu held. A stopped label is insufficient: lease release,
// auxiliary joins, network cleanup and any physical reset must have completed.
func (w *Worker) idleConfirmed() bool {
	if w.book == nil || w.book.quarantined || w.book.Maintenance != nil || w.lease != nil || w.workerEpoch == "" || w.cfg.PodUID == "" || w.manager.Health() != nil {
		return false
	}
	if a := w.book.Assignment; a != nil && a.State != "stopped" {
		return false
	}
	if (w.book.StopIntent != nil || w.book.TerminationProof != nil || w.book.FaultRecovery != nil) && !w.book.StopFinalized && !w.book.faultFinalized() {
		return false
	}
	if w.network != nil {
		w.network.mu.Lock()
		active := w.network.active != nil
		w.network.mu.Unlock()
		if active {
			return false
		}
	}
	return w.manager.ExecutionIdle()
}
