package vector

// lockMutation starts a write that may change any document.
func (s *mutationFencedStore) lockMutation() {
	s.mu.Lock()
	s.generation++
	s.stored.clear()
}

// lockMutationOf starts a write of these documents only.
func (s *mutationFencedStore) lockMutationOf(docs []VectorDocument) {
	s.mu.Lock()
	s.generation++
	s.stored.forget(docs)
}
