package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/usage"
)

// The request rows' index cache (T-201). A page of /usage/requests read
// the whole period (up to 92 days) of the ledger: on 1.43M records, 92
// days of it 1.1M, every page of an area's took ~510ms and the console's
// ~1.3s. The first page of a query now keeps the rows it matched, by
// their keys alone (rowIndex), with the summary and the total; a page
// after it finds its rows there and reads the ledger only from its own
// oldest row on, for those rows' records.
//
// An entry is the query's exactly: the scope (each area's id, salt and
// every account the rows are matched by, as areaRows matches them), the
// period, zone, filters and the moment the first page was read. So
// area A's request can never be given B's rows, and an account claimed,
// let go of or renamed (a key's value replaced) since makes another key:
// the old entry is never read again, and ages out.
const (
	rowCacheTTL      = 60 * time.Second
	rowCacheMost     = 8         // entries
	rowCacheRowsMost = 3_000_000 // rows over every entry (~32 bytes each)
)

// rowCacheNow is the cache's clock, a variable for the tests.
var rowCacheNow = time.Now

// indexedRow is one matched row of a query: where it sorts, and whose it
// is (sc.areas[area], refs[ref]).
type indexedRow struct {
	key  rowKey
	area int32
	ref  int32
}

// rowIndex is a query's matched rows, the newest first, and what they
// came to.
type rowIndex struct {
	rows    []indexedRow
	refs    []string
	summary requestsSummary
	total   int
}

type rowCacheEntry struct {
	key  string
	made time.Time
	idx  *rowIndex
}

// rowCache holds the latest indexes, the most recently used first.
type rowCache struct {
	mu      sync.Mutex
	entries []*rowCacheEntry
}

var rowIndexes = &rowCache{}

func (c *rowCache) get(key string) *rowIndex {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := rowCacheNow()
	for i, e := range c.entries {
		if e.key != key {
			continue
		}
		if now.Sub(e.made) >= rowCacheTTL || now.Before(e.made) {
			c.entries = slices.Delete(c.entries, i, i+1)
			return nil
		}
		copy(c.entries[1:i+1], c.entries[:i])
		c.entries[0] = e
		return e.idx
	}
	return nil
}

func (c *rowCache) put(key string, idx *rowIndex) {
	if len(idx.rows) > rowCacheRowsMost {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := rowCacheNow()
	c.entries = slices.DeleteFunc(c.entries, func(e *rowCacheEntry) bool {
		return e.key == key || now.Sub(e.made) >= rowCacheTTL || now.Before(e.made)
	})
	c.entries = slices.Insert(c.entries, 0, &rowCacheEntry{key: key, made: now, idx: idx})
	rows := 0
	for i, e := range c.entries {
		if rows += len(e.idx.rows); i >= rowCacheMost || rows > rowCacheRowsMost {
			clear(c.entries[i:])
			c.entries = c.entries[:i]
			break
		}
	}
}

func (c *rowCache) drop(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = slices.DeleteFunc(c.entries, func(e *rowCacheEntry) bool { return e.key == key })
}

// rowIndexKey is the cache key of q over sc: everything the rows it
// matches are matched by.
func rowIndexKey(sc scopedAreas, q rowQuery) string {
	type acc struct {
		ID, Kind, Name string
		Added          time.Time
		Contested      bool
		Previous       []string
	}
	type scoped struct {
		ID, Salt string
		Accounts []acc
	}
	scope := make([]scoped, len(sc.areas))
	for i, a := range sc.areas {
		scope[i] = scoped{ID: a.ID, Salt: hex.EncodeToString(sc.salts[i]), Accounts: []acc{}}
		for _, x := range sc.accs[i] {
			scope[i].Accounts = append(scope[i].Accounts, acc{x.ID, x.Kind, x.Name, x.Added, x.Contested, x.previous})
		}
	}
	h := sha256.New()
	json.NewEncoder(h).Encode([]any{scope, string(q.period), q.loc.String(), q.since.UnixNano(), q.until.UnixNano(), q.asOf.UnixNano(), q.filters})
	return hex.EncodeToString(h.Sum(nil))
}

// keepsRow is whether q's filters keep r, area i's call by ref.
func (q rowQuery) keepsRow(sc scopedAreas, i int, ref string, r usage.Record) bool {
	for k, want := range q.filters {
		if len(want) == 0 {
			continue
		}
		var v string
		switch k {
		case "provider":
			v = r.Provider
		case "model":
			v = r.Model
		case "account":
			v = ref
		case "agent":
			v = usage.ReportedAs(r.Agent)
		case "callerKey":
			v = areaCallerKey(sc.salts[i], r.CallerKeyID)
		case "status":
			v = rowStatus(r)
		}
		if !slices.Contains(want, v) {
			return false
		}
	}
	return true
}

// buildRowIndex reads q's rows over sc from the ledger, once.
func buildRowIndex(sc scopedAreas, q rowQuery) *rowIndex {
	idx := &rowIndex{}
	refs := map[string]int32{}
	ords := map[[2]int64]int{}
	cost := usage.NewCoster()
	areaRows(sc.accs, q.since, q.until, func(i int, ref string, r usage.Record) bool {
		if r.Time.After(q.asOf) {
			return true // made after the first page was read
		}
		k := rowKey{T: r.Time.UnixNano(), R: r.RouteID}
		k.Ord = ords[[2]int64{k.T, k.R}]
		ords[[2]int64{k.T, k.R}]++
		if !q.keepsRow(sc, i, ref, r) {
			return true
		}
		c, priced := cost(r)
		idx.summary.add(r, c, priced)
		idx.total++
		n, ok := refs[ref]
		if !ok {
			n = int32(len(idx.refs))
			refs[ref] = n
			idx.refs = append(idx.refs, ref)
		}
		idx.rows = append(idx.rows, indexedRow{k, int32(i), n})
		return true
	})
	slices.SortFunc(idx.rows, func(x, y indexedRow) int {
		if x.key.before(y.key) {
			return -1
		}
		if y.key.before(x.key) {
			return 1
		}
		return 0
	})
	return idx
}

// pageOf is the rows of idx after q's cursor, at most q.limit, and
// whether more follow.
func (idx *rowIndex) pageOf(q rowQuery) ([]indexedRow, bool) {
	start := 0
	if q.after != nil {
		start = sort.Search(len(idx.rows), func(i int) bool { return q.after.before(idx.rows[i].key) })
	}
	end := min(start+q.limit, len(idx.rows))
	return idx.rows[start:end], end < len(idx.rows)
}

// pageRecords are the records of page, read from the ledger from its
// oldest row on (as buildRowIndex counted their order) until the last of
// them is found, false when one of them is gone from it (the ledger
// rewritten since). A page deep in the period reads about its own rows,
// not the rest of the period after them.
func pageRecords(sc scopedAreas, q rowQuery, page []indexedRow, refs []string) ([]usage.Record, bool) {
	out := make([]usage.Record, len(page))
	if len(page) == 0 {
		return out, true
	}
	at := make(map[rowKey]int, len(page))
	for i, row := range page {
		at[row.key] = i
	}
	since := time.Unix(0, page[len(page)-1].key.T)
	if since.Before(q.since) {
		since = q.since
	}
	ords := map[[2]int64]int{}
	found := 0
	areaRows(sc.accs, since, q.until, func(a int, ref string, r usage.Record) bool {
		if r.Time.After(q.asOf) {
			return true
		}
		k := rowKey{T: r.Time.UnixNano(), R: r.RouteID}
		k.Ord = ords[[2]int64{k.T, k.R}]
		ords[[2]int64{k.T, k.R}]++
		// the same row: its key, and the account the index says it is
		if i, ok := at[k]; ok && int(page[i].area) == a && refs[page[i].ref] == ref {
			out[i] = r
			delete(at, k)
			found++
		}
		return found < len(page) // every row of the page found: read no further
	})
	return out, found == len(page)
}
