package storage

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"sync"
	"time"

	"replica/internal/config"
	"replica/internal/storageuri"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const sftpIOTimeout = 30 * time.Second

// Each operation owns its connection, including cancellation and idle deadlines.
type sftpConnection struct {
	*sftp.Client
	transport net.Conn
	ssh       *ssh.Client
	stop      func() bool
	once      sync.Once
}

func (c *sftpConnection) Close() error {
	c.once.Do(func() { c.stop(); _ = c.transport.Close(); _ = c.Client.Close(); _ = c.ssh.Close() })
	return nil
}

type sftpDeadlineConn struct{ net.Conn }

func (c sftpDeadlineConn) Read(p []byte) (int, error) {
	_ = c.SetReadDeadline(time.Now().Add(sftpIOTimeout))
	return c.Conn.Read(p)
}
func (c sftpDeadlineConn) Write(p []byte) (int, error) {
	_ = c.SetWriteDeadline(time.Now().Add(sftpIOTimeout))
	return c.Conn.Write(p)
}

type sftpConnector struct{ profile config.StorageProfileConfig }

func newSFTPConnector(uri string, profile *config.StorageProfileConfig) (*sftpConnector, error) {
	u, err := storageuri.ParseSFTP(uri)
	if err != nil {
		return nil, err
	}
	if profile == nil || profile.Type != "sftp" {
		return nil, fmt.Errorf("SFTP requires a node-local sftp storage profile")
	}
	if profile.PrivateKeyFile == "" || profile.KnownHostsFile == "" {
		return nil, fmt.Errorf("SFTP profile requires private_key_file and known_hosts_file")
	}
	if _, err := sftpUsername(u, *profile); err != nil {
		return nil, err
	}
	return &sftpConnector{profile: *profile}, nil
}

func sftpUsername(u *url.URL, profile config.StorageProfileConfig) (string, error) {
	user := profile.Username
	if u.User != nil {
		if user != "" && user != u.User.Username() {
			return "", fmt.Errorf("SFTP URI username does not match profile")
		}
		user = u.User.Username()
	}
	if user == "" {
		return "", fmt.Errorf("SFTP profile requires username")
	}
	return user, nil
}

func (p *sftpConnector) connect(ctx context.Context, uri string) (*sftpConnection, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	u, err := storageuri.ParseSFTP(uri)
	if err != nil {
		return nil, "", err
	}
	user, err := sftpUsername(u, p.profile)
	if err != nil {
		return nil, "", err
	}
	key, err := os.ReadFile(p.profile.PrivateKeyFile)
	if err != nil {
		return nil, "", fmt.Errorf("read SFTP private key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, "", fmt.Errorf("parse SFTP private key (use an unencrypted node-local key): %w", err)
	}
	hostKey, err := knownhosts.New(p.profile.KnownHostsFile)
	if err != nil {
		return nil, "", fmt.Errorf("read SFTP known hosts: %w", err)
	}
	raw, err := (&net.Dialer{Timeout: sftpIOTimeout}).DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, "", err
	}
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	fail := func(err error) (*sftpConnection, string, error) {
		stop()
		_ = raw.Close()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, "", err
	}
	conn, chans, reqs, err := ssh.NewClientConn(sftpDeadlineConn{raw}, u.Host, &ssh.ClientConfig{User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: hostKey})
	if err != nil {
		return fail(err)
	}
	sshClient := ssh.NewClient(conn, chans, reqs)
	client, err := sftp.NewClient(sshClient)
	if err != nil {
		_ = sshClient.Close()
		return fail(err)
	}
	return &sftpConnection{Client: client, transport: raw, ssh: sshClient, stop: stop}, u.Path, nil
}
