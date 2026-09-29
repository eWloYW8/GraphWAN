package transport

// readBatch borrows only buffers already returned by the same kernel read. It
// never performs another blocking read to fill a batch or invalidate earlier
// entries. Callers must finish processing before asking for the next batch.
func (r *udpPacketReader) readBatch(packets []udpPacket) (int, error) {
	count := 0
	for count < len(packets) {
		raw, control, flags, remote, err := r.read()
		if err != nil {
			return count, err
		}
		packets[count] = udpPacket{raw: raw, control: control, flags: flags, remote: remote}
		count++
		if !r.buffered() {
			break
		}
	}
	return count, nil
}
