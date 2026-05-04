package sshconn

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"time"

	"remofy-bot/internal/crypto"
	"remofy-bot/internal/models"

	"golang.org/x/crypto/ssh"
)

type Conn struct {
	Client  *ssh.Client
	Session *ssh.Session
	Stdin   io.WriteCloser
	Stdout  io.Reader
	cancel  context.CancelFunc
}

// Open serverga ulanadi, PTY ochadi, shellni ishga tushiradi.
// ctx bekor qilinsa keepalive goroutine to'xtaydi (lekin Close ham chaqirilishi kerak).
func Open(parent context.Context, server models.Server) (*Conn, error) {
	secret, err := crypto.Decrypt(server.EncryptedSecret)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	auth := ssh.Password(secret)
	if server.AuthType == "key" {
		signer, err := ssh.ParsePrivateKey([]byte(secret))
		if err != nil {
			return nil, fmt.Errorf("parse key: %w", err)
		}
		auth = ssh.PublicKeys(signer)
	}

	cfg := &ssh.ClientConfig{
		User:            server.Username,
		Auth:            []ssh.AuthMethod{auth},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 15 * time.Second}
	addr := fmt.Sprintf("%s:%d", server.Host, server.Port)

	ctx, cancel := context.WithCancel(parent)

	tcp, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("dial: %w", err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(tcp, addr, cfg)
	if err != nil {
		tcp.Close()
		cancel()
		return nil, fmt.Errorf("ssh handshake: %w", err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)

	// Keepalive — web-ssh patterni
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
					log.Printf("ssh keepalive failed: %v", err)
					return
				}
			}
		}
	}()

	sess, err := client.NewSession()
	if err != nil {
		client.Close()
		cancel()
		return nil, fmt.Errorf("new session: %w", err)
	}

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := sess.RequestPty("xterm", 24, 80, modes); err != nil {
		sess.Close()
		client.Close()
		cancel()
		return nil, fmt.Errorf("pty: %w", err)
	}

	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		client.Close()
		cancel()
		return nil, fmt.Errorf("stdin: %w", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		sess.Close()
		client.Close()
		cancel()
		return nil, fmt.Errorf("stdout: %w", err)
	}
	// stderr ham stdout bilan birlashtiramiz
	stderr, err := sess.StderrPipe()
	if err != nil {
		sess.Close()
		client.Close()
		cancel()
		return nil, fmt.Errorf("stderr: %w", err)
	}

	if err := sess.Shell(); err != nil {
		sess.Close()
		client.Close()
		cancel()
		return nil, fmt.Errorf("shell: %w", err)
	}

	combined := io.MultiReader(stdout, stderr)

	return &Conn{
		Client:  client,
		Session: sess,
		Stdin:   stdin,
		Stdout:  combined,
		cancel:  cancel,
	}, nil
}

func (c *Conn) Close() {
	if c == nil {
		return
	}
	if c.cancel != nil {
		c.cancel()
	}
	if c.Stdin != nil {
		_ = c.Stdin.Close()
	}
	if c.Session != nil {
		_ = c.Session.Close()
	}
	if c.Client != nil {
		_ = c.Client.Close()
	}
}
