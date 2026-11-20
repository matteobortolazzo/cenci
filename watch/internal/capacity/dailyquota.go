package capacity

import (
	"encoding/json"
	"time"
)

const (
	quotaFileName = "daily-quota.json"
	quotaSchema   = 1
	dayLayout     = "2006-01-02"
	// maxUsed bounds a stored count, so a hand-edited file cannot overflow the
	// increment.
	maxUsed = 1_000_000_000
)

// DailyQuota counts dispatch launches per local calendar day in the capacity
// store. The count is persisted, so it survives restarts and is shared by every
// process using the same directory. The clock is passed to every call.
//
// A stored date other than today's reads as 0 used, whether it is earlier or
// later (after a clock change). A file the store cannot read with certainty is
// an error and is never reset or overwritten.
type DailyQuota struct {
	store *Store
}

// NewDailyQuota returns a counter over the capacity directory dir (see Dir).
func NewDailyQuota(dir string) *DailyQuota {
	return &DailyQuota{store: NewStore(dir)}
}

// day is the local calendar date of now.
func day(now time.Time) string { return now.In(time.Local).Format(dayLayout) }

// quotaState is the persisted file: {"schema":1,"date":"YYYY-MM-DD","used":N}.
type quotaState struct {
	Schema int    `json:"schema"`
	Date   string `json:"date"`
	Used   int    `json:"used"`
}

// decodeQuota decodes strictly: exact key names, exactly the three keys, one
// JSON object and nothing after it. A schema other than 1 is reported as
// unknown before the other keys are looked at, since a newer schema may have
// different ones.
func decodeQuota(data []byte) (quotaState, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return quotaState{}, ErrCorrupt
	}
	// json.Unmarshal of null into a value is a silent no-op, so a null would read
	// as the zero value and the next Record would overwrite it: a null is corrupt
	// for every key.
	rawSchema, ok := fields["schema"]
	if !ok || string(rawSchema) == "null" {
		return quotaState{}, ErrCorrupt
	}
	var schema int
	if err := json.Unmarshal(rawSchema, &schema); err != nil {
		return quotaState{}, ErrCorrupt
	}
	if schema != quotaSchema {
		return quotaState{}, ErrUnknownSchema
	}
	if len(fields) != 3 {
		return quotaState{}, ErrCorrupt
	}
	for _, raw := range fields {
		if string(raw) == "null" {
			return quotaState{}, ErrCorrupt
		}
	}
	st := quotaState{Schema: schema}
	if err := json.Unmarshal(fields["date"], &st.Date); err != nil {
		return quotaState{}, ErrCorrupt
	}
	if parsed, err := time.Parse(dayLayout, st.Date); err != nil || parsed.Format(dayLayout) != st.Date {
		return quotaState{}, ErrCorrupt
	}
	if err := json.Unmarshal(fields["used"], &st.Used); err != nil || st.Used < 0 || st.Used > maxUsed {
		return quotaState{}, ErrCorrupt
	}
	return st, nil
}

// load reads the persisted state. An absent file is the zero state.
func (q *DailyQuota) load() (quotaState, error) {
	data, exists, err := q.store.ReadFile(quotaFileName)
	if err != nil || !exists {
		return quotaState{}, err
	}
	return decodeQuota(data)
}

func usedOn(st quotaState, today string) int {
	if st.Date != today {
		return 0
	}
	return st.Used
}

// Used returns how many launches were recorded on now's local day. It reads
// under the store lock and reads 0 from an absent store or file. It never creates
// the store directory or the data file, but when the data file exists and `.lock`
// has gone missing it recreates `.lock` (an empty file): reading an existing file
// without the lock would race a writer's read-modify-write.
func (q *DailyQuota) Used(now time.Time) (int, error) {
	var used int
	err := q.store.withReadLock(quotaFileName, func() error {
		st, err := q.load()
		used = usedOn(st, day(now))
		return err
	})
	if err != nil {
		return 0, err
	}
	return used, nil
}

// Peek is Used without the lock: it never blocks, never creates anything and
// reads 0 from an absent store or file. A write replaces the file atomically,
// so it sees either the old or the new count.
func (q *DailyQuota) Peek(now time.Time) (int, error) {
	st, err := q.load()
	if err != nil {
		return 0, err
	}
	return usedOn(st, day(now)), nil
}

// Record counts one launch on now's local day. It reads and writes under the
// store lock, so concurrent recorders in any process lose no launch. A stored
// date other than today restarts the count at 1; an unreadable file is an
// error and is left untouched.
func (q *DailyQuota) Record(now time.Time) error {
	today := day(now)
	return q.store.WithLock(func() error {
		st, err := q.load()
		if err != nil {
			return err
		}
		used := usedOn(st, today)
		if used < maxUsed {
			used++
		}
		out, err := json.Marshal(quotaState{Schema: quotaSchema, Date: today, Used: used})
		if err != nil {
			return ErrIO
		}
		return q.store.WriteFile(quotaFileName, append(out, '\n'))
	})
}
