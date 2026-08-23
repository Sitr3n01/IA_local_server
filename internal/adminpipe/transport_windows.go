//go:build windows

package adminpipe

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// pipeBufferBytes sizes the kernel buffers. Messages are bounded well below
	// this, so a client can never make the server allocate on demand.
	pipeBufferBytes = 16 << 10
	// pipeDefaultTimeoutMS is the default client wait honoured by WaitNamedPipe.
	pipeDefaultTimeoutMS = 5000
	// maxPipeInstances bounds concurrent administrative connections. The
	// operations behind them are mutually exclusive anyway; the bound exists so
	// a local process cannot exhaust handles by connecting in a loop.
	maxPipeInstances = 4
	// requestDeadline bounds how long a connected client may take to send its
	// one request. It is not the operation timeout: a model load legitimately
	// runs for minutes after the request has been read.
	requestDeadline = 10 * time.Second
	// serving user access mask: FILE_GENERIC_READ | FILE_GENERIC_WRITE.
	servingUserAccess = "0x12019f"
)

// Listener owns the named pipe instances that accept administrative commands.
type Listener struct {
	name       string
	descriptor *windows.SECURITY_DESCRIPTOR
	sddl       string

	mu      sync.Mutex
	pending windows.Handle
	closed  bool
}

// Listen creates the administrative pipe with a DACL that admits SYSTEM, the
// built-in Administrators group, and the account this process runs as. The
// first instance is created with FILE_FLAG_FIRST_PIPE_INSTANCE, so a process
// that already squatted the name makes this call fail loudly instead of
// silently sharing the endpoint.
func Listen(name string) (*Listener, error) {
	if err := validatePipeName(name); err != nil {
		return nil, err
	}
	descriptor, sddl, err := servingUserDescriptor()
	if err != nil {
		return nil, err
	}
	listener := &Listener{name: name, descriptor: descriptor, sddl: sddl}
	handle, err := listener.createInstance(true)
	if err != nil {
		return nil, fmt.Errorf("create administrative pipe: %w", err)
	}
	listener.pending = handle
	return listener, nil
}

// SecurityDescriptor reports the SDDL actually applied. It contains a SID, no
// credential, and exists so an installation check can assert the policy.
func (l *Listener) SecurityDescriptor() string { return l.sddl }

// Name reports the pipe path this listener owns.
func (l *Listener) Name() string { return l.name }

func (l *Listener) createInstance(first bool) (windows.Handle, error) {
	namePtr, err := windows.UTF16PtrFromString(l.name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	attributes := &windows.SecurityAttributes{
		SecurityDescriptor: l.descriptor,
		InheritHandle:      0,
	}
	attributes.Length = uint32(unsafe.Sizeof(*attributes))

	openMode := uint32(windows.PIPE_ACCESS_DUPLEX)
	if first {
		openMode |= windows.FILE_FLAG_FIRST_PIPE_INSTANCE
	}
	// Byte mode with PIPE_REJECT_REMOTE_CLIENTS: the framing is ours, and a
	// remote client must never reach this endpoint even if the machine is later
	// joined to a domain that permits remote pipe access.
	pipeMode := uint32(windows.PIPE_TYPE_BYTE | windows.PIPE_READMODE_BYTE | windows.PIPE_WAIT | windows.PIPE_REJECT_REMOTE_CLIENTS)
	return windows.CreateNamedPipe(
		namePtr,
		openMode,
		pipeMode,
		maxPipeInstances,
		pipeBufferBytes,
		pipeBufferBytes,
		pipeDefaultTimeoutMS,
		attributes,
	)
}

// Accept blocks until a client connects to the pending instance and then arms
// the next one, so the endpoint is continuously available.
func (l *Listener) Accept() (*Conn, error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, errors.New("administrative pipe listener is closed")
	}
	handle := l.pending
	l.mu.Unlock()

	err := windows.ConnectNamedPipe(handle, nil)
	if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		l.mu.Lock()
		closed := l.closed
		l.mu.Unlock()
		if closed {
			return nil, errors.New("administrative pipe listener is closed")
		}
		_ = windows.CloseHandle(handle)
		l.rearm()
		return nil, fmt.Errorf("accept administrative connection: %w", err)
	}

	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		_ = windows.DisconnectNamedPipe(handle)
		_ = windows.CloseHandle(handle)
		return nil, errors.New("administrative pipe listener is closed")
	}
	next, createErr := l.createInstance(false)
	if createErr != nil {
		l.pending = windows.InvalidHandle
		l.mu.Unlock()
		return &Conn{handle: handle, server: true}, nil
	}
	l.pending = next
	l.mu.Unlock()
	return &Conn{handle: handle, server: true}, nil
}

// rearm replaces a pending instance that failed to connect. A failure here is
// reported by the next Accept rather than swallowed.
func (l *Listener) rearm() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	handle, err := l.createInstance(false)
	if err != nil {
		l.pending = windows.InvalidHandle
		return
	}
	l.pending = handle
}

// Close cancels the blocked accept and releases the pending instance. Live
// connections are owned by their handler goroutines and close themselves.
func (l *Listener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	if l.pending == windows.InvalidHandle || l.pending == 0 {
		return nil
	}
	// CancelIoEx cancels the synchronous ConnectNamedPipe issued on the accept
	// goroutine; closing the handle alone would race with it.
	_ = windows.CancelIoEx(l.pending, nil)
	err := windows.CloseHandle(l.pending)
	l.pending = windows.InvalidHandle
	return err
}

// Serve runs the accept loop until ctx is done or the listener is closed. Each
// connection handles exactly one request under a read deadline.
func (l *Listener) Serve(ctx context.Context, handler Handler, onError func(error)) error {
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()

	semaphore := make(chan struct{}, maxPipeInstances)
	var wait sync.WaitGroup
	for {
		conn, err := l.Accept()
		if err != nil {
			wait.Wait()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		select {
		case semaphore <- struct{}{}:
		default:
			// Refuse rather than queue: the caller sees a closed connection and
			// retries, and the server keeps a bounded number of handles.
			_ = conn.Close()
			continue
		}
		wait.Add(1)
		go func(conn *Conn) {
			defer wait.Done()
			defer func() { <-semaphore }()
			defer conn.Close()
			stop := conn.cancelAfter(requestDeadline)
			serveErr := ServeConn(ctx, conn, handler)
			stop()
			if serveErr != nil && onError != nil {
				onError(serveErr)
			}
		}(conn)
	}
}

// Conn is one administrative pipe connection.
type Conn struct {
	handle windows.Handle
	server bool

	mu     sync.Mutex
	closed bool
}

func (c *Conn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	var read uint32
	err := windows.ReadFile(c.handle, p, &read, nil)
	if err != nil {
		if errors.Is(err, windows.ERROR_BROKEN_PIPE) || errors.Is(err, windows.ERROR_NO_DATA) {
			return int(read), errPipeClosed
		}
		return int(read), err
	}
	return int(read), nil
}

func (c *Conn) Write(p []byte) (int, error) {
	written := 0
	for written < len(p) {
		var count uint32
		if err := windows.WriteFile(c.handle, p[written:], &count, nil); err != nil {
			return written, err
		}
		if count == 0 {
			return written, errors.New("administrative pipe write made no progress")
		}
		written += int(count)
	}
	return written, nil
}

// Close flushes and releases the connection. A server connection is
// disconnected first so the client observes end-of-file instead of a reset.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.server {
		_ = windows.FlushFileBuffers(c.handle)
		_ = windows.DisconnectNamedPipe(c.handle)
	}
	return windows.CloseHandle(c.handle)
}

// cancelAfter aborts a blocked read once the deadline elapses, so a client that
// connects and never speaks cannot hold an instance open.
func (c *Conn) cancelAfter(deadline time.Duration) func() {
	timer := time.AfterFunc(deadline, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closed {
			return
		}
		_ = windows.CancelIoEx(c.handle, nil)
	})
	return func() { timer.Stop() }
}

// servingUserDescriptor builds the DACL applied to the pipe. The account this
// process runs as is the serving user by construction, which is exactly the
// principal the installation ACL policy already grants read/execute.
func servingUserDescriptor() (*windows.SECURITY_DESCRIPTOR, string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, "", fmt.Errorf("resolve serving user for the administrative pipe: %w", err)
	}
	sid := user.User.Sid.String()
	if sid == "" {
		return nil, "", errors.New("serving user SID is unavailable")
	}
	// Protected DACL (P): no inherited entry can widen this. SYSTEM and the
	// built-in Administrators group keep full access for recovery; the serving
	// user gets read/write on the pipe and nothing else.
	sddl := "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;" + servingUserAccess + ";;;" + sid + ")"
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, "", fmt.Errorf("build administrative pipe DACL: %w", err)
	}
	return descriptor, sddl, nil
}

func validatePipeName(name string) error {
	if !strings.HasPrefix(name, `\\.\pipe\`) {
		return errors.New(`administrative pipe name must begin with \\.\pipe\`)
	}
	leaf := strings.TrimPrefix(name, `\\.\pipe\`)
	if leaf == "" || len(leaf) > 128 {
		return errors.New("administrative pipe name is empty or too long")
	}
	for _, r := range leaf {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return errors.New("administrative pipe name may contain only letters, digits, dot, dash, and underscore")
		}
	}
	return nil
}

// Dial connects to the administrative pipe and verifies who is answering.
//
// Pipe names are first-come-first-served on Windows, so a client that trusts
// the name alone can be answered by a squatter. Nothing secret is sent either
// way, but a squatter could still fabricate a success, so the server's process
// image is checked against the executable the deployment installed before the
// request is written.
func Dial(ctx context.Context, options DialOptions) (*Conn, error) {
	normalized, err := options.normalized()
	if err != nil {
		return nil, err
	}
	if err := validatePipeName(normalized.Name); err != nil {
		return nil, err
	}
	expected, err := canonicalExecutablePath(normalized.ExpectedServerPath)
	if err != nil {
		return nil, err
	}
	namePtr, err := windows.UTF16PtrFromString(normalized.Name)
	if err != nil {
		return nil, err
	}

	deadline := time.Now().Add(normalized.Timeout)
	for {
		// SECURITY_SQOS_PRESENT|SECURITY_IDENTIFICATION caps the server at
		// identification level, so a compromised endpoint cannot impersonate the
		// operator against anything else on the machine.
		handle, openErr := windows.CreateFile(
			namePtr,
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			0,
			nil,
			windows.OPEN_EXISTING,
			windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION,
			0,
		)
		if openErr == nil {
			if err := assertPipeServer(handle, expected); err != nil {
				_ = windows.CloseHandle(handle)
				return nil, err
			}
			return &Conn{handle: handle}, nil
		}
		if errors.Is(openErr, windows.ERROR_FILE_NOT_FOUND) || errors.Is(openErr, windows.ERROR_PATH_NOT_FOUND) {
			return nil, fmt.Errorf("%w: %s", ErrNotListening, normalized.Name)
		}
		if !errors.Is(openErr, windows.ERROR_PIPE_BUSY) {
			return nil, fmt.Errorf("connect to the administrative pipe: %w", openErr)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if time.Now().After(deadline) {
			return nil, errors.New("administrative pipe is busy")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// assertPipeServer refuses a pipe served by anything other than the installed
// edge executable.
func assertPipeServer(handle windows.Handle, expected string) error {
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(handle, &pid); err != nil {
		return fmt.Errorf("identify the administrative pipe server: %w", err)
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return fmt.Errorf("open the administrative pipe server process: %w", err)
	}
	defer windows.CloseHandle(process)

	buffer := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return fmt.Errorf("read the administrative pipe server image: %w", err)
	}
	actual, err := canonicalExecutablePath(windows.UTF16ToString(buffer[:size]))
	if err != nil {
		return err
	}
	if !strings.EqualFold(actual, expected) {
		return errors.New("the administrative pipe is served by an unexpected executable")
	}
	return nil
}

func canonicalExecutablePath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", errors.New("administrative pipe server executable is required")
	}
	full, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("resolve the administrative pipe server executable: %w", err)
	}
	return full, nil
}
