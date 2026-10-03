package monitor

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A llama.cpp server writes one block of timing lines for every request it
// finishes: how many prompt tokens it had to process, how many it generated, and
// how fast it did each. A tool that wraps such a server and writes its output to
// a log - LM Studio and Bionic do - therefore leaves, on disk and without any
// key, the numbers that the tool's API would not give the monitor. The lines
// carry counts and times and nothing else: no prompt, no answer.
//
// The reader below takes only those numbers out of the lines it recognises. It
// does not keep a line, and a line it does not recognise is forgotten at once.
// Nothing in it knows what model is loaded.

var (
	logStamp   = regexp.MustCompile(`^\[(\d{4}-\d\d-\d\d \d\d:\d\d:\d\d)\]`)
	logLaunch  = regexp.MustCompile(`slot launch_slot_: id\s+(\d+) \| task (\d+) \| processing task`)
	logPrompt  = regexp.MustCompile(`task (\d+) \| prompt eval time =\s*([0-9.]+) ms /\s*(\d+) tokens \(.*?,\s*([0-9.]+) tokens per second\)`)
	logEval    = regexp.MustCompile(`task (\d+) \|\s+eval time =\s*([0-9.]+) ms /\s*(\d+) tokens \(.*?,\s*([0-9.]+) tokens per second\)`)
	logTotal   = regexp.MustCompile(`task (\d+) \|\s+total time =\s*([0-9.]+) ms /\s*(\d+) tokens`)
	logRelease = regexp.MustCompile(`slot\s+release: id\s+(\d+) \| task (\d+) \| stop processing: (?:n_tokens|n_past) = (\d+), truncated = (\d+)`)
)

const (
	// logFirstWindow is how much of the end of a log is read when the monitor
	// first attaches to it, so requests made a little before it started still
	// show. logPollLimit bounds what one look reads.
	logFirstWindow   = 1 << 20
	logPollLimit     = 2 << 20
	logPartialLimit  = 64 << 10
	logPendingLimit  = 64
	logHistoryMaxAge = 24 * time.Hour
)

// loggedRequest is one finished request as the server counted it.
type loggedRequest struct {
	At              time.Time
	Slot            int
	Task            int64
	PromptEvaluated int
	Output          int
	PromptMS        float64
	DecodeMS        float64
	TotalMS         float64
	PromptTPS       float64
	DecodeTPS       float64
	// ContextAtEnd is the number of tokens the slot held when the request
	// finished. Together with the output it gives the whole prompt, and so how
	// much of it was already cached.
	ContextAtEnd *int
	Truncated    bool
}

// promptTokens is the whole prompt: what was processed plus what the slot
// already held. The server counts the last generated token only after it is
// written to the cache, hence the +1.
func (r loggedRequest) promptTokens() int {
	if r.ContextAtEnd != nil && *r.ContextAtEnd+1 >= r.Output {
		if whole := *r.ContextAtEnd + 1 - r.Output; whole >= r.PromptEvaluated {
			return whole
		}
	}
	return r.PromptEvaluated
}

// cachedTokens is how much of the prompt was reused, or -1 when the log does not
// say how large the whole prompt was.
func (r loggedRequest) cachedTokens() int {
	if r.ContextAtEnd == nil {
		return -1
	}
	return r.promptTokens() - r.PromptEvaluated
}

type pendingRequest struct {
	launchedAt time.Time
	slot       int
	task       int64

	promptMS, decodeMS, totalMS    float64
	promptN, decodeN               int
	promptTPS, decodeTPS           float64
	hasPrompt, hasDecode, hasTotal bool
}

// llamaLogParser turns lines into finished requests. It is fed in order.
type llamaLogParser struct {
	loc        *time.Location
	stamp      time.Time
	pending    map[int64]*pendingRequest
	order      []int64
	seenTiming bool
	// readAt is when the lines being fed were read. A server's own log has no
	// wall-clock time on its lines (the tool that wraps it adds one, as LM Studio
	// and Bionic do), so a line without one is dated by when it was read, which is
	// close to when it was written for a log that is followed as it grows.
	readAt time.Time
}

// wallClock is the time to give what is being read.
func (p *llamaLogParser) wallClock() time.Time {
	if !p.stamp.IsZero() {
		return p.stamp
	}
	return p.readAt
}

func newLlamaLogParser(loc *time.Location) *llamaLogParser {
	if loc == nil {
		loc = time.Local
	}
	return &llamaLogParser{loc: loc, pending: make(map[int64]*pendingRequest)}
}

// feed reads one line and returns a request if the line finished one. Only the
// numbers of recognised lines are taken; the line itself is not kept.
func (p *llamaLogParser) feed(line string) *loggedRequest {
	if match := logStamp.FindStringSubmatch(line); match != nil {
		if at, err := time.ParseInLocation("2006-01-02 15:04:05", match[1], p.loc); err == nil {
			p.stamp = at
		}
	}
	if match := logLaunch.FindStringSubmatch(line); match != nil {
		slot, _ := strconv.Atoi(match[1])
		task, _ := strconv.ParseInt(match[2], 10, 64)
		// Undated when the log has no wall-clock time: the request's start is then
		// worked out from when it finished.
		p.track(&pendingRequest{launchedAt: p.stamp, slot: slot, task: task})
		return nil
	}
	if match := logPrompt.FindStringSubmatch(line); match != nil {
		p.seenTiming = true
		if request := p.lookup(match[1]); request != nil {
			request.promptMS, request.promptN, request.promptTPS = number(match[2]), integer(match[3]), number(match[4])
			request.hasPrompt = true
		}
		return nil
	}
	if match := logEval.FindStringSubmatch(line); match != nil {
		p.seenTiming = true
		if request := p.lookup(match[1]); request != nil {
			request.decodeMS, request.decodeN, request.decodeTPS = number(match[2]), integer(match[3]), number(match[4])
			request.hasDecode = true
		}
		return nil
	}
	if match := logTotal.FindStringSubmatch(line); match != nil {
		if request := p.lookup(match[1]); request != nil {
			request.totalMS, request.hasTotal = number(match[2]), true
		}
		return nil
	}
	if match := logRelease.FindStringSubmatch(line); match != nil {
		task, _ := strconv.ParseInt(match[2], 10, 64)
		request := p.pending[task]
		if request == nil {
			return nil
		}
		p.drop(task)
		if !request.hasPrompt && !request.hasDecode {
			return nil
		}
		held := integer(match[3])
		finished := &loggedRequest{
			At: request.launchedAt, Slot: request.slot, Task: request.task,
			PromptEvaluated: request.promptN, Output: request.decodeN,
			PromptMS: request.promptMS, DecodeMS: request.decodeMS, TotalMS: request.totalMS,
			PromptTPS: request.promptTPS, DecodeTPS: request.decodeTPS,
			ContextAtEnd: &held, Truncated: match[4] != "0",
		}
		if !request.hasTotal {
			finished.TotalMS = request.promptMS + request.decodeMS
		}
		if finished.At.IsZero() {
			finished.At = p.wallClock().Add(-time.Duration(finished.TotalMS * float64(time.Millisecond)))
		}
		return finished
	}
	return nil
}

func (p *llamaLogParser) track(request *pendingRequest) {
	p.pending[request.task] = request
	p.order = append(p.order, request.task)
	for len(p.order) > logPendingLimit {
		delete(p.pending, p.order[0])
		p.order = p.order[1:]
	}
}

func (p *llamaLogParser) lookup(task string) *pendingRequest {
	number, err := strconv.ParseInt(task, 10, 64)
	if err != nil {
		return nil
	}
	return p.pending[number]
}

func (p *llamaLogParser) drop(task int64) {
	delete(p.pending, task)
	for index, id := range p.order {
		if id == task {
			p.order = append(p.order[:index], p.order[index+1:]...)
			break
		}
	}
}

func number(text string) float64 {
	value, _ := strconv.ParseFloat(text, 64)
	return value
}

func integer(text string) int {
	value, _ := strconv.Atoi(text)
	return value
}

// logTailer follows one log file. It remembers how far it has read and holds at
// most an unfinished line.
type logTailer struct {
	path    string
	offset  int64
	partial []byte
	primed  bool
	parser  *llamaLogParser
	now     func() time.Time
}

func newLogTailer(path string, loc *time.Location) *logTailer {
	return &logTailer{path: path, parser: newLlamaLogParser(loc), now: time.Now}
}

// poll reads what was appended since the last look and returns the requests it
// finished. The first look reads the end of the file, not all of it.
func (t *logTailer) poll() ([]loggedRequest, error) {
	file, err := os.Open(t.path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("the log is not a regular file")
	}
	size := info.Size()
	dropFirst := false
	first := !t.primed
	t.parser.readAt = t.now()
	switch {
	case !t.primed:
		t.primed = true
		if size > logFirstWindow {
			t.offset = size - logFirstWindow
			dropFirst = true
		}
	case size < t.offset:
		// Truncated or replaced: start again and forget what was half read.
		t.offset, t.partial = 0, nil
		t.parser = newLlamaLogParser(t.parser.loc)
	}
	if size == t.offset {
		return nil, nil
	}
	window := size - t.offset
	if window > logPollLimit {
		t.offset = size - logPollLimit
		window = logPollLimit
		dropFirst = true
	}
	buffer := make([]byte, window)
	read, err := file.ReadAt(buffer, t.offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	t.offset += int64(read)
	data := append(t.partial, buffer[:read]...)
	t.partial = nil
	if dropFirst {
		// The window started in the middle of a line.
		if newline := bytes.IndexByte(data, '\n'); newline >= 0 {
			data = data[newline+1:]
		} else {
			return nil, nil
		}
	}
	var finished []loggedRequest
	for len(data) > 0 {
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			if len(data) <= logPartialLimit {
				t.partial = append([]byte(nil), data...)
			}
			break
		}
		line := strings.TrimRight(string(data[:newline]), "\r")
		data = data[newline+1:]
		if request := t.parser.feed(line); request != nil {
			finished = append(finished, *request)
		}
	}
	// A log whose lines carry no time cannot date what was already in it when
	// the monitor arrived, and dating all of it "now" would be a lie.
	if first && t.parser.stamp.IsZero() {
		finished = nil
	}
	return finished, nil
}

var serverLogName = regexp.MustCompile(`^(\d{4}-\d\d-\d\d)\.(\d+)\.log$`)

// latestServerLog finds the newest log in a directory laid out as LM Studio and
// Bionic lay it out: a folder per month holding one file per day and
// rotation index. It returns "" when there is none.
func latestServerLog(dir string) string {
	months, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var names []string
	for _, month := range months {
		if month.IsDir() {
			names = append(names, month.Name())
		}
	}
	sort.Strings(names)
	for index := len(names) - 1; index >= 0; index-- {
		files, err := os.ReadDir(filepath.Join(dir, names[index]))
		if err != nil {
			continue
		}
		type candidate struct {
			day   string
			index int
			name  string
		}
		var found []candidate
		for _, file := range files {
			match := serverLogName.FindStringSubmatch(file.Name())
			if match == nil || file.IsDir() {
				continue
			}
			rotation, _ := strconv.Atoi(match[2])
			found = append(found, candidate{day: match[1], index: rotation, name: file.Name()})
		}
		if len(found) == 0 {
			continue
		}
		sort.Slice(found, func(i, j int) bool {
			if found[i].day != found[j].day {
				return found[i].day < found[j].day
			}
			return found[i].index < found[j].index
		})
		return filepath.Join(dir, names[index], found[len(found)-1].name)
	}
	return ""
}

// regularFile reports whether path names an existing regular file.
func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && !errors.Is(err, fs.ErrNotExist)
}
