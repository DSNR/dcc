package session

// Sever kills the Session's signaling connection out from under it, the way
// a vanished network path would. Both sides notice their end of the socket
// die, which is exactly the trigger the reconnect machinery answers; tests
// use it to make a blip on demand.
func (s *Session) Sever() {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
}
