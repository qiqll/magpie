package main

import (
	"bufio"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Observed is what a magpie's own records say routing did: what each
// window had left when it renewed (quota-history.json), how often a
// conversation moved account and what load it put on the pool
// (usage.jsonl), and why routing kept or moved each turn (routing/*.jsonl,
// .jsonl.gz). Nothing is simulated.
type Observed struct {
	Dir     string         `json:"dir"`
	Windows []WindowRecord `json:"windows"`
	Moves   []MoveRecord   `json:"moves"`
	Load    []LoadRecord   `json:"load"`
	Why     map[string]int `json:"why"`
	Routes  int            `json:"routes"`
	// Unread counts lines or files that couldn't be read, and Cut files
	// whose reading stopped part way: what they held is unknown, not
	// nothing.
	Unread map[string]int  `json:"unread,omitempty"`
	Cut    map[string]bool `json:"cut,omitempty"`
	// Placements is where routing put conversations, with what the
	// account had left then and how its stint there ended (placement.go).
	Placements *Placements `json:"placements,omitempty"`
	routes     []routeLine
}

// WindowRecord is one account's window over the history kept.
type WindowRecord struct {
	Provider string        `json:"provider"`
	Account  string        `json:"account"`
	Window   string        `json:"window"`
	Span     time.Duration `json:"span,omitempty"` // zero when the history doesn't say
	Points   int           `json:"points"`
	// Cycles is renewals seen (provider.QuotaPoint.StartsCycleAfter); of
	// them, Known had a reading within renewalNear of the renewal, and
	// LeftSum is what those readings had left, summed. A renewal whose
	// last reading was older is Stale: what it lost is unknown.
	Cycles   int     `json:"cycles"`
	Known    int     `json:"known"`
	Stale    int     `json:"stale"`
	LeftSum  float64 `json:"leftSum"`
	LastLeft float64 `json:"lastLeft"` // the latest reading
	// Resets are the renewals seen, so a scenario can be set on them.
	Resets []time.Time `json:"resets,omitempty"`
}

// renewalNear is how close to its renewal a window's last reading must be
// to say what the renewal lost.
const renewalNear = time.Hour

// MeanLeft is what a renewal lost on average, of those Known.
func (w WindowRecord) MeanLeft() float64 {
	if w.Known == 0 {
		return 0
	}
	return w.LeftSum / float64(w.Known)
}

// MoveRecord is one provider's conversations and their account moves.
// A conversation is a session and model, as affinity scopes a stick;
// subagents' and side requests' (a title, a summary) are left out.
type MoveRecord struct {
	Provider string `json:"provider"`
	Convs    int    `json:"convs"`    // with two or more answered turns
	Turns    int    `json:"turns"`    // their turns
	Moves    int    `json:"moves"`    // turns answered on another account than the one before
	Moved    int    `json:"moved"`    // conversations with a move
	Rewrites int64  `json:"rewrites"` // cache_write tokens of the turns that moved
}

// LoadRecord is the demand one provider's conversations put on it: what a
// scenario's Demand is drawn to match.
type LoadRecord struct {
	Provider string `json:"provider"`
	Days     int    `json:"days"`  // days with a request
	Convs    int    `json:"convs"` // conversations started
	// PerHour is conversations started an hour, by the hour of the day in
	// UTC, averaged over the days the log covers.
	PerHour  []float64 `json:"perHour"`
	Turns    float64   `json:"turns"`    // mean turns a conversation
	Gap      float64   `json:"gapSec"`   // median seconds between two turns
	Tokens   float64   `json:"tokens"`   // mean input+output+cache tokens a turn
	Accounts int       `json:"accounts"` // accounts that answered
}

// masker names an account by a hash salted for this run, so a report can
// be passed on without naming who the accounts are: the same account
// reads the same within one report, and nothing outside it can be
// matched to it.
type masker struct {
	show bool
	salt []byte
}

func newMasker(show bool) masker {
	m := masker{show: show, salt: make([]byte, 16)}
	rand.Read(m.salt)
	return m
}

func (m masker) mask(user string) string {
	if m.show || user == "" {
		return user
	}
	sum := sha256.Sum256(append(append([]byte{}, m.salt...), strings.ToLower(user)...))
	return "acct-" + hex.EncodeToString(sum[:4])
}

func observe(dir string, show bool) (Observed, error) {
	o := Observed{Dir: dir, Why: map[string]int{}, Unread: map[string]int{}, Cut: map[string]bool{}}
	m := newMasker(show)
	if err := observeQuotas(&o, filepath.Join(dir, "quota-history.json"), m); err != nil {
		return o, err
	}
	if err := observeUsage(&o, filepath.Join(dir, "usage.jsonl")); err != nil {
		return o, err
	}
	if err := observeRoutes(&o, filepath.Join(dir, "routing")); err != nil {
		return o, err
	}
	o.Placements = place(o.routes)
	o.routes = nil
	return o, nil
}

func observeQuotas(o *Observed, path string, m masker) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var hist map[string]map[string][]provider.QuotaPoint
	if err := json.Unmarshal(b, &hist); err != nil {
		o.Unread[filepath.Base(path)]++
		return nil
	}
	for key, lines := range hist {
		prov, user, _ := strings.Cut(key, "|")
		for name, pts := range lines {
			sort.SliceStable(pts, func(i, j int) bool { return pts[i].At.Before(pts[j].At) })
			w := WindowRecord{Provider: prov, Account: m.mask(user), Window: name, Points: len(pts)}
			for i, p := range pts {
				if p.Start != nil && p.ResetsAt != nil && w.Span == 0 {
					w.Span = p.ResetsAt.Sub(*p.Start)
				}
				if i > 0 && p.StartsCycleAfter(pts[i-1]) {
					w.Cycles++
					prev := pts[i-1]
					// when it renewed: the reset the last reading named, else
					// somewhere before this reading
					at := p.At
					if prev.ResetsAt != nil && !prev.ResetsAt.After(p.At) {
						at = *prev.ResetsAt
					}
					w.Resets = append(w.Resets, at)
					if at.Sub(prev.At) <= renewalNear {
						w.Known++
						w.LeftSum += prev.Left
					} else {
						w.Stale++
					}
				}
				w.LastLeft = p.Left
			}
			o.Windows = append(o.Windows, w)
		}
	}
	sort.Slice(o.Windows, func(i, j int) bool {
		a, b := o.Windows[i], o.Windows[j]
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		if a.Account != b.Account {
			return a.Account < b.Account
		}
		return a.Window < b.Window
	})
	return nil
}

type usageRow struct {
	Time       time.Time `json:"t"`
	Provider   string    `json:"provider"`
	Account    string    `json:"providerAccount"`
	Model      string    `json:"model"`
	Session    string    `json:"session"`
	Subagent   string    `json:"subagent"`
	Kind       string    `json:"kind"`
	Status     int       `json:"status"`
	Error      string    `json:"err"`
	Input      int64     `json:"in"`
	Output     int64     `json:"out"`
	CacheRead  int64     `json:"cache_read"`
	CacheWrite int64     `json:"cache_write"`
}

// scan reads path's lines into fn, counting a line that isn't JSON as
// unread and a reading that stopped (a line past the buffer, a bad gzip)
// as cut: the rest of the file is unknown.
func scan(o *Observed, name string, r io.Reader, fn func([]byte) error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		if fn(sc.Bytes()) != nil {
			o.Unread[name]++
		}
	}
	if sc.Err() != nil {
		o.Cut[name] = true
	}
}

func observeUsage(o *Observed, path string) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	byConv := map[string][]usageRow{}
	scan(o, "usage.jsonl", f, func(b []byte) error {
		var r usageRow
		if err := json.Unmarshal(b, &r); err != nil {
			return err
		}
		// a conversation's own turns: answered, of a session, not a
		// subagent's nor a side request's
		if r.Session == "" || r.Account == "" || r.Error != "" || r.Status >= 400 || r.Subagent != "" || r.Kind != "" {
			return nil
		}
		k := r.Provider + "\x00" + r.Session + "\x00" + r.Model
		byConv[k] = append(byConv[k], r)
		return nil
	})
	moves := map[string]*MoveRecord{}
	type load struct {
		days     map[string]bool
		perHour  [24]int
		convs    int
		turns    int
		tokens   int64
		gaps     []float64
		accounts map[string]bool
	}
	loads := map[string]*load{}
	for _, rows := range byConv {
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Time.Before(rows[j].Time) })
		prov := rows[0].Provider
		l := loads[prov]
		if l == nil {
			l = &load{days: map[string]bool{}, accounts: map[string]bool{}}
			loads[prov] = l
		}
		l.convs++
		l.perHour[rows[0].Time.UTC().Hour()]++
		l.turns += len(rows)
		for i, r := range rows {
			l.days[r.Time.UTC().Format(time.DateOnly)] = true
			l.tokens += r.Input + r.Output + r.CacheRead + r.CacheWrite
			l.accounts[r.Account] = true
			if i > 0 {
				l.gaps = append(l.gaps, r.Time.Sub(rows[i-1].Time).Seconds())
			}
		}
		if len(rows) < 2 {
			continue
		}
		m := moves[prov]
		if m == nil {
			m = &MoveRecord{Provider: prov}
			moves[prov] = m
		}
		m.Convs++
		m.Turns += len(rows)
		moved := false
		for i := 1; i < len(rows); i++ {
			if rows[i].Account != rows[i-1].Account {
				m.Moves++
				m.Rewrites += rows[i].CacheWrite
				moved = true
			}
		}
		if moved {
			m.Moved++
		}
	}
	for _, m := range moves {
		o.Moves = append(o.Moves, *m)
	}
	sort.Slice(o.Moves, func(i, j int) bool { return o.Moves[i].Provider < o.Moves[j].Provider })
	for prov, l := range loads {
		r := LoadRecord{Provider: prov, Days: len(l.days), Convs: l.convs, Accounts: len(l.accounts), PerHour: make([]float64, 24)}
		for h, n := range l.perHour {
			r.PerHour[h] = float64(n) / float64(max(1, r.Days))
		}
		r.Turns = float64(l.turns) / float64(l.convs)
		r.Tokens = float64(l.tokens) / float64(max(1, l.turns))
		if len(l.gaps) > 0 {
			sort.Float64s(l.gaps)
			r.Gap = l.gaps[len(l.gaps)/2]
		}
		o.Load = append(o.Load, r)
	}
	sort.Slice(o.Load, func(i, j int) bool { return o.Load[i].Provider < o.Load[j].Provider })
	return nil
}

// routeLine is what replay reads of a gateway.Route (trace.go): its
// conversation, who was weighed for it and who tried it.
type routeLine struct {
	Time       time.Time `json:"time"`
	Session    string    `json:"session"`
	Model      string    `json:"model"`
	Provider   string    `json:"provider"`
	Kind       string    `json:"kind"`
	Sub        string    `json:"sub"`
	SubagentID string    `json:"subagentId"`
	// Subagent: a Codex or Claude Desktop subagent's request (a
	// SubagentPick), told by its presence alone
	Subagent json.RawMessage `json:"subagent"`
	Pinned   string          `json:"pinned"`
	User     *struct {
		User   string   `json:"user"`
		Spares []string `json:"spares"`
	} `json:"user"`
	Affinity *struct {
		Why string `json:"why"`
	} `json:"affinity"`
	Order []weighedLine `json:"order"`
	Tries []tryLine     `json:"tries"`
}

// weighedLine is what replay reads of a gateway.Weighed.
type weighedLine struct {
	ID         string      `json:"id"`
	Who        string      `json:"who"`
	Known      bool        `json:"known"`
	Used       float64     `json:"used"`
	Renews     []time.Time `json:"renews"`
	UsedRenews *time.Time  `json:"usedRenews"`
	Rest       *struct{}   `json:"rest"`
	Barred     bool        `json:"barred"`
	Held       bool        `json:"held"`
	Capped     int         `json:"capped"`
	Reserve    int         `json:"reserve"`
	Lapsed     string      `json:"lapsed"`
	Fallback   bool        `json:"fallback"`
	Learns     bool        `json:"learns"`
	WeekLow    bool        `json:"weekLow"`
	Aside      bool        `json:"aside"`
	// fullest: "short" when the window Used is of renews before the
	// biggest (the five hours), "long" when it is the biggest, "" not
	// told; isReady: readyForNew. Set by slim, which drops the rest.
	fullest string
	isReady bool
}

// tryLine is what replay reads of a gateway.Try.
type tryLine struct {
	ID     string `json:"id"`
	Status int    `json:"status"`
	Fail   string `json:"fail"`
}

// observeRoutes reads routing/: every finished route, a file a day, the
// oldest dropped past 30 days or 200 MB (saveRoute, pruneHistory).
func observeRoutes(o *Observed, dir string) error {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") && !strings.HasSuffix(name, ".jsonl.gz") {
			continue
		}
		if strings.HasSuffix(name, ".jsonl") && slices.ContainsFunc(ents, func(g os.DirEntry) bool { return g.Name() == name+".gz" }) {
			continue // the same day, packed: read once
		}
		readRoutes(o, filepath.Join(dir, name))
	}
	return nil
}

func readRoutes(o *Observed, path string) {
	name := "routing/" + filepath.Base(path)
	f, err := os.Open(path)
	if err != nil {
		o.Cut[name] = true
		return
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		z, err := gzip.NewReader(f)
		if err != nil {
			o.Cut[name] = true
			return
		}
		defer z.Close()
		r = z
	}
	scan(o, name, r, func(b []byte) error {
		var l routeLine
		if err := json.Unmarshal(b, &l); err != nil {
			return err
		}
		o.Routes++
		if l.Affinity != nil && l.Affinity.Why != "" {
			o.Why[l.Affinity.Why]++
		}
		o.routes = append(o.routes, l.slim())
		return nil
	})
}
