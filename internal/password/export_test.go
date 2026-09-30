package password

// OccupySlots takes every hashing slot of h until the returned function is called.
func OccupySlots(h *Hasher) (release func()) {
	for range cap(h.slots) {
		h.slots <- struct{}{}
	}
	return func() {
		for range cap(h.slots) {
			<-h.slots
		}
	}
}
