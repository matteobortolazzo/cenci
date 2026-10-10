package incident

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type Incident struct {
	Key            string          `json:"key"`
	AlertID        string          `json:"alertId"`
	Status         string          `json:"status"`
	FiredAt        time.Time       `json:"firedAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
	NextAt         time.Time       `json:"nextAt"`
	Resource       Resource        `json:"resource"` // immutable trusted mapping captured at intake
	DeployedCommit string          `json:"deployedCommit"`
	Alert          json.RawMessage `json:"untrustedAlert"`
	Attempts       int             `json:"attempts"`
	ReservedUSD    float64         `json:"reservedUsd"`
	Report         string          `json:"report"`
	PR             string          `json:"pr,omitempty"`
	Publication    bool            `json:"publication"`
}

type Store struct{ Dir string }

func NewStore(dir string) *Store { return &Store{Dir: dir} }
func Key(id string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(id))))
	return hex.EncodeToString(sum[:])
}

func (s *Store) directory() error {
	if !filepath.IsAbs(s.Dir) {
		return fmt.Errorf("state directory must be absolute")
	}
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return err
	}
	return os.Chmod(s.Dir, 0700)
}

// Update holds a cross-process lock through read/modify/fsync/rename. Both file
// and directory are synced before returning, including before broker settlement.
func (s *Store) Update(fn func(map[string]*Incident) error) error {
	return s.transaction(true, fn)
}

func (s *Store) transaction(persist bool, fn func(map[string]*Incident) error) error {
	if err := s.directory(); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(s.Dir, "ledger.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("incident ledger lock timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
	rows := map[string]*Incident{}
	path := filepath.Join(s.Dir, "incidents.json")
	b, err := os.ReadFile(path)
	if err == nil {
		var disk struct {
			Version   int                  `json:"version"`
			Incidents map[string]*Incident `json:"incidents"`
		}
		if err := json.Unmarshal(b, &disk); err != nil {
			return err
		}
		if disk.Version != 1 || disk.Incidents == nil {
			return fmt.Errorf("unsupported incident ledger")
		}
		rows = disk.Incidents
	} else if !os.IsNotExist(err) {
		return err
	}
	for k, v := range rows {
		if v == nil || v.Key != k || Key(v.AlertID) != k {
			return fmt.Errorf("corrupt incident ledger")
		}
	}
	if err := fn(rows); err != nil {
		return err
	}
	if !persist {
		return nil
	}
	b, err = json.MarshalIndent(struct {
		Version   int                  `json:"version"`
		Incidents map[string]*Incident `json:"incidents"`
	}{1, rows}, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.Dir, ".ledger-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	dir, err := os.Open(s.Dir)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

func (s *Store) List() ([]Incident, error) {
	out := make([]Incident, 0)
	err := s.transaction(false, func(rows map[string]*Incident) error {
		for _, row := range rows {
			out = append(out, *row)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, err
}

func (s *Store) Accept(body []byte, c Config) error {
	if len(body) > 1024*1024 {
		return reject("alert payload exceeds 1 MiB")
	}
	var a struct {
		Schema string `json:"schemaId"`
		Data   struct {
			Essentials struct {
				ID        string    `json:"alertId"`
				Condition string    `json:"monitorCondition"`
				Targets   []string  `json:"alertTargetIDs"`
				Fired     time.Time `json:"firedDateTime"`
			} `json:"essentials"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &a); err != nil {
		return reject("invalid common alert schema: %v", err)
	}
	e := a.Data.Essentials
	if a.Schema != "azureMonitorCommonAlertSchema" || strings.TrimSpace(e.ID) == "" || len(e.ID) > 2048 || len(e.Targets) != 1 || e.Fired.IsZero() || (e.Condition != "Fired" && e.Condition != "Resolved") {
		return reject("common alert requires identity, fired time, one resource and Fired/Resolved condition")
	}
	r, allowed := c.resource(e.Targets[0])
	if !allowed {
		return reject("resource is not allowlisted")
	}
	return s.Update(func(rows map[string]*Incident) error {
		k := Key(e.ID)
		row, exists := rows[k]
		if exists {
			if !strings.EqualFold(row.Resource.ID, r.ID) || !row.FiredAt.Equal(e.Fired) {
				return reject("alert identity conflicts with persisted resource or fired time")
			}
			if e.Condition == "Resolved" {
				row.Status = "resolved"
				row.UpdatedAt = time.Now()
			}
			return nil
		}
		row = &Incident{Key: k, AlertID: e.ID, Status: "queued", FiredAt: e.Fired, UpdatedAt: time.Now(), Resource: r, DeployedCommit: r.deployed(e.Fired), Alert: append(json.RawMessage{}, body...)}
		if e.Condition == "Resolved" {
			row.Status = "resolved"
		} else if row.DeployedCommit == "" {
			row.Status = "review"
			row.Report = "Missing deployment metadata at alert fired time; human review required."
		}
		rows[k] = row
		return nil
	})
}

func (s *Store) Cancel(key string) error {
	return s.Update(func(rows map[string]*Incident) error {
		r, ok := rows[key]
		if !ok {
			return fmt.Errorf("incident not found")
		}
		if r.Status != "draft" && r.Status != "resolved" {
			r.Status = "cancelled"
			r.UpdatedAt = time.Now()
		}
		return nil
	})
}

// Recover is called only while holding the lifetime worker lock. No second
// worker may reclaim jobs while the original process/container is still alive.
func (s *Store) Recover() error {
	return s.Update(func(rows map[string]*Incident) error {
		for _, r := range rows {
			if r.Status == "running" || r.Status == "reconciling" {
				if r.Publication {
					r.Status = "publishing"
				} else {
					r.Status = "queued"
				}
				r.Report = "Worker interrupted; reserved budget retained."
			}
		}
		return nil
	})
}

func (s *Store) Claim(c Config, now time.Time) ([]Incident, error) {
	return s.claim(c, now, nil)
}

func (s *Store) claim(c Config, now time.Time, executing map[string]string) ([]Incident, error) {
	out := make([]Incident, 0)
	err := s.Update(func(rows map[string]*Incident) error {
		busy := map[string]bool{}
		active := 0
		counted := map[string]bool{}
		keys := make([]string, 0, len(rows))
		for k, r := range rows {
			keys = append(keys, k)
			if r.Status == "running" || r.Status == "reconciling" {
				busy[strings.ToLower(r.Resource.Repo)] = true
				active++
				counted[k] = true
			}
		}
		for key, repo := range executing {
			if !counted[key] {
				active++
				busy[strings.ToLower(repo)] = true
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			r := rows[k]
			if active >= c.Concurrency {
				break
			}
			if (r.Status != "queued" && r.Status != "publishing") || r.NextAt.After(now) || busy[strings.ToLower(r.Resource.Repo)] {
				continue
			}
			if r.Status == "publishing" {
				r.Status = "reconciling"
			} else {
				if r.Attempts >= c.MaxAttempts || r.ReservedUSD+c.BudgetUSD > c.BudgetUSD*float64(c.MaxAttempts)+1e-9 {
					r.Status = "review"
					r.Report += "\nExecution attempts or reserved cost exhausted; human review required."
					continue
				}
				r.Attempts++
				r.ReservedUSD += c.BudgetUSD
				r.Status = "running"
			}
			r.UpdatedAt = now
			busy[strings.ToLower(r.Resource.Repo)] = true
			active++
			out = append(out, *r)
		}
		return nil
	})
	return out, err
}

func (s *Store) MarkPublishing(key string) error {
	return s.Update(func(rows map[string]*Incident) error {
		r := rows[key]
		if r == nil || (r.Status != "running" && r.Status != "reconciling") {
			return fmt.Errorf("incident no longer active")
		}
		r.Publication = true
		return nil
	})
}

func (s *Store) WorkerLock() (io.Closer, error) {
	if err := s.directory(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(s.Dir, "worker.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("incident worker already running: %w", err)
	}
	return f, nil
}
