# Raft Protocol (Go)

A small, educational implementation of the Raft consensus protocol in Go. It includes:
- **Raft node**: leader election, heartbeats, log replication, and a simple in-memory state machine
- **CLI client**: sends commands to any node; automatically redirects to the leader

This is great for learning how Raft works end-to-end and for experimenting locally.

## Features
- **Leader election** with randomized election timeouts
- **Heartbeats** and **log replication** over Go `net/rpc`
- **Commit index** advancement based on majority replication
- **State machine** with simple integer operations per key: `set`, `increase`, `decrease`, `del`, `get`
- **Topology** query to inspect the current leader, term, and members
- **Client redirect**: reads/writes routed to the leader transparently by the CLI
- Structured logging via Uber `zap` to stdout or file

## Requirements
- Go 1.21+

## Quickstart: Run a 3‑node cluster
Open three terminals and run each node with its config:

```bash
go run cmd/main.go --config=example-config/node1.yml
```

```bash
go run cmd/main.go --config=example-config/node2.yml
```

```bash
go run cmd/main.go --config=example-config/node3.yml
```

Within ~0.5s, a leader should be elected. Debug logs in the terminal will show elections, heartbeats, and RPCs.

### Inspect cluster topology
From a fourth terminal:

```bash
go run cmd/cli/raft_cli.go -node-address localhost:7500 topology
```

You’ll get JSON showing `leaderID`, `currentTerm`, and `members`.

### Write and read data
You can send commands to any node; the CLI will redirect to the leader if needed.

```bash
# Set an initial value
go run cmd/cli/raft_cli.go -node-address localhost:7501 set counter 10

# Increment it
go run cmd/cli/raft_cli.go -node-address localhost:7502 increase counter

# Read it
go run cmd/cli/raft_cli.go -node-address localhost:7500 get counter
```

### Demonstrate failover
Stop the leader process (Ctrl+C). Within a few hundred milliseconds, another node becomes leader. Verify:

```bash
go run cmd/cli/raft_cli.go -node-address localhost:7501 topology
```

Writes and reads continue; followers will redirect to the new leader.

## CLI Usage

Binary-style usage (via `go run` here):

```bash
go run cmd/cli/raft_cli.go -node-address <host:port> <command> [key] [value]
```

- **node-address**: any node in the cluster (e.g., `localhost:7500`)
- **command**: one of `topology`, `set`, `increase`, `decrease`, `del`, `get`
- **key**: required for all except `topology`
- **value**: integer required only for `set`

Examples:

```bash
# Cluster view
go run cmd/cli/raft_cli.go -node-address localhost:7500 topology

# Set an integer value for a key
go run cmd/cli/raft_cli.go -node-address localhost:7500 set counter 42

# Atomic increments/decrements
go run cmd/cli/raft_cli.go -node-address localhost:7500 increase counter
go run cmd/cli/raft_cli.go -node-address localhost:7500 decrease counter

# Delete a key
go run cmd/cli/raft_cli.go -node-address localhost:7500 del counter

# Read value
go run cmd/cli/raft_cli.go -node-address localhost:7500 get counter
```

Client behavior:
- If the command hits a follower, the node responds with leader info.
- The CLI automatically follows the redirect and reissues the command to the leader.
- Responses are JSON and include fields like `success`, `result`, `redirect`, `leaderAddress`, `topology`.

## Configuration

Example: `example-config/node1.yml`

```yaml
network:
  host: localhost
  ip: 7500

group:
  name: mbp-local
  members:
    - localhost:7500
    - localhost:7501
    - localhost:7502

logging:
  level: -1   # debug -1, info 0, error 2
  # destination: <path to a file>
```

- `network.host:ip` identifies the node and serves as its RPC bind address
- `group.members` should list all nodes’ addresses in the cluster
- `logging.level` uses `-1` (debug), `0` (info), `2` (error)
- `logging.destination` optionally writes logs to a file instead of stdout

## How it works (internals)

- **RPCs**: `RequestVote` for elections; `AppendEntries` for heartbeats and log replication; `ClientCommand` for client reads/writes.
- **Election**: each node runs a randomized election timeout (~100–500ms) and becomes candidate on timeout; majority votes elect a leader.
- **Heartbeats**: leaders send periodic empty `AppendEntries` to maintain authority and share commit index.
- **Log replication**: leader appends client commands as log entries and replicates to followers, performing backoff on conflict.
- **Commit**: once a majority replicates an entry from the leader’s current term, the leader advances `CommitIndex` and applies entries to the state machine.
- **State machine**: an in-memory `map[string]int]` supporting `set`, `increase`, `decrease`, `del`, `get` per key.
- **Client redirect**: followers return the leader’s address; the CLI replays the command on the leader.

Relevant files:
- `cmd/main.go` — loads YAML config, initializes logging, starts the node RPC server
- `internal/raft/raft.go` — protocol logic: elections, heartbeats, replication, commit, state machine
- `cmd/cli/raft_cli.go` + `internal/cli/raft_cli.go` — CLI entry and RPC client with leader redirect
- `internal/types/*.go` — types for config, commands, logs, RPC args/response
- `internal/helpers/timer.go` — randomized election timeouts

## Troubleshooting
- If CLI shows `Redirecting to leader ...` repeatedly, ensure all nodes list the same `group.members` and are reachable.
- Port already in use: change `network.ip` for each node config.
- No leader elected: verify all three nodes are running and can connect (no firewall blocking local ports).
- Stale logs after crash: this demo uses in-memory state; restart resets node state.

## License
MIT
