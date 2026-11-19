//go:build !windows

package docker

import (
	"context"
	"errors"
	"net"
)

const defaultHost = "unix:///var/run/docker.sock"

func dialPipe(ctx context.Context, path string) (net.Conn, error) {
	return nil, errors.New("docker: named pipes are only supported on Windows")
}
