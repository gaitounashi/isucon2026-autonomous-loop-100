package main

import (
	"log"
	"net"
	"net/http"
	"os"

	"example.com/isureal-company/isureal"
)

// 2026-09-23

const defaultSocketPath = "/home/isucon/webapp/isureal.sock"

func main() {
	config := isureal.ConfigFromEnv()
	server := isureal.NewServer(config)
	// nginx connects through a Unix domain socket; TCP :8080 stays available as a fallback.
	socketPath, ok := os.LookupEnv("ISUREAL_SOCKET")
	if !ok {
		socketPath = defaultSocketPath
	}
	if socketPath != "" {
		if _, err := serveUnixSocket(socketPath, server); err != nil {
			log.Printf("failed to listen on %s: %v", socketPath, err)
		}
	}
	if err := server.Start(":8080"); err != nil {
		panic("failed to start server: " + err.Error())
	}
}

// serveUnixSocket replaces any stale socket file at path, makes it connectable by nginx
// and serves handler on it in the background.
func serveUnixSocket(path string, handler http.Handler) (net.Listener, error) {
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o666); err != nil {
		ln.Close()
		return nil, err
	}
	go func() {
		if err := http.Serve(ln, handler); err != nil {
			log.Printf("unix socket server stopped: %v", err)
		}
	}()
	return ln, nil
}
