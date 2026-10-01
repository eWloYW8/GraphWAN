module github.com/eWloYW8/GraphWAN

go 1.26.0

// raft-boltdb imports legacy Bolt only for its migration helper. Reuse the
// maintained bbolt API so this unused helper does not exclude newer GOARCHs.
replace github.com/boltdb/bolt => ./internal/boltcompat

require (
	github.com/coder/websocket v1.8.15
	github.com/flynn/noise v1.1.0
	github.com/hashicorp/raft v1.8.0
	github.com/hashicorp/raft-boltdb/v2 v2.4.2
	github.com/hashicorp/yamux v0.1.2
	github.com/kardianos/service v1.2.4
	github.com/pion/stun/v3 v3.1.7
	github.com/quic-go/quic-go v0.63.0
	github.com/vishvananda/netlink v1.3.1
	go.etcd.io/bbolt v1.5.0
	golang.org/x/crypto v0.57.0
	golang.org/x/net v0.58.0
	golang.org/x/sys v0.48.0
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2
	golang.zx2c4.com/wireguard v0.0.0-20260522210424-ecfc5a8d5446
	golang.zx2c4.com/wireguard/windows v1.1.1
	google.golang.org/grpc v1.84.0
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/boltdb/bolt v1.3.1 // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/hashicorp/go-hclog v1.6.3 // indirect
	github.com/hashicorp/go-immutable-radix v1.3.1 // indirect
	github.com/hashicorp/go-metrics v0.7.0 // indirect
	github.com/hashicorp/go-msgpack/v2 v2.1.5 // indirect
	github.com/hashicorp/golang-lru v1.0.2 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/pion/dtls/v3 v3.1.5 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/transport/v4 v4.1.0 // indirect
	github.com/vishvananda/netns v0.0.5 // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260706201446-f0a921348800 // indirect
)
