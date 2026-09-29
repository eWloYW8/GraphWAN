package model

// DefaultSTUNServers is copied into each newly enrolled Agent's desired state.
// Later edits, including an empty list to disable discovery, remain authoritative.
// UDP and TCP observations must be obtained independently from their data sockets.
func DefaultSTUNServers() []string {
	return []string{
		"stun.miwifi.com:3478",
		"stun.cloudflare.com:3478",
		"stun.nextcloud.com:443",
		"tcp://stun.nextcloud.com:443",
	}
}
