package core

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ScheduleExecution is the public, secret-free schedule payload delivered to callbacks.
type ScheduleExecution struct {
	ID       string
	Name     string
	Platform string
	Target   string
	Prompt   string
	Cron     string
	Timezone string
	Enabled  bool
}

// ScheduleCallback is invoked asynchronously when an enabled schedule is due.
type ScheduleCallback func(ScheduleExecution, time.Time)

func (s *Service) SetScheduleCallback(fn ScheduleCallback) {
	s.scheduleMu.Lock()
	s.scheduleCallback = fn
	s.scheduleMu.Unlock()
}

func (s *Service) startScheduler() {
	s.schedulerWG.Add(1)
	go func() {
		defer s.schedulerWG.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-s.life.Done():
				return
			case now := <-ticker.C:
				s.runSchedulerOnce(now)
			}
		}
	}()
}

func (s *Service) runSchedulerOnce(now time.Time) {
	rows, err := s.db.List(s.life, "schedule")
	if err != nil {
		return
	}
	for _, row := range rows {
		var item scheduleRecord
		if jsonUnmarshal(row.Data, &item) != nil || !item.Enabled {
			continue
		}
		loc, err := time.LoadLocation(item.Timezone)
		if err != nil || !cronMatches(item.Cron, now.In(loc)) {
			continue
		}
		key := item.ID + ":" + now.In(loc).Format("200601021504")
		s.scheduleMu.Lock()
		already := s.scheduleRuns[key]
		if !already {
			s.scheduleRuns[key] = true
			// Keep this map bounded while retaining enough history to avoid duplicate ticks.
			if len(s.scheduleRuns) > 4096 {
				for k := range s.scheduleRuns {
					delete(s.scheduleRuns, k)
					if len(s.scheduleRuns) <= 2048 {
						break
					}
				}
			}
		}
		cb := s.scheduleCallback
		s.scheduleMu.Unlock()
		if already {
			continue
		}
		itemCopy := ScheduleExecution{ID: item.ID, Name: item.Name, Platform: item.Platform, Target: item.Target, Prompt: item.Prompt, Cron: item.Cron, Timezone: item.Timezone, Enabled: item.Enabled}
		executionTime := now.In(loc)
		s.executionWG.Add(1)
		go func() {
			defer s.executionWG.Done()
			select {
			case <-s.life.Done():
				return
			default:
			}
			_ = s.db.Log(context.Background(), "info", "schedule pending execution: "+itemCopy.ID)
			if cb != nil {
				cb(itemCopy, executionTime)
			}
		}()
	}
}

func validCron(expr string) bool {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return false
	}
	max := [...]int{59, 23, 31, 12, 7}
	min := [...]int{0, 0, 1, 1, 0}
	for i, f := range fields {
		if _, err := cronFieldMatches(f, -1, min[i], max[i]); err != nil {
			return false
		}
	}
	return true
}

func cronMatches(expr string, t time.Time) bool {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return false
	}
	dow := int(t.Weekday())
	vals := [...]int{t.Minute(), t.Hour(), t.Day(), int(t.Month()), dow}
	min := [...]int{0, 0, 1, 1, 0}
	max := [...]int{59, 23, 31, 12, 7}
	for i := 0; i < 2; i++ {
		ok, err := cronFieldMatches(fields[i], vals[i], min[i], max[i])
		if err != nil || !ok {
			return false
		}
	}
	monthOK, err := cronFieldMatches(fields[3], vals[3], min[3], max[3])
	if err != nil || !monthOK {
		return false
	}
	// Vixie cron treats day-of-month and day-of-week as OR when both
	// fields are restricted. If either field is wildcard, the other is
	// the effective constraint.
	dom, domErr := cronFieldMatches(fields[2], vals[2], min[2], max[2])
	dowMatch, dowErr := cronFieldMatches(fields[4], vals[4], min[4], max[4])
	if !dowMatch && vals[4] == 0 {
		dowMatch, dowErr = cronFieldMatches(fields[4], 7, min[4], max[4])
	}
	if domErr != nil || dowErr != nil {
		return false
	}
	domWildcard := fields[2] == "*"
	dowWildcard := fields[4] == "*"
	switch {
	case domWildcard && dowWildcard:
		return true
	case domWildcard:
		return dowMatch
	case dowWildcard:
		return dom
	default:
		return dom || dowMatch
	}
}

func cronFieldMatches(field string, value, min, max int) (bool, error) {
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return false, errors.New("empty cron item")
		}
		step := 1
		if strings.Contains(part, "/") {
			var raw string
			parts := strings.Split(part, "/")
			if len(parts) != 2 || parts[1] == "" {
				return false, errors.New("invalid cron step")
			}
			raw = parts[1]
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 {
				return false, errors.New("invalid cron step")
			}
			step, part = n, parts[0]
		}
		start, end := min, max
		if max == 7 && part == "7" {
			// Cron convention permits both 0 and 7 for Sunday.
			if value < 0 || value == 0 || value == 7 {
				return true, nil
			}
			continue
		}
		switch {
		case part == "*" || part == "":
		case strings.Contains(part, "-"):
			bounds := strings.Split(part, "-")
			if len(bounds) != 2 {
				return false, errors.New("invalid cron range")
			}
			var err error
			start, err = strconv.Atoi(bounds[0])
			if err != nil {
				return false, err
			}
			end, err = strconv.Atoi(bounds[1])
			if err != nil {
				return false, err
			}
		default:
			var err error
			start, err = strconv.Atoi(part)
			if err != nil {
				return false, err
			}
			end = start
		}
		if start < min || end > max || start > end {
			return false, errors.New("cron value out of range")
		}
		for n := start; n <= end; n += step {
			if n == value {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *Service) testNode(ctx context.Context, id string) (nodeRecord, error) {
	var item nodeRecord
	if err := s.db.Get(ctx, "node", id, &item); err != nil {
		return item, errors.New("node not found")
	}
	var secret map[string]string
	if err := s.db.Secret(ctx, "node", id, &secret); err != nil || secret["url"] == "" {
		return s.recordNodeTest(ctx, id, item, "node credentials are unavailable", "error", 0)
	}
	raw := secret["url"]
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Fragment != "" || u.Scheme == "" {
		return s.recordNodeTest(ctx, id, item, "node URL is invalid", "error", 0)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" && scheme != "socks5" && scheme != "socks5h" {
		return s.recordNodeTest(ctx, id, item, "node URL scheme is unsupported", "error", 0)
	}
	client, err := nodeHTTPClient(u)
	if err != nil {
		return s.recordNodeTest(ctx, id, item, "node URL is invalid", "error", 0)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, "https://muse.ai/api/session?vm_state=live", nil)
	if err != nil {
		return s.recordNodeTest(ctx, id, item, "node probe failed", "error", 0)
	}
	started := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(started).Milliseconds()
	if err != nil {
		return s.recordNodeTest(ctx, id, item, "node probe failed", "error", latency)
	}
	io.CopyN(io.Discard, resp.Body, 4096)
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusProxyAuthRequired {
		return s.recordNodeTest(ctx, id, item, "node credentials were rejected", "error", latency)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return s.recordNodeTest(ctx, id, item, fmt.Sprintf("node endpoint returned status %d", resp.StatusCode), "error", latency)
	}
	return s.recordNodeTest(ctx, id, item, "", "ok", latency)
}

func (s *Service) recordNodeTest(ctx context.Context, id string, item nodeRecord, message, status string, latency int64) (nodeRecord, error) {
	item.ID = id
	item.LatencyMS = latency
	item.TestedAt = time.Now().UTC().Format(time.RFC3339Nano)
	item.Error = message
	item.Status = status
	if err := s.db.Put(ctx, "node", id, item, nil); err != nil {
		return item, err
	}
	if message != "" {
		return item, errors.New(message)
	}
	return item, nil
}

func nodeHTTPClient(node *url.URL) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = nil
	scheme := strings.ToLower(node.Scheme)
	if scheme == "http" || scheme == "https" {
		tr.Proxy = http.ProxyURL(node)
	} else {
		dialer, err := socks5DialContext(node)
		if err != nil {
			return nil, err
		}
		tr.DialContext = dialer
	}
	return &http.Client{Transport: tr, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func socks5DialContext(proxyURL *url.URL) (func(context.Context, string, string) (net.Conn, error), error) {
	if proxyURL.Hostname() == "" || proxyURL.Port() == "" {
		return nil, errors.New("invalid socks5 proxy")
	}
	var user, pass string
	if proxyURL.User != nil {
		user = proxyURL.User.Username()
		pass, _ = proxyURL.User.Password()
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(proxyURL.Hostname(), proxyURL.Port()))
		if err != nil {
			return nil, errors.New("node proxy connection failed")
		}
		if deadline, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(deadline)
		}
		ok := false
		defer func() {
			if !ok {
				conn.Close()
			}
		}()
		method := []byte{5, 0}
		if user != "" {
			if len(user) > 255 || len(pass) > 255 {
				return nil, errors.New("node proxy credentials are invalid")
			}
			method = []byte{5, 2}
		}
		if _, err := conn.Write(append([]byte{5, 1}, method[1])); err != nil {
			return nil, errors.New("node proxy handshake failed")
		}
		var selected [2]byte
		if _, err := io.ReadFull(conn, selected[:]); err != nil || selected[0] != 5 || selected[1] == 255 {
			return nil, errors.New("node proxy authentication failed")
		}
		if selected[1] == 2 {
			auth := append([]byte{1, byte(len(user))}, []byte(user)...)
			auth = append(auth, byte(len(pass)))
			auth = append(auth, []byte(pass)...)
			if _, err := conn.Write(auth); err != nil {
				return nil, errors.New("node proxy authentication failed")
			}
			var ar [2]byte
			if _, err := io.ReadFull(conn, ar[:]); err != nil || ar[1] != 0 {
				return nil, errors.New("node proxy authentication failed")
			}
		}
		host, portText, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("node endpoint address is invalid")
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return nil, errors.New("node endpoint address is invalid")
		}
		atyp := byte(3)
		addr := []byte(host)
		if ip := net.ParseIP(host); ip != nil {
			if ip4 := ip.To4(); ip4 != nil {
				atyp, addr = 1, ip4
			} else {
				atyp, addr = 4, ip.To16()
			}
		} else if len(addr) > 255 {
			return nil, errors.New("node endpoint address is invalid")
		}
		request := []byte{5, 1, 0, atyp, byte(len(addr))}
		if atyp == 1 {
			request = []byte{5, 1, 0, atyp}
		}
		if atyp == 4 {
			request = []byte{5, 1, 0, atyp}
		}
		request = append(request, addr...)
		var p [2]byte
		binary.BigEndian.PutUint16(p[:], uint16(port))
		request = append(request, p[:]...)
		if _, err := conn.Write(request); err != nil {
			return nil, errors.New("node proxy connect failed")
		}
		var head [4]byte
		if _, err := io.ReadFull(conn, head[:]); err != nil || head[1] != 0 {
			return nil, errors.New("node proxy connect failed")
		}
		length := 0
		switch head[3] {
		case 1:
			length = 4
		case 4:
			length = 16
		case 3:
			var n [1]byte
			if _, err := io.ReadFull(conn, n[:]); err != nil {
				return nil, errors.New("node proxy connect failed")
			}
			length = int(n[0])
		default:
			return nil, errors.New("node proxy connect failed")
		}
		if _, err := io.CopyN(io.Discard, conn, int64(length+2)); err != nil {
			return nil, errors.New("node proxy connect failed")
		}
		ok = true
		return conn, nil
	}, nil
}

func jsonUnmarshal(data []byte, dst any) error {
	return json.Unmarshal(data, dst)
}
