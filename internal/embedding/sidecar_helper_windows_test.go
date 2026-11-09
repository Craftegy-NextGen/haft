//go:build windows

package embedding

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

// The Windows fixtures for the shared embedding sidecar use the Go
// helper-process pattern (ADR-093 E6): the test binary is the fake haft-embed.
// The production code starts it with the real sidecar's arguments, so
// TestMain decides what the process is before the testing flags are parsed.
//
// Environment variables name the fixture behavior, and the presence of
// --serve-socket in the arguments names the daemon, so that a daemon started
// by a helper process is a daemon whatever role the helper had.

const (
	// helperModeEnv makes the process a fake sidecar that does not serve a
	// socket: "reject" exits 2 on --serve-socket like an old clap binary and
	// otherwise never speaks; "silent" starts and never speaks; "stdio"
	// serves the stdio protocol, as a per-process sidecar does.
	helperModeEnv = "HAFT_TEST_SIDECAR_MODE"
	// helperRoleEnv makes the process a haft client: "starter" connects or
	// starts the shared daemon and exits without closing; "client" does the
	// same and closes.
	helperRoleEnv = "HAFT_TEST_PROCESS_ROLE"
	// helperStartsEnv names a file to which every daemon appends its pid.
	helperStartsEnv = "HAFT_TEST_SHARED_SIDECAR_STARTS"

	helperResultPrefix = "DAEMON "
	helperSilence      = 30 * time.Second
)

func runHelperProcess(args []string) (int, bool) {
	switch os.Getenv(helperModeEnv) {
	case "reject":
		if helperArg(args, "--serve-socket") != "" {
			fmt.Fprintln(os.Stderr, "unexpected argument --serve-socket")
			return 2, true
		}
		time.Sleep(helperSilence)
		return 0, true
	case "silent":
		time.Sleep(helperSilence)
		return 0, true
	case "stdio":
		return serveHelperStdio(), true
	}
	if helperArg(args, "--serve-socket") != "" {
		return serveHelperSidecar(args), true
	}
	switch os.Getenv(helperRoleEnv) {
	case "starter":
		return runHelperClient(false), true
	case "client":
		return runHelperClient(true), true
	}
	return 0, false
}

// runHelperClient is a separate haft process: it reaches the shared sidecar
// the way production does and prints the pid of the daemon that answered (the
// fake daemon puts its pid in the first vector component).
func runHelperClient(closeAdapter bool) int {
	embedder, err := New(Config{Provider: ProviderLocal, Model: "fake", Dim: 2})
	if err != nil {
		fmt.Println("ERROR", err)
		return 1
	}
	vectors, err := embedder.Embed(context.Background(), RoleQuery, []string{"alpha"})
	if err != nil || len(vectors) != 1 || len(vectors[0]) != 2 {
		fmt.Println("ERROR", err, vectors)
		return 1
	}
	fmt.Println(helperResultPrefix + strconv.Itoa(int(vectors[0][0])))
	if closeAdapter {
		_ = embedder.Close()
	}
	return 0
}

func serveHelperSidecar(args []string) int {
	socket := helperArg(args, "--serve-socket")
	model := helperArg(args, "--model")
	dim := helperIntArg(args, "--dim", 2)
	idle := time.Duration(helperIntArg(args, "--idle-timeout-secs", 30)) * time.Second
	if model == "" {
		model = DefaultLocalModel
	}

	if logPath := os.Getenv(helperStartsEnv); logPath != "" {
		file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintln(file, os.Getpid())
			_ = file.Close()
		}
	}

	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		return 3
	}
	defer listener.Close()

	unixListener := listener.(*net.UnixListener)
	active := atomic.Int32{}
	lastAccept := time.Now()
	for {
		_ = unixListener.SetDeadline(time.Now().Add(100 * time.Millisecond))
		conn, err := unixListener.Accept()
		if err != nil {
			if active.Load() == 0 && time.Since(lastAccept) >= idle {
				return 0
			}
			continue
		}
		active.Add(1)
		lastAccept = time.Now()
		go serveHelperConn(conn, model, dim, &active)
	}
}

func serveHelperStdio() int {
	model := DefaultLocalModel
	dim := 2
	line, err := json.Marshal(sidecarHandshake{Ready: true, Model: model, Dim: dim})
	if err != nil {
		return 1
	}
	fmt.Println(string(line))
	reader := bufio.NewReader(os.Stdin)
	for {
		requestLine, err := reader.ReadBytes('\n')
		if err != nil {
			return 0
		}
		var request sidecarRequest
		if err := json.Unmarshal(requestLine, &request); err != nil {
			return 1
		}
		payload, err := json.Marshal(sidecarResponse{ID: request.ID, Vectors: helperVectors(len(request.Texts), dim)})
		if err != nil {
			return 1
		}
		fmt.Println(string(payload))
	}
}

func helperVectors(rows int, dim int) [][]float32 {
	vectors := make([][]float32, rows)
	for row := range vectors {
		vector := make([]float32, dim)
		if dim > 0 {
			vector[0] = float32(os.Getpid())
		}
		vectors[row] = vector
	}
	return vectors
}

func serveHelperConn(conn net.Conn, model string, dim int, active *atomic.Int32) {
	defer active.Add(-1)
	defer conn.Close()
	reader := bufio.NewReader(conn)
	line, err := json.Marshal(sidecarHandshake{Ready: true, Model: model, Dim: dim})
	if err != nil {
		return
	}
	_, _ = conn.Write(append(line, '\n'))

	for {
		requestLine, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var request sidecarRequest
		if err := json.Unmarshal(requestLine, &request); err != nil {
			return
		}
		payload, err := json.Marshal(sidecarResponse{ID: request.ID, Vectors: helperVectors(len(request.Texts), dim)})
		if err != nil {
			return
		}
		_, _ = conn.Write(append(payload, '\n'))
	}
}

func helperArg(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func helperIntArg(args []string, name string, fallback int) int {
	parsed, err := strconv.Atoi(helperArg(args, name))
	if err != nil {
		return fallback
	}
	return parsed
}
