//go:build windows

package adminpipe

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// x/sys/windows does not wrap this entry point. Load only the system DLL.
var cancelSynchronousIO = windows.NewLazySystemDLL("kernel32.dll").NewProc("CancelSynchronousIo")

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
	// Delivery and flush are bounded separately from a potentially long model
	// operation. A peer that stops reading must not retain the pipe forever.
	responseDeadline = 2 * time.Second
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
		return newConn(handle, true), nil
	}
	l.pending = next
	l.mu.Unlock()
	return newConn(handle, true), nil
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
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			_ = l.Close()
		case <-stopped:
		}
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
		conn.ctx = ctx
		readCtx, cancelRead := context.WithTimeout(ctx, requestDeadline)
		conn.readCtx = readCtx
		select {
		case semaphore <- struct{}{}:
		default:
			// Refuse rather than queue: the caller sees a closed connection and
			// retries, and the server keeps a bounded number of handles.
			_ = conn.Close()
			cancelRead()
			continue
		}
		wait.Add(1)
		go func(conn *Conn) {
			defer wait.Done()
			defer func() { <-semaphore }()
			defer cancelRead()
			defer conn.Close()
			serveErr := ServeConn(ctx, conn, handler)
			if serveErr != nil && onError != nil {
				onError(serveErr)
			}
		}(conn)
	}
}

// Conn is one administrative pipe connection.
type Conn struct {
	handle    windows.Handle
	server    bool
	ctx       context.Context
	readCtx   context.Context
	closedCtx context.Context
	cancel    context.CancelFunc

	mu        sync.Mutex
	closed    bool
	ioWait    sync.WaitGroup
	closeDone chan struct{}
}

func newConn(handle windows.Handle, server bool) *Conn {
	closedCtx, cancel := context.WithCancel(context.Background())
	return &Conn{handle: handle, server: server, ctx: context.Background(),
		closedCtx: closedCtx, cancel: cancel, closeDone: make(chan struct{})}
}

func (c *Conn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	var read uint32
	ctx := c.ctx
	if c.readCtx != nil {
		ctx = c.readCtx
	}
	err := c.runIO(ctx, func() error { return windows.ReadFile(c.handle, p, &read, nil) })
	if err != nil {
		if errors.Is(err, windows.ERROR_BROKEN_PIPE) || errors.Is(err, windows.ERROR_NO_DATA) {
			return int(read), errPipeClosed
		}
		return int(read), err
	}
	return int(read), nil
}

func (c *Conn) Write(p []byte) (int, error) {
	ctx := c.ctx
	if c.server {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, responseDeadline)
		defer cancel()
	}
	written := 0
	for written < len(p) {
		var count uint32
		if err := c.runIO(ctx, func() error { return windows.WriteFile(c.handle, p[written:], &count, nil) }); err != nil {
			return written, err
		}
		if count == 0 {
			return written, errors.New("administrative pipe write made no progress")
		}
		written += int(count)
	}
	return written, nil
}

// Close cancels live I/O, waits for it to return, then releases the handle.
// Server delivery is flushed under a deadline and the serving context so a
// peer that never reads cannot prevent shutdown.
func (c *Conn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		<-c.closeDone
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	defer close(c.closeDone)
	c.cancel()
	c.ioWait.Wait()
	if c.server {
		ctx, cancel := context.WithTimeout(c.ctx, responseDeadline)
		_ = synchronousIO(ctx, func() error { return windows.FlushFileBuffers(c.handle) })
		cancel()
		_ = windows.DisconnectNamedPipe(c.handle)
	}
	return windows.CloseHandle(c.handle)
}

func (c *Conn) runIO(ctx context.Context, operation func() error) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errPipeClosed
	}
	c.ioWait.Add(1)
	c.mu.Unlock()
	defer c.ioWait.Done()
	ioCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.closedCtx, cancel)
	defer func() { stop(); cancel() }()
	return synchronousIO(ioCtx, operation)
}

// Synchronous pipe operations, including FlushFileBuffers, need cancellation
// of their issuing OS thread. Pin that thread and join the cancellation worker
// before releasing it, so cancellation cannot hit unrelated Go work. Retry
// until completion to cover cancellation just before the syscall starts.
func synchronousIO(ctx context.Context, operation func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ctx.Done() == nil {
		return operation()
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	thread, err := windows.OpenThread(windows.THREAD_TERMINATE, false, windows.GetCurrentThreadId())
	if err != nil {
		return fmt.Errorf("prepare cancellable pipe I/O: %w", err)
	}
	defer windows.CloseHandle(thread)
	done, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-done:
			return
		case <-ctx.Done():
		}
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			_, _, _ = cancelSynchronousIO.Call(uintptr(thread))
			select {
			case <-done:
				return
			case <-ticker.C:
			}
		}
	}()
	err = operation()
	close(done)
	<-joined
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
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

// pipePrefix is the only local named-pipe namespace this transport accepts.
// It is a single constant on purpose: the prefix was duplicated across the
// listener, the default-name helper, and the client, and one copy silently lost
// a backslash - which the listener then refused, stopping the edge from
// starting at all.
const pipePrefix = `\\.\pipe\`

func validatePipeName(name string) error {
	if !strings.HasPrefix(name, pipePrefix) {
		return errors.New("administrative pipe name must begin with " + pipePrefix)
	}
	leaf := strings.TrimPrefix(name, pipePrefix)
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
		if err := ctx.Err(); err != nil {
			return nil, err
		}
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
			conn := newConn(handle, false)
			conn.ctx = ctx
			return conn, nil
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
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
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
