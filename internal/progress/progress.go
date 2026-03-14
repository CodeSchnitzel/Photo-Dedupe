package progress

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mattn/go-isatty"
)

// Progress displays a live progress bar on the terminal.
// It uses a ticker goroutine to render at fixed intervals, avoiding
// per-file render overhead in the hot path.
type Progress struct {
	total     int64
	processed int64 // atomic
	started   time.Time
	isTTY     bool
	done      chan struct{}
	ticker    *time.Ticker
	mu        sync.Mutex
	lastLen   int    // length of last rendered line, for clearing
	lastPath  string // most recently processed file path
	lastLog   int64  // last logged count (for non-TTY fallback)

	// logFunc is used for non-TTY fallback logging. Set via SetLogFunc.
	logFunc func(format string, args ...interface{})
}

// New creates a progress tracker for the given total file count.
func New(total int64) *Progress {
	return &Progress{
		total:   total,
		started: time.Now(),
		isTTY:   isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd()),
		done:    make(chan struct{}),
	}
}

// SetLogFunc sets the function used for non-TTY fallback progress logging.
func (p *Progress) SetLogFunc(f func(format string, args ...interface{})) {
	p.logFunc = f
}

// Start begins the ticker goroutine that renders the progress line.
func (p *Progress) Start() {
	p.ticker = time.NewTicker(200 * time.Millisecond)
	go func() {
		for {
			select {
			case <-p.ticker.C:
				p.render()
			case <-p.done:
				return
			}
		}
	}()
}

// Increment atomically bumps the processed counter and stores the current path.
func (p *Progress) Increment(path string) {
	p.mu.Lock()
	p.lastPath = path
	p.mu.Unlock()
	atomic.AddInt64(&p.processed, 1)
}

// ClearLine erases the current progress line so that a log message can be
// printed cleanly above it. The next ticker tick will re-render the bar.
func (p *Progress) ClearLine() {
	if !p.isTTY {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lastLen > 0 {
		blank := "\r" + strings.Repeat(" ", p.lastLen) + "\r"
		fmt.Fprint(os.Stdout, blank)
		p.lastLen = 0
	}
}

// IsTTY returns whether the progress bar is rendering to a terminal.
func (p *Progress) IsTTY() bool {
	return p.isTTY
}

// Finish stops the ticker, renders a final line, and prints a newline.
func (p *Progress) Finish() {
	if p.ticker != nil {
		p.ticker.Stop()
	}
	close(p.done)

	if p.isTTY {
		p.render()
		fmt.Fprintln(os.Stdout) // move to next line
	} else {
		// Final non-TTY log
		processed := atomic.LoadInt64(&p.processed)
		elapsed := time.Since(p.started)
		rate := float64(0)
		if elapsed.Seconds() > 0 {
			rate = float64(processed) / elapsed.Seconds()
		}
		if p.logFunc != nil {
			p.logFunc("Progress: %d/%d files (%.1f files/sec, %s elapsed)",
				processed, p.total, rate, formatDuration(elapsed))
		}
	}
}

func (p *Progress) render() {
	processed := atomic.LoadInt64(&p.processed)
	total := p.total

	if !p.isTTY {
		// Non-TTY: log every 1000 files
		if processed > 0 && processed-p.lastLog >= 1000 {
			elapsed := time.Since(p.started)
			rate := float64(processed) / elapsed.Seconds()
			if p.logFunc != nil {
				p.logFunc("Progress: %d/%d files (%.1f files/sec)", processed, total, rate)
			}
			p.lastLog = processed
		}
		return
	}

	elapsed := time.Since(p.started)
	pct := float64(0)
	if total > 0 {
		pct = float64(processed) / float64(total) * 100
	}

	rate := float64(0)
	eta := ""
	if elapsed.Seconds() > 0.5 {
		rate = float64(processed) / elapsed.Seconds()
		if rate > 0 && processed < total {
			remaining := time.Duration(float64(total-processed)/rate) * time.Second
			eta = formatDuration(remaining)
		} else if processed >= total {
			eta = "done"
		}
	}

	p.mu.Lock()
	path := p.lastPath
	p.mu.Unlock()

	shortPath := truncatePath(path, 40)

	line := fmt.Sprintf("\r[%d/%d] %.1f%% | %.1f files/sec | ETA %s | %s",
		processed, total, pct, rate, eta, shortPath)

	// Pad with spaces to clear previous longer line.
	if len(line) < p.lastLen {
		line += strings.Repeat(" ", p.lastLen-len(line))
	}
	p.lastLen = len(line)

	fmt.Fprint(os.Stdout, line)
}

// truncatePath shortens a file path for display.
// Shows .../<parent>/<filename> if the full path is too long.
func truncatePath(path string, maxLen int) string {
	if len(path) <= maxLen {
		return path
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	parent := filepath.Base(dir)

	short := filepath.Join("...", parent, base)
	if len(short) > maxLen {
		// Just show the filename.
		if len(base) > maxLen {
			return "..." + base[len(base)-(maxLen-3):]
		}
		return base
	}
	return short
}

// formatDuration formats a duration as "Xh Ym", "Xm Ys", or "Xs".
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60

	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
